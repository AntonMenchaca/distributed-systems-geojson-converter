# 003: Learner Lab limits

Date: _(fill in)_ · Owner: Anton · Reviewer: Samuel · Status: **pending: needs a live Learner Lab session**

These limits set the autoscaler's maximum task count, whether native ECS scaling (`SCALER=native`) is available as a comparison, and whether a Lambda-based converter is possible. Paste the CLI output under each question as evidence.

## Before you start

```sh
export AWS_PROFILE=<your Learner Lab profile>
aws sts get-caller-identity   # Arn must contain assumed-role/voclabs/; stop if not
```

Checks 1, 2a and 4 are read-only. Checks 2b, 3 and 5 need a deployed stack (`terraform apply -var prefix=gc-anton -var worker_count=1`) and change things; destroy it in the same sitting.

## 1. Fargate On-Demand vCPU quota (read-only)

```sh
aws service-quotas get-service-quota --service-code fargate --quota-code L-3032A538 \
  --query 'Quota.{name:QuotaName,value:Value}'
```

**Answer:** _vCPUs_ → autoscaler maximum = floor((quota − API 0.5 vCPU) ÷ task vCPU).

## 2. Can Application Auto Scaling create its service-linked role?

a. Does it already exist? (read-only)

```sh
aws iam get-role --role-name AWSServiceRoleForApplicationAutoScaling_ECSService --query Role.Arn
```

b. If not, register the worker service as a scalable target, which creates the role (**writes**; needs the stack):

```sh
aws application-autoscaling register-scalable-target --service-namespace ecs \
  --resource-id service/gc-anton-cluster/gc-anton-worker --scalable-dimension ecs:service:DesiredCount \
  --min-capacity 1 --max-capacity 2
aws application-autoscaling deregister-scalable-target --service-namespace ecs \
  --resource-id service/gc-anton-cluster/gc-anton-worker --scalable-dimension ecs:service:DesiredCount
```

**Answer:** _yes / no (error text)_ → if no, `SCALER=native` is dropped.

## 3. Does task scale-in protection work with `LabRole`? (needs the stack)

From the CLI (your session's permissions):

```sh
TASK=$(aws ecs list-tasks --cluster gc-anton-cluster --service-name gc-anton-worker --query 'taskArns[0]' --output text)
aws ecs update-task-protection --cluster gc-anton-cluster --tasks "$TASK" --protection-enabled --expires-in-minutes 5
aws ecs get-task-protection --cluster gc-anton-cluster --tasks "$TASK"
```

From inside the task (what `SHUTDOWN_MODE=drain` will do with `LabRole`):

```sh
aws iam simulate-principal-policy --policy-source-arn "$(aws iam get-role --role-name LabRole --query Role.Arn --output text)" \
  --action-names ecs:UpdateTaskProtection ecs:GetTaskProtection --query 'EvaluationResults[].{a:EvalActionName,d:EvalDecision}'
```

**Answer:** _yes / no_ → if no, `SHUTDOWN_MODE=drain` runs without scale-in protection and the autoscaler results note it.

## 4. Lambda concurrency limit (read-only)

```sh
aws lambda get-account-settings --query 'AccountLimit.{concurrent:ConcurrentExecutions,unreserved:UnreservedConcurrentExecutions}'
```

**Answer:** _n_ → a Lambda-based converter is possible only if this is well above 10.

## 5. Do ECS tasks keep running after the 4-hour session ends? (needs the stack)

1. With `worker_count=1` deployed, note the task's start time:
   `aws ecs describe-tasks --cluster gc-anton-cluster --tasks "$TASK" --query 'tasks[0].{started:startedAt,status:lastStatus}'`
2. End the lab session (or let it expire), wait at least 15 minutes, start a new session.
3. Run the same command. The same task ARN still `RUNNING` with the same `startedAt` means tasks survive.

**Answer:** _yes / no_ → if no, `run.sh` must finish every run inside one session.

## Decision

_(the vCPU cap, and anything that changes the task-shape or autoscaler runs; post it to everyone)_
