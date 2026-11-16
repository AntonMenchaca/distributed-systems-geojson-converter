<!-- One PR per issue. Title: `<type>(<scope>): <subject>` (Conventional Commits). Paste the handoff note on the issue too. -->

## Handoff note

```
Issue: <link>              Owner: <name>        Reviewer: <name>
Delivered: <PR links; paths; results/<phase>/<run>/>
Acceptance: <each criterion, checked, with an evidence link>
How to use it: <one command or one config change for the next person>
Known limits / not done: <...>
Unblocks: <issues + owners>
```

## Definition of done

- [ ] CI is green: `go build/vet/test` in every Go module touched, `python -m pytest tools/tests`, `terraform fmt -check` + `validate`, `bash -n` on scripts
- [ ] Ran locally against `docker compose` before touching AWS
- [ ] Contract changes (`api.yaml`, `schemas/*.json`, `knobs.md`) are in this PR, with their consumers
- [ ] AWS run steps: `results/<phase>/<run>/` is complete and `results/costs.csv` has the run's row
- [ ] Reviewer from the rotation in `.github/CODEOWNERS` is requested
