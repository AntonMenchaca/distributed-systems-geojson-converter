"""Run one experiment end to end and write its results folder.

    python tools/run.py <phase> <run> <trace> <tfvars|local> [--session-ends HH:MM] [--drain-s 1800]

    python tools/run.py local local-smoke traces/sample.csv local
    python tools/run.py p1 p1-base-anton-s11 traces/fail-s11.csv terraform/p1-base.tfvars

scripts/run.sh is the same command for shells that prefer it. Run it from the repo root with the
repo's venv (it needs boto3 and locust).

Local runs go against the Compose stack (`docker compose up --build`) and write
results/<run>/. AWS runs go against the stack Terraform deployed from terraform/ with the given
tfvars, using the AWS credentials in the environment (AWS_PROFILE), and write
results/<phase>/<run>/. This script never deploys or destroys anything.

In order:
  1. Pre-flight. Refuses to start (exit 2) unless the run name is new and well formed, every
     dataset in the trace is in the catalog, the API is healthy and every queue and DLQ is
     empty. On AWS also: the caller is a Learner Lab role, the lab session outlasts the
     run's time limit plus teardown, the deployed knobs match the tfvars, every service has
     running = desired (and any autoscaler is at its minimum), and this account's priced
     costs.csv rows plus this run's estimate stay under the budget.
  2. config.json: knobs, task definitions, git SHA, GDAL and estimates.json versions, seed.
  3. Start the samplers: queues.csv, tasks.csv and, locally, resources.csv.
  4. Replay the trace with Locust (open loop): locust_*.csv.
  5. Wait until every job is terminal, or until the trace's length plus --drain-s.
  6. collect.py (jobs.csv, attempts.csv), resources.csv from Container Insights on AWS, then verify.py.
  7. cost.py, when it exists: the costs.csv row and a check for stacks left running.
events.csv records each step's time; the task killer appends its kills to the same file.

Exit status: 0 the run finished and verified; 1 verify failed; 2 refused at pre-flight;
3 incomplete (time limit, collection failure or interrupted; the folder has an INCOMPLETE file
saying why).
"""
from __future__ import annotations

import argparse
import csv
import json
import os
import re
import shutil
import subprocess
import sys
import time
import urllib.request
from collections import Counter
from datetime import datetime, timedelta
from pathlib import Path

from sampling import append_rows, now_iso

REPO = Path(__file__).resolve().parents[1]
RUN_ID = re.compile(r"^[a-z0-9][a-z0-9-]{0,62}$")
AWS_PHASE = re.compile(r"^p[1-9]$")
TERMINAL = {"SUCCEEDED", "FAILED", "DEAD_LETTERED", "REJECTED"}

SESSION_HOURS = 4          # a Learner Lab session's length
MIN_SESSION_LEFT_MIN = 60  # the least session a run needs, however short its trace
TEARDOWN_MIN = 10          # on top of the run: collect, then terraform destroy
BUDGET_USD = 17.0          # per account: its costs.csv rows plus the next run's estimate

# us-east-1 on-demand prices (USD per hour).
FARGATE_VCPU_HR = 0.04048
FARGATE_GB_HR = 0.004445
ALB_HR = 0.0225

LOCAL_ENDPOINTS = {
    "AWS_ENDPOINT_URL": "http://localhost:4566",
    "AWS_ENDPOINT_URL_DYNAMODB": "http://localhost:8000",
    "AWS_ACCESS_KEY_ID": "local",
    "AWS_SECRET_ACCESS_KEY": "local",
    "AWS_DEFAULT_REGION": "us-east-1",
    "AWS_REGION": "us-east-1",
}
LOCAL_QUEUES = {q: f"http://localhost:4566/000000000000/{q}" for q in ("jobs", "jobs-small", "jobs-large")}
LOCAL_SERVICES = ["api", "worker"]
EVENT_COLUMNS = ["timestamp", "event", "task_id"]


class Refused(Exception):
    """Pre-flight failed: the run must not start."""


# --- names, folders and the trace ---------------------------------------------------------

