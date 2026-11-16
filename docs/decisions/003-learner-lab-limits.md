# 003: Learner Lab limits

Date: 2026-11-15 · Owner: Anton · Reviewer: Samuel · Status: **answered, except check 1 (the lab role cannot read the quota)**

These limits set the autoscaler's maximum task count, whether native ECS scaling (`SCALER=native`) is available as a comparison, and whether a Lambda-based converter is possible. The CLI output under each question is the evidence.

Checks ran in Anton's account (`148831131433`) as `assumed-role/voclabs/...`, against a `gc-anton` stack with 4 workers and 1 API task, which was destroyed afterwards.

## Before you start

```sh
export AWS_PROFILE=<your Learner Lab profile>
aws sts get-caller-identity   # Arn must contain assumed-role/voclabs/; stop if not
```

Checks 1, 2a and 4 are read-only. Checks 2b, 3 and 5 need a deployed stack (`terraform apply -var prefix=gc-<you>`) and change things; destroy it in the same sitting.

## 1. Fargate On-Demand vCPU quota (read-only)

```sh
aws service-quotas get-service-quota --service-code fargate --quota-code L-3032A538 \
  --query 'Quota.{name:QuotaName,value:Value}'
```

**Answer: unknown. The lab role cannot read it.**

```
An error occurred (AccessDeniedException) when calling the GetServiceQuota operation: User: arn:aws:sts::148831131433:assumed-role/voclabs/... is not authorized to perform: servicequotas:GetServiceQuota on resource: arn:aws:servicequotas:us-east-1:148831131433:fargate/L-3032A538 because no identity-based policy allows the servicequotas:GetServiceQuota action
```

Until the cap is known, the autoscaler maximum has no measured ceiling. 4 workers × 1 vCPU plus the 0.5 vCPU API (4.5 vCPU) ran without any capacity error.

## 2. Can Application Auto Scaling scale the worker service?

a. Does the service-linked role exist? (read-only)

```sh
aws iam get-role --role-name AWSServiceRoleForApplicationAutoScaling_ECSService --query Role.Arn
```

```
arn:aws:iam::148831131433:role/aws-service-role/ecs.application-autoscaling.amazonaws.com/AWSServiceRoleForApplicationAutoScaling_ECSService
```

b. Register the worker service as a scalable target, attach a target-tracking policy, then remove both (**writes**; needs the stack). Min and max are set to the running count (4), so registering cannot scale the service in:

```sh
R=service/gc-anton-cluster/gc-anton-worker
aws application-autoscaling register-scalable-target --service-namespace ecs \
  --resource-id $R --scalable-dimension ecs:service:DesiredCount --min-capacity 4 --max-capacity 4
aws application-autoscaling put-scaling-policy --service-namespace ecs --resource-id $R \
  --scalable-dimension ecs:service:DesiredCount --policy-name gc-anton-probe-cpu --policy-type TargetTrackingScaling \
  --target-tracking-scaling-policy-configuration '{"TargetValue":70.0,"PredefinedMetricSpecification":{"PredefinedMetricType":"ECSServiceAverageCPUUtilization"}}'
aws application-autoscaling delete-scaling-policy --service-namespace ecs --resource-id $R \
  --scalable-dimension ecs:service:DesiredCount --policy-name gc-anton-probe-cpu
aws application-autoscaling deregister-scalable-target --service-namespace ecs \
  --resource-id $R --scalable-dimension ecs:service:DesiredCount
```

```
ScalableTargetARN: arn:aws:application-autoscaling:us-east-1:148831131433:scalable-target/0ec5cd03ea1fe37145d8ac92add00b3b13d4
min 4, max 4, role .../AWSServiceRoleForApplicationAutoScaling_ECSService
policy alarms: TargetTracking-service/gc-anton-cluster/gc-anton-worker-AlarmHigh-..., ...-AlarmLow-...
after cleanup: 0 targets, 0 policies, 0 alarms; workers still 4 of 4
```

**Answer: yes.** Registering a target and creating a target-tracking policy (with its CloudWatch alarms) both work, so `SCALER=native` stays available as a comparison.

