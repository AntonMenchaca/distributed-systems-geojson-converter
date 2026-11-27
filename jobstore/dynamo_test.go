package jobstore

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
)

// newTestStore creates a fresh table in DynamoDB Local (DYNAMODB_ENDPOINT) so tests never share state.
func newTestStore(t *testing.T, mode CommitMode) Store {
	t.Helper()
	endpoint := os.Getenv("DYNAMODB_ENDPOINT")
	if endpoint == "" {
		t.Skip("DYNAMODB_ENDPOINT not set; start DynamoDB Local (docker compose up dynamodb) to run jobstore tests")
	}
	ctx := context.Background()
	cfg, err := config.LoadDefaultConfig(ctx,
		config.WithRegion("us-east-1"),
		config.WithCredentialsProvider(credentials.NewStaticCredentialsProvider("local", "local", "")),
	)
	if err != nil {
		t.Fatal(err)
	}
	client := dynamodb.NewFromConfig(cfg, func(o *dynamodb.Options) { o.BaseEndpoint = aws.String(endpoint) })
	table := fmt.Sprintf("jobs-test-%d", time.Now().UnixNano())
	if _, err := client.CreateTable(ctx, CreateTableInput(table)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = client.DeleteTable(context.Background(), &dynamodb.DeleteTableInput{TableName: aws.String(table)})
	})
	return NewDynamoStore(client, table, mode)
}

var t0 = time.Date(2026, 11, 14, 18, 30, 0, 0, time.UTC)

func sampleJob(id string) Job {
	return Job{
		JobID:        id,
		RunID:        "local-x",
		OutputFormat: "gpkg",
		SizeClass:    "small",
		InputKey:     "datasets/dc.geojson",
		InputBytes:   23611576,
		SubmittedAt:  t0,
	}
}

func TestCreateRejectsDuplicateJobID(t *testing.T) {
	s, ctx := newTestStore(t, CommitBlind), context.Background()
	if err := s.Create(ctx, sampleJob("j1")); err != nil {
		t.Fatalf("first create: %v", err)
	}
	if err := s.Create(ctx, sampleJob("j1")); err == nil {
		t.Fatal("second create with the same job_id succeeded; want an error")
	}
}

func TestClaimIncrementsEpoch(t *testing.T) {
	s, ctx := newTestStore(t, CommitBlind), context.Background()
	if err := s.Create(ctx, sampleJob("j1")); err != nil {
		t.Fatal(err)
	}
	for want := int64(1); want <= 2; want++ {
		got, err := s.Claim(ctx, "j1", fmt.Sprintf("task-%d", want), t0.Add(time.Duration(want)*time.Second))
		if err != nil {
			t.Fatalf("claim %d: %v", want, err)
		}
		if got != want {
			t.Fatalf("claim returned epoch %d, want %d", got, want)
		}
	}
	j, err := s.Get(ctx, "j1")
	if err != nil {
		t.Fatal(err)
	}
	if len(j.Attempts) != 2 || j.Attempts[1].Epoch != 2 || j.Attempts[1].TaskID != "task-2" {
		t.Fatalf("attempts = %+v, want two with the second at epoch 2 by task-2", j.Attempts)
	}
}

func TestClaimAfterSucceededReturnsErrAlreadyDone(t *testing.T) {
	s, ctx := newTestStore(t, CommitBlind), context.Background()
	if err := s.Create(ctx, sampleJob("j1")); err != nil {
		t.Fatal(err)
	}
	epoch, err := s.Claim(ctx, "j1", "task-1", t0)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Commit(ctx, "j1", epoch, Result{OutputKey: "outputs/j1/out.gpkg", CommittedAt: t0.Add(5 * time.Second)}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Claim(ctx, "j1", "task-2", t0.Add(6*time.Second)); !errors.Is(err, ErrAlreadyDone) {
		t.Fatalf("claim after SUCCEEDED returned %v, want ErrAlreadyDone", err)
	}
}

// Deliberately naive: blind mode lets a stale attempt overwrite the newer attempt's result.
func TestBlindCommitIgnoresEpoch(t *testing.T) {
	s, ctx := newTestStore(t, CommitBlind), context.Background()
	if err := s.Create(ctx, sampleJob("j1")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Claim(ctx, "j1", "task-1", t0); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Claim(ctx, "j1", "task-2", t0.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := s.Commit(ctx, "j1", 1, Result{OutputKey: "outputs/j1/stale.gpkg", CommittedAt: t0.Add(9 * time.Second)}); err != nil {
		t.Fatalf("blind commit with stale epoch 1: %v, want success", err)
	}
	j, err := s.Get(ctx, "j1")
	if err != nil {
		t.Fatal(err)
	}
	if j.Status != StatusSucceeded || j.OutputKey != "outputs/j1/stale.gpkg" {
		t.Fatalf("got status %s output %q, want SUCCEEDED with the stale attempt's output", j.Status, j.OutputKey)
	}
}

func TestStatusTimestampsSet(t *testing.T) {
	s, ctx := newTestStore(t, CommitBlind), context.Background()
	if err := s.Create(ctx, sampleJob("j1")); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkQueued(ctx, "j1", "jobs", t0.Add(50*time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	epoch, err := s.Claim(ctx, "j1", "task-1", t0.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Commit(ctx, "j1", epoch, Result{OutputKey: "outputs/j1/out.gpkg", FeatureCount: 1000, CommittedAt: t0.Add(6 * time.Second)}); err != nil {
		t.Fatal(err)
	}
	j, err := s.Get(ctx, "j1")
	if err != nil {
		t.Fatal(err)
	}
	for name, ts := range map[string]time.Time{
		"submitted_at": j.SubmittedAt, "enqueued_at": j.EnqueuedAt, "started_at": j.StartedAt, "committed_at": j.CommittedAt,
	} {
		if ts.IsZero() {
			t.Errorf("%s is zero after the full lifecycle", name)
		}
	}
	if !j.EnqueuedAt.Equal(t0.Add(50 * time.Millisecond)) {
		t.Errorf("enqueued_at = %v, want %v (millisecond precision kept)", j.EnqueuedAt, t0.Add(50*time.Millisecond))
	}
}

// TestHeartbeatUpdatesHeartbeatAt: a successful Heartbeat stamps heartbeat_at for the
// current epoch (LEASE_MODE=heartbeat renewals).
func TestHeartbeatUpdatesHeartbeatAt(t *testing.T) {
	s, ctx := newTestStore(t, CommitBlind), context.Background()
	if err := s.Create(ctx, sampleJob("j1")); err != nil {
		t.Fatal(err)
	}
	epoch, err := s.Claim(ctx, "j1", "task-1", t0)
	if err != nil {
		t.Fatal(err)
	}
	at := t0.Add(20 * time.Second)
	if err := s.Heartbeat(ctx, "j1", epoch, at); err != nil {
		t.Fatal(err)
	}
	j, err := s.Get(ctx, "j1")
	if err != nil {
		t.Fatal(err)
	}
	if !j.HeartbeatAt.Equal(at) {
		t.Fatalf("heartbeat_at = %v, want %v", j.HeartbeatAt, at)
	}
	if err := s.Heartbeat(ctx, "j1", epoch+1, at.Add(time.Second)); !errors.Is(err, ErrStaleEpoch) {
		t.Fatalf("Heartbeat with stale epoch: %v, want ErrStaleEpoch", err)
	}
}