def name_problems(phase: str, run: str, owner: str | None) -> list[str]:
    if not RUN_ID.match(run):
        return [f"run name {run!r}: only lowercase letters, digits and dashes, at most 63 characters"]
    if phase == "local":
        return [] if run.startswith("local-") else [f"local run name {run!r} must start with local-"]
    if not AWS_PHASE.match(phase):
        return [f"phase {phase!r}: want local or p1..p9"]
    if not re.match(rf"^{phase}-[a-z0-9-]+-{owner}-s\d+$", run):
        return [f"run name {run!r}: want {phase}-<change>-{owner}-s<seed> (README, 'Runs and outputs')"]
    return []


def results_dir(root: Path, phase: str, run: str) -> Path:
    return root / run if phase == "local" else root / phase / run


def seed_of(run: str) -> int | None:
    m = re.search(r"-s(\d+)$", run)
    return int(m.group(1)) if m else None


def load_trace(path: Path) -> list[dict]:
    with path.open(newline="") as f:
        return list(csv.DictReader(f))


def trace_duration_s(rows: list[dict]) -> float:
    return max((float(r["t_offset_s"]) for r in rows), default=0.0)


def trace_problems(rows: list[dict], catalog: dict) -> list[str]:
    if not rows:
        return ["the trace has no jobs"]
    missing = sorted({r["dataset_id"] for r in rows} - set(catalog))
    return [f"datasets not in the catalog: {', '.join(missing)}"] if missing else []


def load_catalog(path: Path) -> dict[str, dict]:
    with path.open(newline="") as f:
        return {row["dataset_id"]: row for row in csv.DictReader(f)}


# --- session ------------------------------------------------------------------------------

def session_end(credentials_mtime: datetime | None, override: str | None, now: datetime) -> datetime | None:
    """When the lab session ends. The credentials file is written when a session starts (its
    keys are pasted in from the lab's AWS Details), so it ends SESSION_HOURS after that, unless
    --session-ends gives the time shown in the lab."""
    if override:
        hh, mm = (int(x) for x in override.split(":"))
        end = now.replace(hour=hh, minute=mm, second=0, microsecond=0)
        # The lab shows a clock time: one at or before now is tomorrow's.
        return end if end > now else end + timedelta(days=1)
    if credentials_mtime is None:
        return None
    return credentials_mtime + timedelta(hours=SESSION_HOURS)


def session_problems(end: datetime | None, now: datetime, time_limit_s: float) -> list[str]:
    """The session must outlast the run's time limit (trace plus drain) and the teardown."""
    if end is None:
        return ["cannot tell when the lab session ends: no credentials file; pass --session-ends HH:MM"]
    left = (end - now).total_seconds() / 60
    needed = max(time_limit_s / 60, MIN_SESSION_LEFT_MIN) + TEARDOWN_MIN
    if left < needed:
        return [f"the lab session ends at {end:%H:%M} ({left:.0f} min left); this run needs {needed:.0f}. "
                "Restart the session and refresh the credentials, or pass --session-ends if this is wrong"]
    return []


def credentials_mtime() -> datetime | None:
    path = Path(os.environ.get("AWS_SHARED_CREDENTIALS_FILE", Path.home() / ".aws" / "credentials"))
    return datetime.fromtimestamp(path.stat().st_mtime) if path.exists() else None


# --- knobs --------------------------------------------------------------------------------

_TFVAR = re.compile(r'^\s*([A-Za-z_][A-Za-z0-9_]*)\s*=\s*("([^"]*)"|[^#\s]+)')


def parse_tfvars(path: Path) -> dict[str, str]:
    """name = value lines (strings, numbers, booleans): the only form our tfvars files use."""
    out = {}
    for line in path.read_text().splitlines():
        m = _TFVAR.match(line)
        if m:
            out[m.group(1)] = m.group(3) if m.group(3) is not None else m.group(2)
    return out


_SHAPE = re.compile(r"^(\d+)x([\d.]+)-([\d.]+)-(\d+)gb$")
# tfvars variable -> (index in TASK_SHAPE, divisor that turns the variable into the shape's unit)
_SHAPE_VARS = {"worker_count": (0, 1), "worker_cpu": (1, 1024), "worker_memory": (2, 1024), "worker_ephemeral_gb": (3, 1)}


