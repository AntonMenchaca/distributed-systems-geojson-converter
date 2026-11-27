package jobstore

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

// tsLayout keeps millisecond precision in a fixed width, so timestamps sort as strings.
const tsLayout = "2006-01-02T15:04:05.000Z"

type dynamoStore struct {
	client *dynamodb.Client
	table  string
	mode   CommitMode
	now    func() time.Time
}

// NewDynamoStore returns a Store backed by the given DynamoDB table.
func NewDynamoStore(client *dynamodb.Client, table string, mode CommitMode) Store {
	return &dynamoStore{client: client, table: table, mode: mode, now: time.Now}
}

func s(v string) types.AttributeValue { return &types.AttributeValueMemberS{Value: v} }
func n(v int64) types.AttributeValue {
	return &types.AttributeValueMemberN{Value: strconv.FormatInt(v, 10)}
}
func f(v float64) types.AttributeValue {
	return &types.AttributeValueMemberN{Value: strconv.FormatFloat(v, 'f', -1, 64)}
}
func ts(t time.Time) types.AttributeValue { return s(t.UTC().Format(tsLayout)) }

func isCondFailed(err error) bool {
	var ccf *types.ConditionalCheckFailedException
	return errors.As(err, &ccf)
}

// update collects a SET expression; names and values are added alongside each assignment.
type update struct {
	sets   []string
	names  map[string]string
	values map[string]types.AttributeValue
}

func newUpdate() *update {
	return &update{names: map[string]string{}, values: map[string]types.AttributeValue{}}
}

// set assigns path = value. path may contain #name placeholders registered with name().
func (u *update) set(path string, v types.AttributeValue) *update {
	ph := fmt.Sprintf(":v%d", len(u.values))
	u.sets = append(u.sets, path+" = "+ph)
	u.values[ph] = v
	return u
}

func (u *update) expr(raw string) *update { u.sets = append(u.sets, raw); return u }

func (u *update) name(ph, attr string) *update { u.names[ph] = attr; return u }

// val registers a named value placeholder for use in expr() or a condition.
func (u *update) val(ph string, v types.AttributeValue) *update { u.values[ph] = v; return u }

func (d *dynamoStore) key(jobID string) map[string]types.AttributeValue {
	return map[string]types.AttributeValue{"job_id": s(jobID)}
}

func (d *dynamoStore) run(ctx context.Context, jobID string, u *update, cond string, rv types.ReturnValue) (*dynamodb.UpdateItemOutput, error) {
	in := &dynamodb.UpdateItemInput{
		TableName:                 aws.String(d.table),
		Key:                       d.key(jobID),
		UpdateExpression:          aws.String("SET " + strings.Join(u.sets, ", ")),
		ExpressionAttributeValues: u.values,
		ReturnValues:              rv,
	}
	if len(u.names) > 0 {
		in.ExpressionAttributeNames = u.names
	}
	if cond != "" {
		in.ConditionExpression = aws.String(cond)
	}
	return d.client.UpdateItem(ctx, in)
}

func (d *dynamoStore) newItem(j Job, status, code string) map[string]types.AttributeValue {
	item := map[string]types.AttributeValue{
		"job_id":        s(j.JobID),
		"run_id":        s(j.RunID),
		"output_format": s(j.OutputFormat),
		"size_class":    s(j.SizeClass),
		"input_key":     s(j.InputKey),
		"input_bytes":   n(j.InputBytes),
		"status":        s(status),
		"epoch":         n(0),
		"submitted_at":  ts(j.SubmittedAt),
	}
	if code != "" {
		item["error_code"] = s(code)
	}
	return item
}

func (d *dynamoStore) put(ctx context.Context, item map[string]types.AttributeValue) error {
	_, err := d.client.PutItem(ctx, &dynamodb.PutItemInput{
		TableName:           aws.String(d.table),
		Item:                item,
		ConditionExpression: aws.String("attribute_not_exists(job_id)"),
	})
	if isCondFailed(err) {
		return ErrJobExists
	}
	return err
}

func (d *dynamoStore) Create(ctx context.Context, j Job) error {
	return d.put(ctx, d.newItem(j, StatusSubmitted, ""))
}

