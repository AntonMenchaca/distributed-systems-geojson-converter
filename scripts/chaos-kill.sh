#!/bin/bash
# Stop one ECS worker task and append a kill row to events.csv.
# attempts[].task_id is the task ARN (worker/cmd/worker/taskid.go); pass that ARN so the
# kill joins to the attempt. Cluster is terraform output cluster_name (${prefix}-cluster).
#
#   scripts/chaos-kill.sh --cluster gc-samuel-cluster --task "$TASK_ARN" \
#       --events results/p1/p1-base-samuel-s11/events.csv
#
# --dry-run writes the events.csv row and prints the aws command without calling aws.
# events.csv columns: timestamp,event,task_id. event is "kill". timestamp is UTC.
set -euo pipefail

cluster=""
task=""
events=""
reason="chaos-kill"
dry=0

usage() {
  sed -n '2,12p' "$0" >&2
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --cluster) cluster=${2:-}; shift 2 ;;
    --task) task=${2:-}; shift 2 ;;
    --events) events=${2:-}; shift 2 ;;
    --reason) reason=${2:-}; shift 2 ;;
    --dry-run) dry=1; shift ;;
    -h|--help) usage; exit 0 ;;
    *) echo "unknown argument: $1" >&2; usage; exit 2 ;;
  esac
done

if [[ -z "$cluster" || -z "$task" || -z "$events" ]]; then
  echo "need --cluster, --task and --events" >&2
  usage
  exit 2
fi

if [[ "$dry" -eq 0 ]]; then
  aws ecs stop-task --cluster "$cluster" --task "$task" --reason "$reason" >/dev/null
else
  printf 'dry-run: aws ecs stop-task --cluster %q --task %q --reason %q\n' "$cluster" "$task" "$reason"
fi

ts=$(date -u +%Y-%m-%dT%H:%M:%S.%3NZ)
mkdir -p "$(dirname "$events")"
if [[ ! -s "$events" ]]; then
  printf 'timestamp,event,task_id\n' >"$events"
fi
printf '%s,kill,%s\n' "$ts" "$task" >>"$events"
echo "recorded kill of $task in $events"