def knob_problems(tfvars: dict[str, str], knobs: dict[str, str], deployed_prefix: str) -> list[str]:
    """Every knob the tfvars sets must equal what is deployed (`terraform output knobs`)."""
    problems = []
    if "prefix" in tfvars and tfvars["prefix"] != deployed_prefix:
        problems.append(f"tfvars prefix {tfvars['prefix']} but the deployed stack is {deployed_prefix}")
    for var, value in tfvars.items():
        knob = var.upper()
        if knob in knobs and value != knobs[knob]:
            problems.append(f"{knob}: tfvars {value}, deployed {knobs[knob]}")
    shape = _SHAPE.match(knobs.get("TASK_SHAPE", ""))
    for var, (i, div) in _SHAPE_VARS.items():
        if var in tfvars and shape and float(tfvars[var]) / div != float(shape.group(i + 1)):
            problems.append(f"TASK_SHAPE: tfvars {var} = {tfvars[var]}, deployed shape {knobs['TASK_SHAPE']}")
    return problems


def declared_variables() -> set[str]:
    """Every variable the root module declares (terraform/*.tf)."""
    return {m for f in (REPO / "terraform").glob("*.tf")
            for m in re.findall(r'^variable\s+"([^"]+)"', f.read_text(), flags=re.M)}


def undeclared_problems(tfvars: dict[str, str], declared: set[str]) -> list[str]:
    # Terraform only warns about these, so a misspelt knob would deploy its default.
    return [f"tfvars sets {var}, which terraform/ does not declare (a typo?)"
            for var in tfvars if var not in declared]


def knob_names() -> set[str]:
    """Every knob in knobs.md's table (its first column)."""
    names = set()
    for line in (REPO / "knobs.md").read_text().splitlines():
        if line.startswith("| `"):
            names |= set(re.findall(r"`([A-Z_]+)`", line.split("|")[1]))
    return names


# --- stack state --------------------------------------------------------------------------

def queue_problems(rows: list[dict]) -> list[str]:
    return [f"queue {r['queue']} is not empty ({r['visible']} visible, {r['in_flight']} in flight)"
            for r in rows if r["visible"] or r["in_flight"]]


def service_problems(services: list[dict], scalable_targets: list[dict]) -> list[str]:
    problems = [f"service {s['serviceName']}: {s['runningCount']} running, {s['desiredCount']} desired"
                for s in services if s["runningCount"] != s["desiredCount"]]
    minimum = {t["ResourceId"].rsplit("/", 1)[-1]: t["MinCapacity"] for t in scalable_targets}
    for s in services:
        if s["serviceName"] in minimum and s["desiredCount"] != minimum[s["serviceName"]]:
            problems.append(f"service {s['serviceName']}: autoscaler at {s['desiredCount']}, "
                            f"minimum {minimum[s['serviceName']]}")
    return problems


# --- cost ---------------------------------------------------------------------------------

def estimate_usd(vcpu: float, gb: float, hours: float) -> float:
    return hours * (vcpu * FARGATE_VCPU_HR + gb * FARGATE_GB_HR + ALB_HR)


def spent_usd(costs: Path, prefix: str) -> tuple[float, list[str]]:
    """What this account's earlier runs cost, and a problem for each of its rows with no price."""
    if not costs.exists():
        return 0.0, []
    with costs.open(newline="") as f:
        rows = [r for r in csv.DictReader(f) if r["account_prefix"] == prefix]
    unpriced = [r["run_id"] for r in rows if not r["usd"].strip()]
    problems = [f"costs.csv row {run_id} has no usd: fill in its price before the next run" for run_id in unpriced]
    return sum(float(r["usd"]) for r in rows if r["usd"].strip()), problems


def budget_problems(spent: float, estimate: float) -> list[str]:
    if spent + estimate >= BUDGET_USD:
        return [f"this account's runs in costs.csv cost ${spent:.2f}; this run adds up to ${estimate:.2f}, past the ${BUDGET_USD:.0f} budget"]
    return []


