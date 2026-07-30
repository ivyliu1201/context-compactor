package journal

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/ivyliu1201/context-compactor/internal/privacy"
	"github.com/ivyliu1201/context-compactor/internal/protocol"
)

const testPromptPolicyVersion = "context-compactor/memory-extractor/v1"

func TestAppendQueuesBoundedRedactedMemoryJobIdempotently(t *testing.T) {
	store, root := openTestStore(t)
	request := validAppendRequest(root, "event-memory-job-1", "")
	request.Event.Content = strings.Repeat("界", MaxMemoryPromptRunes+25) +
		"\nAuthorization: Bearer example-credential"
	request.MemoryJob = &MemoryJobRequest{
		PromptPolicyVersion: testPromptPolicyVersion,
		EnqueuedAt:          journalTestTime,
	}

	result, err := store.Append(context.Background(), request)
	if err != nil {
		t.Fatalf("Append() error = %v", err)
	}
	if !result.EventInserted || !result.MemoryJobInserted {
		t.Fatalf("Append() result = %+v, want event and memory job", result)
	}

	job, found, err := store.LoadMemoryExtractionJob(
		context.Background(),
		request.Event.ID,
	)
	if err != nil || !found {
		t.Fatalf("LoadMemoryExtractionJob() found = %t, error = %v", found, err)
	}
	if utf8.RuneCountInString(job.Prompt) != MaxMemoryPromptRunes {
		t.Fatalf(
			"stored prompt runes = %d, want %d",
			utf8.RuneCountInString(job.Prompt),
			MaxMemoryPromptRunes,
		)
	}
	if privacy.ContainsPotentialSecret(job.Prompt) ||
		strings.Contains(job.Prompt, "example-credential") {
		t.Fatalf("stored prompt contains secret material")
	}
	if job.RedactionCount != 1 {
		t.Fatalf("job redaction count = %d, want 1", job.RedactionCount)
	}
	if job.Status != MemoryJobPending || job.AttemptCount != 0 || !job.Retryable {
		t.Fatalf("new memory job state = %+v", job)
	}
	if !job.ExpiresAt.Equal(journalTestTime.Add(DefaultMemoryPromptRetention)) {
		t.Fatalf("job expires_at = %s", job.ExpiresAt)
	}

	retry, err := store.Append(context.Background(), request)
	if err != nil {
		t.Fatalf("Append() retry error = %v", err)
	}
	if retry.EventInserted || retry.MemoryJobInserted {
		t.Fatalf("Append() retry result = %+v, want idempotent no-op", retry)
	}
	assertTableCount(t, store, "memory_extraction_jobs", 1)

	if _, err := store.db.Exec("PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
		t.Fatalf("checkpoint WAL before file inspection: %v", err)
	}
	databaseBytes, err := os.ReadFile(store.Path())
	if err != nil {
		t.Fatalf("read journal database: %v", err)
	}
	if strings.Contains(string(databaseBytes), "example-credential") {
		t.Fatal("journal database contains unredacted credential")
	}
}

func TestAppendMemoryJobRequiresUserPrompt(t *testing.T) {
	store, root := openTestStore(t)
	request := validAppendRequest(root, "event-memory-job-kind", "")
	request.Event.Kind = protocol.EventSessionStart
	request.MemoryJob = &MemoryJobRequest{
		PromptPolicyVersion: testPromptPolicyVersion,
		EnqueuedAt:          journalTestTime,
	}

	_, err := store.Append(context.Background(), request)
	if err == nil || !strings.Contains(err.Error(), "requires a user_prompt event") {
		t.Fatalf("Append() error = %v, want user_prompt rejection", err)
	}
	assertTableCount(t, store, "events", 0)
	assertTableCount(t, store, "memory_extraction_jobs", 0)
}