func (d *dynamoStore) Reject(ctx context.Context, j Job, code string) error {
	return d.put(ctx, d.newItem(j, StatusRejected, code))
}

func (d *dynamoStore) MarkQueued(ctx context.Context, jobID, queue string, at time.Time) error {
	u := newUpdate().name("#s", "status").name("#q", "queue").
		set("#s", s(StatusQueued)).set("#q", s(queue)).set("enqueued_at", ts(at))
	_, err := d.run(ctx, jobID, u, "attribute_exists(job_id)", types.ReturnValueNone)
	if isCondFailed(err) {
		return ErrNotFound
	}
	return err
}

// Claim starts a new attempt. The terminal fences (SUCCEEDED and FAILED) are in the
// ConditionExpression, so a concurrent commit or permanent failure cannot lose to a late
// claim (steps 3.1-B and 3.3-B). The epoch increment and the attempts append share one
// UpdateItem, so attempts[epoch-1] is always this attempt.
func (d *dynamoStore) Claim(ctx context.Context, jobID, taskID string, at time.Time) (int64, error) {
	att := &types.AttributeValueMemberL{Value: []types.AttributeValue{
		&types.AttributeValueMemberM{Value: map[string]types.AttributeValue{"task_id": s(taskID), "started_at": ts(at)}},
	}}
	u := newUpdate().name("#s", "status").
		set("#s", s(StatusRunning)).set("started_at", ts(at)).
		val(":one", n(1)).val(":t", ts(at)).val(":att", att).
		val(":empty", &types.AttributeValueMemberL{Value: []types.AttributeValue{}}).
		val(":done", s(StatusSucceeded)).val(":failed", s(StatusFailed)).
		expr("epoch = epoch + :one").
		expr("first_started_at = if_not_exists(first_started_at, :t)").
		expr("attempts = list_append(if_not_exists(attempts, :empty), :att)")
	out, err := d.run(ctx, jobID, u, "attribute_exists(job_id) AND #s <> :done AND #s <> :failed", types.ReturnValueUpdatedNew)
	if isCondFailed(err) {
		return 0, d.alreadyDoneOrMissing(ctx, jobID)
	}
	if err != nil {
		return 0, err
	}
	var epoch int64
	if err := attributevalue.Unmarshal(out.Attributes["epoch"], &epoch); err != nil {
		return 0, fmt.Errorf("jobstore: claim: decode epoch: %w", err)
	}
	stamp := newUpdate().set(fmt.Sprintf("attempts[%d].epoch", epoch-1), n(epoch))
	if _, err := d.run(ctx, jobID, stamp, "", types.ReturnValueNone); err != nil {
		return 0, fmt.Errorf("jobstore: claim: stamp attempt epoch: %w", err)
	}
	return epoch, nil
}

func (d *dynamoStore) alreadyDoneOrMissing(ctx context.Context, jobID string) error {
	cur, err := d.Get(ctx, jobID)
	if err != nil {
		return err
	}
	switch cur.Status {
	case StatusSucceeded:
		return ErrAlreadyDone
	case StatusFailed:
		return ErrAlreadyFailed
	}
	// The condition failed but the job is neither SUCCEEDED nor FAILED (a blind-mode Fail or
	// Commit moved the status between the two calls). Not a terminal error: the worker
	// deletes the message on those, which would drop a job that is still live. The message
	// stays for redelivery instead.
	return fmt.Errorf("jobstore: claim %s: condition failed while status is %s", jobID, cur.Status)
}

func (d *dynamoStore) Heartbeat(ctx context.Context, jobID string, epoch int64, at time.Time) error {
	u := newUpdate().set("heartbeat_at", ts(at)).val(":mine", n(epoch))
	_, err := d.run(ctx, jobID, u, "epoch = :mine", types.ReturnValueNone)
	if isCondFailed(err) {
		return d.staleOrMissing(ctx, jobID)
	}
	return err
}

func (d *dynamoStore) staleOrMissing(ctx context.Context, jobID string) error {
	if _, err := d.Get(ctx, jobID); err != nil {
		return err
	}
	return ErrStaleEpoch
}