# --- events and outcome files -------------------------------------------------------------

def event(path: Path, name: str, task_id: str = "") -> None:
    append_rows(path, EVENT_COLUMNS, [{"timestamp": now_iso(), "event": name, "task_id": task_id}])
    print(f"run: {name}", flush=True)


def incomplete(folder: Path, reason: str, aws: bool) -> None:
    text = f"{now_iso()} {reason}\n"
    if aws:
        text += "The stack is still deployed. Rerun under a new run name, or `terraform destroy` before the session ends.\n"
    (folder / "INCOMPLETE").write_text(text)
    print(f"run: INCOMPLETE: {reason}", file=sys.stderr)


# --- the target: local Compose or a deployed AWS stack ------------------------------------

def sh(args: list[str], env: dict | None = None, check: bool = True) -> str:
    return subprocess.run(args, cwd=REPO, env=env, capture_output=True, text=True, check=check).stdout.strip()


def service_args(services: list[str]) -> list[str]:
    return [a for s in services for a in ("--service", s)]


def gdal_version_of(text: str) -> str:
    """'GDAL 3.13.3 "Iowa City", released 2026/...' -> '3.13.3'."""
    m = re.search(r"GDAL (\d+\.\d+\.\d+)", text)
    return m.group(1) if m else "unknown"


def git_sha() -> str:
    sha = sh(["git", "rev-parse", "HEAD"])
    return sha + ("-dirty" if sh(["git", "status", "--porcelain", "--untracked-files=no"]) else "")


class Local:
    aws = False

    def __init__(self):
        self.env = {**os.environ, **LOCAL_ENDPOINTS}
        self.base_url = "http://localhost:8080"
        self.table = "jobs"
        self.catalog = REPO / "datasets/local/datasets.csv"
        self.queues = dict(LOCAL_QUEUES)
        self.owner = None

    def preflight(self, args) -> list[str]:
        return []

    def knobs(self) -> dict:
        cfg = json.loads(sh(["docker", "compose", "config", "--format", "json"]))
        names = knob_names()
        knobs = {}
        for service in LOCAL_SERVICES:
            for k, v in (cfg["services"][service].get("environment") or {}).items():
                if k in names:
                    knobs[k] = v
        return dict(sorted(knobs.items()))

    def task_defs(self) -> dict:
        ids = sh(["docker", "compose", "ps", "-q", *LOCAL_SERVICES]).split()
        inspect = json.loads(sh(["docker", "inspect", *ids])) if ids else []
        return {c["Config"]["Labels"]["com.docker.compose.service"]: c["Image"] for c in inspect}

    def gdal_version(self) -> str:
        return gdal_version_of(sh(["docker", "compose", "exec", "-T", "worker", "gdalinfo", "--version"]))

    def tasks_args(self, folder: Path) -> list[str]:
        return ["--out", str(folder / "tasks.csv"), "--docker", *service_args(LOCAL_SERVICES)]

    def samplers(self, folder: Path) -> list[list[str]]:
        # Docker keeps no history of CPU and memory, so resources are sampled during the run.
        return [["tools/watch_ecs.py", *self.tasks_args(folder)],
                ["tools/resources.py", "--out", str(folder / "resources.csv"), "--docker", *service_args(LOCAL_SERVICES)]]

    def after_run(self, folder: Path, started: str, ended: str) -> None:
        pass

    def verify(self, run: str, folder: Path) -> int:
        out = folder.relative_to(REPO) / "verify.json"
        return subprocess.run(["docker", "compose", "run", "--rm", "tools", "python", "tools/verify.py",
                               "--run-id", run, "--out", out.as_posix()], cwd=REPO).returncode


