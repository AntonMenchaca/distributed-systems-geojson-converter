#!/bin/bash
# Run one experiment and write its results folder: the same command as tools/run.py
# (see its --help). Uses the repo's .venv when there is one.
#
#   scripts/run.sh local local-smoke traces/sample.csv local
#   scripts/run.sh p1 p1-base-anton-s11 traces/fail-s11.csv terraform/p1-base.tfvars
set -euo pipefail
cd "$(dirname "$0")/.."
py=python3
[[ -x .venv/bin/python ]] && py=.venv/bin/python
exec "$py" tools/run.py "$@"
