package runtime

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ivyliu1201/context-compactor/internal/compiler"
	"github.com/ivyliu1201/context-compactor/internal/journal"
	"github.com/ivyliu1201/context-compactor/internal/protocol"
)

func TestBackgroundMemoryWorkerCompletesNoChangeWithoutRefresh(t *testing.T) {
	store, root := openMemoryWorkerStore(t)
	event := appendMemoryWorkerJob(t, store, root, "event-worker-no-change")
	extractorCalls := 0
	worker := testBackgroundMemoryWorker(
		store,
		root,
		MemoryDecisionFunc(func(
			_ context.Context,
			request MemoryExtractionRequest,
		) (MemoryExtractionResult, error) {
			extractorCalls++
			if request.Job.SourceEventID != event.ID {
				t.Fatalf("extractor job event = %q", request.Job.SourceEventID)
			}
			return MemoryExtractionResult{
				Result: protocol.ExtractionResult{
					Protocol: protocol.Version,
					Outcome:  protocol.OutcomeNoChange,
				},
				Model:        "test-routine-model",
				AttemptCount: 1,
			}, nil
		}),
	)

	result, err := worker.ProcessNext(context.Background())
	if err != nil {
		t.Fatalf("ProcessNext() error = %v", err)
	}
	if !result.Found ||
		result.Outcome != protocol.OutcomeNoChange ||
		result.MemoryChanged ||
		result.RefreshQueued {
		t.Fatalf("ProcessNext() = %+v, want no change", result)
	}
	if extractorCalls != 1 {
		t.Fatalf("extractor calls = %d, want 1", extractorCalls)
	}

	job, found, err := store.LoadMemoryExtractionJob(
		context.Background(),
		event.ID,
	)
	if err != nil || !found {
		t.Fatalf("LoadMemoryExtractionJob() found = %t, error = %v", found, err)
	}
	if job.Status != journal.MemoryJobCompleted ||
		job.ResultOutcome != protocol.OutcomeNoChange ||
		job.Model != "test-routine-model" {
		t.Fatalf("completed memory job = %+v", job)
	}
	state, err := store.LoadJournalStateSnapshot(context.Background())
	if err != nil {
		t.Fatalf("LoadJournalStateSnapshot() error = %v", err)
	}
	if state.LastOperationSeq != 0 {
		t.Fatalf("last operation seq = %d, want 0", state.LastOperationSeq)
	}
	_, refreshFound, err := store.ClaimNextCapsuleRefresh(
		context.Background(),
		memoryExtractorTestTime.Add(time.Minute),
		time.Minute,
	)
	if err != nil {
		t.Fatalf("ClaimNextCapsuleRefresh() error = %v", err)
	}
	if refreshFound {
		t.Fatal("no_change unexpectedly enqueued a capsule refresh")
	}
}

func TestBackgroundMemoryWorkerAppliesUpdateAndQueuesRefresh(t *testing.T) {
	store, root := openMemoryWorkerStore(t)
	event := appendMemoryWorkerJob(t, store, root, "event-worker-update")
	worker := testBackgroundMemoryWorker(
		store,
		root,
		MemoryDecisionFunc(func(
			_ context.Context,
			request MemoryExtractionRequest,
		) (MemoryExtractionResult, error) {
			update := validExtractedMemoryUpdate(request.Job)
			return MemoryExtractionResult{
				Result: protocol.ExtractionResult{
					Protocol:     protocol.Version,
					Outcome:      protocol.OutcomeMemoryUpdate,
					MemoryUpdate: &update,
				},
				Model:        "test-routine-model",
				AttemptCount: 1,
			}, nil
		}),
	)

	result, err := worker.ProcessNext(context.Background())
	if err != nil {
		t.Fatalf("ProcessNext() error = %v", err)
	}
	if !result.Found ||
		result.Outcome != protocol.OutcomeMemoryUpdate ||
		!result.MemoryChanged ||
		!result.RefreshQueued {
		t.Fatalf("ProcessNext() = %+v, want update and refresh", result)
	}

	snapshot, found, err := store.LoadMemoryView(context.Background())
	if err != nil || !found {
		t.Fatalf("LoadMemoryView() found = %t, error = %v", found, err)
	}
	if len(snapshot.View.Records) != 1 ||
		snapshot.View.Records[0].Record.Source.EventID != event.ID {
		t.Fatalf("memory view = %+v", snapshot.View)
	}
	refresh, found, err := store.ClaimNextCapsuleRefresh(
		context.Background(),
		memoryExtractorTestTime.Add(time.Minute),
		time.Minute,
	)
	if err != nil || !found {
		t.Fatalf("ClaimNextCapsuleRefresh() found = %t, error = %v", found, err)
	}
	if refresh.Source.OperationSeq != snapshot.View.LastOperationSeq ||
		refresh.Source.ViewDigest != snapshot.View.Digest {
		t.Fatalf("refresh source = %+v, snapshot = %+v", refresh.Source, snapshot)
	}

	next, err := worker.ProcessNext(context.Background())
	if err != nil {
		t.Fatalf("second ProcessNext() error = %v", err)
	}
	if next.Found {
		t.Fatalf("second ProcessNext() = %+v, want no memory job", next)
	}
}