class AWS:
    aws = True

    def __init__(self, tfvars: Path):
        self.tfvars_path = tfvars
        self.env = dict(os.environ)
        out = {k: v["value"] for k, v in json.loads(sh(["terraform", f"-chdir={REPO / 'terraform'}", "output", "-json"])).items()}
        if not out:
            raise Refused("terraform has no outputs: nothing is deployed from terraform/")
        self.out = out
        self.base_url = out["alb_url"]
        self.table = out["jobs_table"]
        self.catalog = REPO / "datasets/datasets.csv"
        self.prefix = out["prefix"]
        self.owner = self.prefix.removeprefix("gc-")
        dlqs = {(k if k.endswith("dlq") else f"{k}-dlq"): v for k, v in (out.get("dlq_urls") or {}).items()}
        self.queues = {**out["queue_urls"], **dlqs}
        workers = out.get("worker_service_names") or {"worker": out["worker_service_name"]}
        self.services = [out["api_service_name"], *workers.values()]

    def preflight(self, args) -> list[str]:
        import boto3

        problems = []
        arn = boto3.client("sts").get_caller_identity()["Arn"]
        if "assumed-role/voclabs/" not in arn:
            problems.append(f"caller {arn} is not a Learner Lab session (assumed-role/voclabs/); check AWS_PROFILE")
        now = datetime.now()
        problems += session_problems(session_end(credentials_mtime(), args.session_ends, now), now, args.time_limit_s)
        tfvars = parse_tfvars(self.tfvars_path)
        problems += undeclared_problems(tfvars, declared_variables())
        problems += knob_problems(tfvars, self.out["knobs"], self.prefix)

        ecs = boto3.client("ecs")
        services = ecs.describe_services(cluster=self.out["cluster_name"], services=self.services)["services"]
        targets = boto3.client("application-autoscaling").describe_scalable_targets(
            ServiceNamespace="ecs", ResourceIds=[f"service/{self.out['cluster_name']}/{s}" for s in self.services],
        )["ScalableTargets"]
        problems += service_problems(services, targets)

        vcpu = gb = 0.0
        for s in services:
            td = ecs.describe_task_definition(taskDefinition=s["taskDefinition"])["taskDefinition"]
            vcpu += s["desiredCount"] * int(td["cpu"]) / 1024
            gb += s["desiredCount"] * int(td["memory"]) / 1024
        self.estimate = estimate_usd(vcpu, gb, hours=args.time_limit_s / 3600)
        spent, unpriced = spent_usd(REPO / "results/costs.csv", self.prefix)
        problems += unpriced + budget_problems(spent, self.estimate)
        return problems

    def knobs(self) -> dict:
        return dict(sorted(self.out["knobs"].items()))

    def task_defs(self) -> dict:
        return self.out.get("task_definitions") or {}

    def gdal_version(self) -> str:
        # The worker image is built from the GDAL image WORKER_IMAGE names (terraform/images.tf).
        images = (REPO / "terraform/images.tf").read_text()
        m = re.search(rf'{re.escape(self.out["knobs"]["WORKER_IMAGE"])}\s*=\s*"[^"]*-(\d+\.\d+\.\d+)@', images)
        return m.group(1) if m else "unknown"

    def tasks_args(self, folder: Path) -> list[str]:
        return ["--out", str(folder / "tasks.csv"), "--cluster", self.out["cluster_name"], *service_args(self.services)]

    def samplers(self, folder: Path) -> list[list[str]]:
        # Container Insights keeps CPU and memory, so resources.csv is queried after the run.
        return [["tools/watch_ecs.py", *self.tasks_args(folder)]]

    def after_run(self, folder: Path, started: str, ended: str) -> None:
        rc = subprocess.run([sys.executable, "tools/resources.py", "--out", str(folder / "resources.csv"),
                             "--cluster", self.out["cluster_name"], "--start", started, "--end", ended],
                            cwd=REPO, env=self.env).returncode
        if rc != 0:
            raise RuntimeError("resources.py failed")

    def verify(self, run: str, folder: Path) -> int:
        # verify.py needs ogrinfo, so it runs in the tools image, with this session's AWS profile.
        sh(["docker", "build", "-q", "-f", "tools/Dockerfile", "-t", "gc-tools", "."])
        out = folder.relative_to(REPO) / "verify.json"
        region = self.env.get("AWS_REGION") or self.env.get("AWS_DEFAULT_REGION") or "us-east-1"
        return subprocess.run([
            "docker", "run", "--rm", "-v", f"{REPO}:/repo", "-v", f"{Path.home() / '.aws'}:/root/.aws:ro",
            "-e", f"AWS_PROFILE={self.env.get('AWS_PROFILE', 'default')}", "-e", f"AWS_REGION={region}",
            "-e", f"AWS_DEFAULT_REGION={region}", "gc-tools", "python", "tools/verify.py", "--run-id", run,
            "--out", out.as_posix(), "--table", self.table, "--bucket", self.out["bucket"],
            "--catalog", "datasets/datasets.csv",
        ], cwd=REPO).returncode