## 3. Does task scale-in protection work with `LabRole`? (needs the stack)

From the CLI (the session's permissions):

```sh
TASK=$(aws ecs list-tasks --cluster gc-anton-cluster --service-name gc-anton-worker --query 'taskArns[0]' --output text)
aws ecs update-task-protection --cluster gc-anton-cluster --tasks "$TASK" --protection-enabled --expires-in-minutes 5
aws ecs get-task-protection --cluster gc-anton-cluster --tasks "$TASK"
aws ecs update-task-protection --cluster gc-anton-cluster --tasks "$TASK" --no-protection-enabled
```

```
task 14e24591df164827aa4277dfa60b3fff: protectionEnabled true, expires 2026-11-15T20:24:35-05:00, failures []
get-task-protection: protectionEnabled true
after disable: protectionEnabled false
```

From inside the task (what `SHUTDOWN_MODE=drain` will do with `LabRole`):

```sh
aws iam simulate-principal-policy --policy-source-arn "$(aws iam get-role --role-name LabRole --query Role.Arn --output text)" \
  --action-names ecs:UpdateTaskProtection ecs:GetTaskProtection --query 'EvaluationResults[].{a:EvalActionName,d:EvalDecision}'
```

```
ecs:UpdateTaskProtection  allowed
ecs:GetTaskProtection     allowed
```

**Answer: yes, with one caveat.** Protection works from the CLI, and `LabRole`'s policies allow both calls. The simulator may not apply the lab's organization policy (the one that denies `s3:GetBucketObjectLockConfiguration`), so the drain code should confirm the call succeeds from a running task the first time it runs on AWS.

## 4. Lambda concurrency limit (read-only)

```sh
aws lambda get-account-settings --query 'AccountLimit.{concurrent:ConcurrentExecutions,unreserved:UnreservedConcurrentExecutions}'
```

```
concurrent 400, unreserved 400
```

**Answer: 400.** Well above 10, so a Lambda-based converter is possible as far as concurrency goes.

## 5. Do ECS tasks keep running after the session ends? (needs the stack)

1. Note every task's start time.
2. End the lab session, wait at least 15 minutes, start a new session.
3. Run the same command. The same task ARNs still `RUNNING` with the same `startedAt` means tasks survive.

```sh
aws ecs describe-tasks --cluster gc-anton-cluster --tasks <task ARNs> --query 'tasks[].[taskArn,lastStatus,startedAt]' --output text
```

```
before, 2026-11-16T01:20:08Z                            after restart, 2026-11-16T01:46:37Z
worker 14e24591... RUNNING 2026-11-15T19:58:26-05:00    RUNNING 2026-11-15T19:58:26-05:00
worker 59f523f4... RUNNING 2026-11-15T19:58:29-05:00    RUNNING 2026-11-15T19:58:29-05:00
worker 9104601b... RUNNING 2026-11-15T19:58:22-05:00    RUNNING 2026-11-15T19:58:22-05:00
worker e913a18b... RUNNING 2026-11-15T19:58:29-05:00    RUNNING 2026-11-15T19:58:29-05:00
api    f5e40605... RUNNING 2026-11-15T19:59:50-05:00    RUNNING 2026-11-15T19:59:50-05:00
no stopped tasks; GET /health still {"status":"ok"}
```

**Answer: yes.** All five tasks survived about 26 minutes with the session ended. A stack keeps serving (and billing) after the session's credentials expire; only tools on a laptop need fresh credentials.

## Decision

- **`SCALER=native` stays** as a comparison for the autoscaler runs (check 2).
- **`SHUTDOWN_MODE=drain` uses scale-in protection** (check 3), confirmed from a running task on its first AWS run.
- **A Lambda-based converter is not ruled out by concurrency** (check 4).
- **A run may outlast a session** (check 5), but a stack left up keeps billing after the session ends, so `terraform destroy` still happens in the same sitting.
- **The vCPU cap is unknown** (check 1). 4.5 vCPU is known to work. Until a higher count is shown to run, plan task-shape and autoscaler runs within that, and find the cap by raising `worker_count` in a short test before relying on more.