func TestApplyMemoryExtractionRejectsMismatchedCreatedAt(t *testing.T) {
	store, root := openTestStore(t)
	ctx := context.Background()
	request := validAppendRequest(root, "event-memory-created-at", "")
	request.Event.Content = "Use UTC timestamps."
	request.MemoryJob = &MemoryJobRequest{
		PromptPolicyVersion: testPromptPolicyVersion,
		EnqueuedAt:          journalTestTime,
	}
	if _, err := store.Append(ctx, request); err != nil {
		t.Fatalf("Append() error = %v", err)
	}
	job, found, err := store.ClaimNextMemoryExtraction(
		ctx,
		journalTestTime.Add(time.Second),
		time.Minute,
	)
	if err != nil || !found {
		t.Fatalf(
			"ClaimNextMemoryExtraction() found = %t, error = %v",
			found,
			err,
		)
	}
	update := protocol.MemoryUpdate{
		Protocol:      protocol.Version,
		PrivacyMode:   protocol.PrivacyStandard,
		SourceEventID: request.Event.ID,
		CreatedAt:     journalTestTime.Add(-time.Second),
		Operations: []protocol.Operation{{
			ID:   "operation-created-at-1",
			Kind: protocol.OperationAdd,
			Record: &protocol.MemoryRecord{
				ID:         "record-created-at-1",
				Kind:       protocol.MemoryConstraint,
				Value:      "Use UTC timestamps.",
				Priority:   protocol.PriorityHigh,
				Confidence: protocol.ConfidenceExplicit,
				Status:     protocol.StatusActive,
				Source: protocol.SourceReference{
					EventID: request.Event.ID,
				},
				CreatedAt: journalTestTime.Add(-time.Second),
			},
		}},
	}
	_, err = store.ApplyMemoryExtraction(ctx, ApplyMemoryJobRequest{
		JobID: job.ID,
		Result: protocol.ExtractionResult{
			Protocol:     protocol.Version,
			Outcome:      protocol.OutcomeMemoryUpdate,
			MemoryUpdate: &update,
		},
		Model:       "test-model",
		CompletedAt: journalTestTime.Add(2 * time.Second),
	})
	if err == nil || !strings.Contains(err.Error(), "created_at") {
		t.Fatalf("ApplyMemoryExtraction() error = %v, want created_at rejection", err)
	}
}

func TestPruneMemoryExtractionJobsDeletesExpiredAndOldestTerminalJobs(t *testing.T) {
	store, root := openTestStore(t)
	ctx := context.Background()
	for index := 1; index <= 4; index++ {
		eventID := "event-memory-prune-" + string(rune('0'+index))
		request := validAppendRequest(root, eventID, "")
		request.Event.OccurredAt = journalTestTime.Add(time.Duration(index) * time.Minute)
		request.Event.Content = "project memory " + eventID
		request.MemoryJob = &MemoryJobRequest{
			PromptPolicyVersion: testPromptPolicyVersion,
			EnqueuedAt:          request.Event.OccurredAt,
		}
		if _, err := store.Append(ctx, request); err != nil {
			t.Fatalf("Append(%s) error = %v", eventID, err)
		}
		if index <= 3 {
			if _, err := store.db.ExecContext(ctx, `
UPDATE memory_extraction_jobs
SET status = 'completed', completed_at = ?, result_outcome = 'no_change'
WHERE source_event_id = ?`,
				formatTime(request.Event.OccurredAt.Add(time.Second)),
				eventID,
			); err != nil {
				t.Fatalf("complete memory job %s: %v", eventID, err)
			}
		}
	}

	result, err := store.PruneMemoryExtractionJobs(
		ctx,
		journalTestTime.Add(time.Hour),
		2,
	)
	if err != nil {
		t.Fatalf("PruneMemoryExtractionJobs() error = %v", err)
	}
	if result.ExpiredDeleted != 0 || result.OverflowDeleted != 2 {
		t.Fatalf("PruneMemoryExtractionJobs() = %+v, want two overflow", result)
	}
	assertTableCount(t, store, "memory_extraction_jobs", 2)

	result, err = store.PruneMemoryExtractionJobs(
		ctx,
		journalTestTime.Add(DefaultMemoryPromptRetention+time.Hour),
		DefaultMaxMemoryExtractionJobs,
	)
	if err != nil {
		t.Fatalf("expired PruneMemoryExtractionJobs() error = %v", err)
	}
	if result.ExpiredDeleted != 2 {
		t.Fatalf("expired prune = %+v, want two expired", result)
	}
	assertTableCount(t, store, "memory_extraction_jobs", 0)
	assertTableCount(t, store, "events", 4)
}

func TestJournalRetentionKeepsEventReferencedByMemoryJob(t *testing.T) {
	store, root := openTestStore(t)
	ctx := context.Background()
	request := validAppendRequest(root, "event-memory-retention", "")
	request.MemoryJob = &MemoryJobRequest{
		PromptPolicyVersion: testPromptPolicyVersion,
		EnqueuedAt:          journalTestTime,
	}
	result, err := store.Append(ctx, request)
	if err != nil {
		t.Fatalf("Append() error = %v", err)
	}
	if err := store.UpdateCursor(ctx, "test-consumer", result.EventSeq); err != nil {
		t.Fatalf("UpdateCursor() error = %v", err)
	}

	if _, err := store.Prune(ctx, RetentionPolicy{}); err != nil {
		t.Fatalf("Prune() error = %v", err)
	}
	assertTableCount(t, store, "events", 1)
	assertTableCount(t, store, "memory_extraction_jobs", 1)
}