# --- the run ------------------------------------------------------------------------------

def locust_bin() -> str | None:
    """The locust command next to this Python. `python -m locust` would not do: run from the
    repo root, it imports the repo's locust/ folder instead of the package."""
    return shutil.which("locust", path=str(Path(sys.executable).parent))


def http_ok(url: str) -> bool:
    try:
        with urllib.request.urlopen(url, timeout=5) as r:
            return r.status == 200
    except OSError:
        return False


def job_statuses(target, run: str) -> Counter:
    import boto3
    from verify import load_jobs

    return Counter(j["status"] for j in load_jobs(boto3.client("dynamodb"), target.table, run))


def preflight(args, target, trace_rows: list[dict], folder: Path) -> list[str]:
    import boto3
    import watch_queues

    problems = name_problems(args.phase, args.run, target.owner)
    if folder.exists():
        problems.append(f"{folder.relative_to(REPO)} exists: results folders are never reused; pick a new run name")
    problems += trace_problems(trace_rows, load_catalog(target.catalog))
    if locust_bin() is None:
        problems.append("locust is not installed in this Python: pip install -r locust/requirements.txt")
    if not http_ok(f"{target.base_url}/health"):
        return problems + [f"API not healthy at {target.base_url}/health"]
    problems += queue_problems(watch_queues.sample(boto3.client("sqs"), target.queues, ts=""))
    if job_statuses(target, args.run):
        problems.append(f"run_id {args.run} already has jobs in the table; pick a new run name")
    return problems + target.preflight(args)


def config(args, target, started_at: str) -> dict:
    estimates = json.loads((REPO / "config/estimates.json").read_text())
    return {
        "run_id": args.run,
        "phase": args.phase,
        "owner": target.owner,
        "account_prefix": getattr(target, "prefix", "local"),
        "git_sha": git_sha(),
        "gdal_version": target.gdal_version(),
        "trace_file": Path(args.trace).as_posix(),
        "seed": seed_of(args.run),
        "knobs": target.knobs(),
        "task_defs": target.task_defs(),
        "started_at": started_at,
        "estimates_version": estimates.get("version"),
    }


def stop(procs: list[subprocess.Popen]) -> None:
    for p in procs:
        if p.poll() is None:
            p.terminate()
    for p in procs:
        try:
            p.wait(timeout=10)
        except subprocess.TimeoutExpired:
            p.kill()