func TestBackgroundMemoryWorkerRetriesFailedExtraction(t *testing.T) {
	store, root := openMemoryWorkerStore(t)
	event := appendMemoryWorkerJob(t, store, root, "event-worker-retry")
	currentTime := memoryExtractorTestTime
	calls := 0
	worker := testBackgroundMemoryWorker(
		store,
		root,
		MemoryDecisionFunc(func(
			_ context.Context,
			_ MemoryExtractionRequest,
		) (MemoryExtractionResult, error) {
			calls++
			if calls == 1 {
				return MemoryExtractionResult{}, errors.New("temporary model failure")
			}
			return MemoryExtractionResult{
				Result: protocol.ExtractionResult{
					Protocol: protocol.Version,
					Outcome:  protocol.OutcomeNoChange,
				},
				Model:        "test-repair-model",
				AttemptCount: 2,
			}, nil
		}),
	)
	worker.Now = func() time.Time { return currentTime }

	first, err := worker.ProcessNext(context.Background())
	if err == nil || !first.Found {
		t.Fatalf("first ProcessNext() = %+v, error = %v", first, err)
	}
	job, found, loadErr := store.LoadMemoryExtractionJob(
		context.Background(),
		event.ID,
	)
	if loadErr != nil || !found {
		t.Fatalf("LoadMemoryExtractionJob() found = %t, error = %v", found, loadErr)
	}
	if job.Status != journal.MemoryJobPending ||
		job.AttemptCount != 1 ||
		job.NextAttemptAt == nil {
		t.Fatalf("retryable memory job = %+v", job)
	}

	currentTime = currentTime.Add(worker.RetryDelay / 2)
	beforeRetry, err := worker.ProcessNext(context.Background())
	if err != nil || beforeRetry.Found {
		t.Fatalf("before retry ProcessNext() = %+v, error = %v", beforeRetry, err)
	}

	currentTime = currentTime.Add(worker.RetryDelay)
	retried, err := worker.ProcessNext(context.Background())
	if err != nil {
		t.Fatalf("retried ProcessNext() error = %v", err)
	}
	if !retried.Found || retried.Outcome != protocol.OutcomeNoChange {
		t.Fatalf("retried ProcessNext() = %+v", retried)
	}
}

func openMemoryWorkerStore(t *testing.T) (*journal.Store, string) {
	t.Helper()
	root := t.TempDir()
	store, err := journal.Open(
		context.Background(),
		journal.OpenOptions{ProjectRoot: root},
	)
	if err != nil {
		t.Fatalf("journal.Open() error = %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store, root
}

func appendMemoryWorkerJob(
	t *testing.T,
	store *journal.Store,
	root string,
	eventID string,
) protocol.TransientEvent {
	t.Helper()
	event := protocol.TransientEvent{
		Protocol:   protocol.Version,
		ID:         eventID,
		SessionID:  "session-memory-worker",
		Kind:       protocol.EventUserPrompt,
		OccurredAt: memoryExtractorTestTime,
		CWD:        root,
		Content:    "Use UTC timestamps throughout this project.",
	}
	_, _, err := store.AppendAndRebuildMemoryView(
		context.Background(),
		journal.AppendRequest{
			Event:       event,
			Adapter:     "codex-cli",
			PrivacyMode: protocol.PrivacyStandard,
			MemoryJob: &journal.MemoryJobRequest{
				PromptPolicyVersion: MemoryPromptPolicyVersion,
				EnqueuedAt:          event.OccurredAt,
			},
		},
	)
	if err != nil {
		t.Fatalf("AppendAndRebuildMemoryView() error = %v", err)
	}
	return event
}

func testBackgroundMemoryWorker(
	store *journal.Store,
	root string,
	extractor MemoryDecisionMaker,
) BackgroundMemoryWorker {
	return BackgroundMemoryWorker{
		Jobs:            store,
		DecisionMaker:   extractor,
		ProjectRoot:     root,
		RepositoryScope: DefaultRepositoryScope,
		RefreshConfiguration: journal.RefreshConfiguration{
			PrivacyMode: protocol.PrivacyStandard,
			Limits: compiler.BudgetLimits{
				Target:  256,
				Trigger: 512,
				Hard:    1024,
			},
			CompilerPolicyVersion: compiler.CompilerPolicyVersion,
			TokenCounterIdentity:  compiler.RenderCounterIdentity,
		},
		LeaseDuration: time.Minute,
		RetryDelay:    time.Second,
		MaxAttempts:   DefaultMaxMemoryJobAttempts,
		Now: func() time.Time {
			return memoryExtractorTestTime.Add(time.Second)
		},
	}
}
