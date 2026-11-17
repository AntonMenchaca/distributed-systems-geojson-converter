"""Static guards on terraform/. Learner Lab cannot create IAM, and every
knob in knobs.md must be settable from Terraform."""
import re
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
TF = ROOT / "terraform"


def tf_files() -> list[Path]:
    files = sorted(p for p in TF.rglob("*.tf") if ".terraform" not in p.parts)
    assert files, "no terraform/*.tf files"
    return files


def test_no_iam_resources():
    # Learner Lab cannot create roles or policies; every role is data.aws_iam_role.lab_role (LabRole).
    offenders = [f"{p.relative_to(ROOT)}: {m}" for p in tf_files()
                 for m in re.findall(r'resource\s+"(aws_iam_[a-z_]+)"', p.read_text())]
    assert offenders == []


def test_no_aws_s3_bucket_resource():
    # Learner Lab's SCP denies s3:GetBucketObjectLockConfiguration, which aws_s3_bucket reads
    # after every create and refresh, so a declared aws_s3_bucket can never apply there.
    offenders = [str(p.relative_to(ROOT)) for p in tf_files()
                 if re.search(r'resource\s+"aws_s3_bucket"\s', p.read_text())]
    assert offenders == []


def test_roles_come_from_labrole():
    root = "\n".join(p.read_text() for p in TF.glob("*.tf"))
    assert re.search(r'data\s+"aws_iam_role"\s+"lab_role"\s*{\s*name\s*=\s*"LabRole"', root)
    for attr, value in re.findall(r'(execution_role_arn|task_role_arn)\s*=\s*([^\n]+)', root):
        assert value.strip() == "data.aws_iam_role.lab_role.arn", f"{attr} = {value}"
    assert re.findall(r'(execution_role_arn|task_role_arn)\s*=', root), "no task roles wired in the root module"


def knob_tf_variables() -> set[str]:
    names = set()
    for line in (ROOT / "knobs.md").read_text().splitlines():
        cells = [c.strip() for c in line.strip().strip("|").split("|")]
        if line.startswith("|") and cells[0].startswith("`"):
            names |= set(re.findall(r"`([a-z_0-9]+)`", cells[2]))
    return names


def test_every_knob_has_a_terraform_variable():
    declared = set(re.findall(r'variable\s+"([a-z_0-9]+)"', (TF / "variables.tf").read_text()))
    missing = knob_tf_variables() - declared
    assert len(knob_tf_variables()) >= 18
    assert missing == set()
    assert "prefix" in declared


def test_resource_names_use_the_guarded_prefix():
    # local.prefix depends on the Learner Lab precondition; var.prefix does not, so a name
    # built from var.prefix would let `terraform apply -target=...` skip the guard.
    for p in (TF / "main.tf", TF / "images.tf"):
        lines = [l for l in p.read_text().splitlines()
                 if "var.prefix" in l and not l.lstrip().startswith("#") and "prefix = \"${var.prefix}" not in l]
        assert lines == [], f"{p.name}: {lines}"