def main(argv: list[str] | None = None) -> int:
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("phase", help="local, or p1..p9")
    ap.add_argument("run", help="the run name, which is also every job's run_id")
    ap.add_argument("trace", help="trace CSV from tools/gen_trace.py")
    ap.add_argument("tfvars", help="the tfvars file the stack was deployed with, or local")
    ap.add_argument("--session-ends", metavar="HH:MM", help="when the lab session ends, if not 4 h after the credentials were written")
    ap.add_argument("--drain-s", type=float, default=1800, help="how long to wait for jobs after the trace ends (default 1800)")
    ap.add_argument("--poll-s", type=float, default=15)
    args = ap.parse_args(argv)

    os.chdir(REPO)
    if (args.phase == "local") != (args.tfvars == "local"):
        ap.error("a local run takes phase local and tfvars local; an AWS run takes p1..p9 and a tfvars file")
    trace = Path(args.trace)
    trace_rows = load_trace(trace)
    args.time_limit_s = trace_duration_s(trace_rows) + args.drain_s

    try:
        target = Local() if args.phase == "local" else AWS(Path(args.tfvars))
        os.environ.update(target.env)
        folder = results_dir(REPO / "results", args.phase, args.run)
        problems = preflight(args, target, trace_rows, folder)
    except Refused as e:
        problems = [str(e)]
    if problems:
        print("run: refused:\n  - " + "\n  - ".join(problems), file=sys.stderr)
        return 2

    folder.mkdir(parents=True)
    events = folder / "events.csv"
    started_at = now_iso()
    (folder / "config.json").write_text(json.dumps(config(args, target, started_at), indent=2) + "\n")
    print(f"run: {args.run} -> {folder.relative_to(REPO)}")
    if target.aws:
        print("run: post the start in #runs")
    event(events, "start")

    samplers = [subprocess.Popen([sys.executable, *s], cwd=REPO, env=target.env) for s in
                [["tools/watch_queues.py", "--out", str(folder / "queues.csv"),
                  *[a for k, v in target.queues.items() for a in ("--queue", f"{k}={v}")]],
                 *target.samplers(folder)]]
    reason = None
    try:
        event(events, "replay_start")
        locust = subprocess.Popen([
            locust_bin(), "-f", "locust/locustfile.py", "--headless", "-u", "1", "-r", "1",
            "--host", target.base_url, "--trace", str(trace), "--run-id", args.run,
            "--csv", str(folder / "locust"), "--run-time", f"{int(trace_duration_s(trace_rows) + 600)}s",
        ], cwd=REPO, env=target.env)
        deadline = time.monotonic() + args.time_limit_s
        while locust.poll() is None and time.monotonic() < deadline:
            time.sleep(1)
        if locust.poll() is None:
            locust.terminate()
            reason = "replay did not finish before the time limit"
        event(events, "replay_end")

        while reason is None:
            counts = job_statuses(target, args.run)
            waiting = sum(n for s, n in counts.items() if s not in TERMINAL)
            print(f"run: {dict(counts)}", flush=True)
            if counts and not waiting:
                event(events, "all_terminal")
                break
            if time.monotonic() >= deadline:
                reason = f"time limit: {waiting} jobs not terminal {args.drain_s:.0f} s after the trace ended"
                event(events, "time_limit")
                break
            time.sleep(args.poll_s)
    except KeyboardInterrupt:
        reason = "interrupted"
        event(events, "interrupted")
    finally:
        stop(samplers)

    # Collect whatever happened, even for an unfinished run: it is the evidence of why.
    ended_at = now_iso()
    # One last look at the tasks, so tasks.csv has every stop time.
    subprocess.run([sys.executable, "tools/watch_ecs.py", *target.tasks_args(folder), "--once"], cwd=REPO, env=target.env)
    try:
        if subprocess.run([sys.executable, "tools/collect.py", "--run-id", args.run, "--out", str(folder / "jobs.csv"),
                           "--attempts-out", str(folder / "attempts.csv"),
                           "--table", target.table], cwd=REPO, env=target.env).returncode != 0:
            raise RuntimeError("collect.py failed")
        target.after_run(folder, started_at, ended_at)
    except Exception as e:  # the run's data could not be collected: say so in the folder
        incomplete(folder, f"collection failed: {e}", target.aws)
        return 3
    verified = target.verify(args.run, folder) == 0
    event(events, "end")

    if (REPO / "tools/cost.py").exists():
        subprocess.run([sys.executable, "tools/cost.py", "--run-dir", str(folder)], cwd=REPO, env=target.env)
        if target.aws:
            subprocess.run([sys.executable, "tools/cost.py", "--open-stacks", "--prefix", target.prefix], cwd=REPO, env=target.env)
    elif target.aws:
        print("run: no tools/cost.py yet: add this run's row to results/costs.csv by hand")
    if target.aws:
        print("run: when you are done, `terraform destroy` and post the teardown in #runs")

    if reason:
        incomplete(folder, reason, target.aws)
        return 3
    print(f"run: {'verified' if verified else 'VERIFY FAILED'}: {folder.relative_to(REPO)}")
    return 0 if verified else 1


if __name__ == "__main__":
    sys.exit(main())
