# Security

This is a course project. Every stack runs in one member's AWS Academy Learner Lab account, only while a run is in progress, and is destroyed in the same sitting. It processes only public data (Microsoft US Building Footprints and generated files): no personal data, no user accounts, no payments. This file sets the rules we follow and lists what is and isn't protected, so nobody has to guess.

## Reporting a problem

The repository is private. If you find a leaked credential, an exposed stack or a vulnerability:

1. **A leaked AWS credential:** end the Learner Lab session at once (it invalidates the session credentials), then tell the team in the Teams chat.
2. **Anything else:** post in the Teams chat with an @mention, not in a GitHub issue or PR comment. Fix it in a normal PR once the team agrees.
3. If a secret ever reaches a commit, removing it in a later commit is not enough: the history still has it. Treat it as leaked (step 1), then ask Anton before rewriting history.

## Rules for everyone

**Secrets**
- Never commit credentials, `.env` files, Terraform state (`*.tfstate`) or tfvars. `.gitignore` and `terraform/.gitignore` ignore them; don't force-add them.
- AWS access comes only from your Learner Lab profile (`export AWS_PROFILE=<your profile>`). Never put keys in code, scripts, Dockerfiles, CI or the Compose file.
- The only credentials in the repo are the emulator's dummy `local` / `local` values (`docker-compose.yml`, `local/init.sh`, `tools/run.py`, CI). They work only against LocalStack and DynamoDB Local.
- Don't paste credentials, session tokens or `~/.aws` contents into chats, issues, PRs or AI tools.

**The right account**
- Before any AWS command, run `aws sts get-caller-identity`: the ARN must contain `assumed-role/voclabs/`. Terraform refuses to plan for any other caller, and `scripts/run.sh` refuses to start.
- If your shell defaults to another AWS profile (a work account, for example), set `AWS_PROFILE` in every terminal you use for this project.

**Stacks**
- Deploy, run, collect, destroy, in one sitting. Post the start and the teardown in the team chat.
- After `terraform destroy`, check that nothing is left: `python tools/cost.py --open-stacks --prefix gc-<you>` should print `nothing running`.

**Untrusted files**
- Files classmates send for "Break it" are untrusted input. Upload them unopened to the run bucket's `datasets/` prefix and let the workers convert them; don't open them in desktop GIS tools or run GDAL on them outside a container.

## What is protected

| Area | Protection | Where |
|------|------------|-------|
| AWS account | Terraform checks the caller is a Learner Lab session; tasks use the lab's existing `LabRole`; the stack creates no IAM users, roles or keys | `terraform/main.tf` |
| S3 bucket | All public access blocked; encrypted at rest (S3's default SSE-S3); deleted with the stack | `terraform/modules/s3` |
| SQS and DynamoDB | Encrypted at rest by AWS default; reachable only with the account's credentials | `terraform/modules/sqs`, `terraform/modules/dynamodb` |
| Network | API tasks accept traffic only from the load balancer; worker tasks accept no inbound traffic | `terraform/modules/network` |
| API input | `run_id` must match `^[a-z0-9][a-z0-9-]{0,62}$`; `dataset_id` must be in the catalog, so a request can't name an arbitrary S3 key; formats are an enum | `api/internal/jobs/handlers.go`, `api.yaml` |
| Conversion | `ogr2ogr` and `ogrinfo` run as child processes with argument lists, never through a shell | `worker/internal/gdal/runner.go` |
| Fault injection | Per-job chaos flags do nothing unless the stack is deployed with `chaos_enabled = "1"` (default `"0"`) | `terraform/variables.tf`, `worker/internal/chaos` |
| CI | Workflows have read-only repository access (`permissions: contents: read`) and use no secrets | `.github/workflows/ci.yml` |
| Base images | The worker and tools images build on official OSGeo GDAL images pinned by digest, so a rebuild can't pick up a changed image | `worker/Dockerfile`, `tools/Dockerfile`, `docs/decisions/002-gdal-image.md` |

## Known gaps (accepted for this project)

These are deliberate trade-offs for a short-lived course stack. Each would need fixing before anyone ran this service for real.

| Gap | Risk while a stack is up | Why we accept it | What a real deployment needs |
|-----|--------------------------|------------------|------------------------------|
| **The API has no authentication** (`security: []` in `api.yaml`) and the load balancer accepts HTTP from anywhere | Anyone who finds the load balancer's URL can submit jobs, which costs money and pollutes a run's results; with chaos enabled, they could also crash workers | Stacks live for about an hour, URLs are random and unpublished, and jobs can only read pre-staged datasets. An IP allow-list would add a step to every deploy and break runs from changing networks or the EC2 upload host | API keys or IAM auth, and an allow-list of client IPs on the load balancer |
| **Plain HTTP, no TLS** | Requests and responses can be read in transit | No secrets or personal data cross the API | An HTTPS listener with an ACM certificate |
| **`LabRole` is broad**: every task can do anything the lab account can | A compromised container could reach any resource in the account | Learner Lab doesn't allow creating narrower roles | A separate least-privilege role per service |
| **No request body size limit** on the API | A huge JSON body ties up an API task | Single-user, short-lived stacks | `http.MaxBytesReader` on job requests |
| **Containers run as root** | A GDAL exploit in a malicious file would run as root inside the container | The container holds no secrets beyond the task role, and tasks are replaced after each run | A non-root `USER` in each Dockerfile |
| **No ECR image scanning** | Known vulnerabilities in base images go unnoticed | Images are rebuilt from pinned or official bases for each sitting | `scan_on_push` on the ECR repositories |
| **GitHub Actions pinned to major tags** (`actions/checkout@v4`) | A compromised action release could run in CI | CI has no secrets and read-only access | Pin actions to commit SHAs |

## Dependencies

Go modules are pinned in `go.sum`; Python dependencies are pinned to exact versions in `tools/requirements.txt` and `locust/requirements.txt`. When updating any of them, read the release notes and run the full test suite.