func attemptPath(epoch int64, field string) string {
	return fmt.Sprintf("attempts[%d].%s", epoch-1, field)
}

// Commit in blind mode overwrites the job's result whatever the epoch: the deliberately
// naive baseline. Fenced mode adds "epoch = :mine": only the newest claim can commit, and
// any other attempt gets ErrStaleEpoch and must discard its output (step 3.2-B).
func (d *dynamoStore) Commit(ctx context.Context, jobID string, epoch int64, r Result) error {
	if epoch < 1 {
		return fmt.Errorf("jobstore: commit: epoch %d is not a claimed attempt", epoch)
	}
	u := newUpdate().name("#s", "status").
		set("#s", s(StatusSucceeded)).
		set("output_key", s(r.OutputKey)).
		set("output_bytes", n(r.OutputBytes)).
		set("feature_count", n(r.FeatureCount)).
		set("committed_at", ts(r.CommittedAt)).
		set("download_ms", n(r.DownloadMS)).
		set("convert_ms", n(r.ConvertMS)).
		set("package_ms", n(r.PackageMS)).
		set("upload_ms", n(r.UploadMS)).
		set(attemptPath(epoch, "ended_at"), ts(r.CommittedAt)).
		set(attemptPath(epoch, "outcome"), s(OutcomeSucceeded)).
		set(attemptPath(epoch, "peak_rss_mb"), f(r.PeakRSSMB)).
		set(attemptPath(epoch, "cpu_s"), f(r.CPUS)).
		set(attemptPath(epoch, "peak_disk_mb"), f(r.PeakDiskMB))
	if r.OutputSHA256 != "" {
		u.set("output_sha256", s(r.OutputSHA256))
	}
	cond := "attribute_exists(job_id)"
	if d.mode == CommitFenced {
		u.val(":mine", n(epoch))
		cond += " AND epoch = :mine"
	}
	_, err := d.run(ctx, jobID, u, cond, types.ReturnValueNone)
	if isCondFailed(err) {
		if d.mode == CommitFenced {
			return d.staleOrMissing(ctx, jobID)
		}
		return ErrNotFound
	}
	return err
}

// Fail records a failed attempt. A permanent failure (bad input) ends the job as FAILED;
// a transient one leaves the status for the next delivery to change. Fenced mode also
// requires epoch = :mine and a status that is not SUCCEEDED, so a stale attempt cannot
// overwrite a newer attempt's result and a finished job never goes backwards.
func (d *dynamoStore) Fail(ctx context.Context, jobID string, epoch int64, code string, permanent bool) error {
	if epoch < 1 {
		return fmt.Errorf("jobstore: fail: epoch %d is not a claimed attempt", epoch)
	}
	outcome := OutcomeError
	u := newUpdate().set("error_code", s(code))
	if permanent {
		outcome = OutcomeFailed
		u.name("#s", "status").set("#s", s(StatusFailed))
	}
	u.set(attemptPath(epoch, "ended_at"), ts(d.now())).
		set(attemptPath(epoch, "outcome"), s(outcome)).
		set(attemptPath(epoch, "error_code"), s(code))
	cond := "attribute_exists(job_id)"
	if d.mode == CommitFenced {
		u.name("#s", "status").val(":mine", n(epoch)).val(":done", s(StatusSucceeded))
		cond += " AND epoch = :mine AND #s <> :done"
	}
	_, err := d.run(ctx, jobID, u, cond, types.ReturnValueNone)
	if isCondFailed(err) {
		if d.mode == CommitFenced {
			return d.staleOrMissing(ctx, jobID)
		}
		return ErrNotFound
	}
	return err
}

func (d *dynamoStore) Get(ctx context.Context, jobID string) (Job, error) {
	out, err := d.client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName:      aws.String(d.table),
		Key:            d.key(jobID),
		ConsistentRead: aws.Bool(true),
	})
	if err != nil {
		return Job{}, err
	}
	if len(out.Item) == 0 {
		return Job{}, ErrNotFound
	}
	var j Job
	if err := attributevalue.UnmarshalMap(out.Item, &j); err != nil {
		return Job{}, fmt.Errorf("jobstore: decode %s: %w", jobID, err)
	}
	return j, nil
}
