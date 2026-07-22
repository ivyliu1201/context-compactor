package adapter

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"

	"context-compactor/internal/compiler"
	"context-compactor/internal/reducer"
)

type CapsuleRefreshTrigger string

const (
	RefreshAfterTurn  CapsuleRefreshTrigger = "after_turn"
	RefreshDuringIdle CapsuleRefreshTrigger = "during_idle"
)

var ErrNoVerifiedCapsule = errors.New("no verified context capsule is available")

// CapsuleSourceSnapshot identifies the fixed derived view consumed by one
// background refresh. Complete prompts are not part of this snapshot.
type CapsuleSourceSnapshot struct {
	EventSeq     int64
	OperationSeq int64
	ViewDigest   string
}

type CapsuleRefreshRequest struct {
	RepositoryScope string
	Trigger         CapsuleRefreshTrigger
	Source          CapsuleSourceSnapshot
	Build           func(context.Context, CapsuleSourceSnapshot) (compiler.VerifiedCapsule, error)
}

type CapsuleRefreshResult struct {
	RepositoryScope string
	Generation      uint64
	Published       bool
	Discarded       bool
	Capsule         compiler.VerifiedCapsule
	Err             error
}

type CapsuleRefreshCoordinator struct {
	mu     sync.RWMutex
	scopes map[string]*capsuleRefreshScope
}

type capsuleRefreshScope struct {
	generation uint64
	capsule    compiler.VerifiedCapsule
	hasCapsule bool
}

// NewCapsuleRefreshCoordinator validates and clones the last verified capsule
// for each repository. A nil initial map starts without a foreground fallback.
func NewCapsuleRefreshCoordinator(
	initial map[string]compiler.VerifiedCapsule,
) (*CapsuleRefreshCoordinator, error) {
	coordinator := &CapsuleRefreshCoordinator{
		scopes: make(map[string]*capsuleRefreshScope, len(initial)),
	}
	for repositoryScope, capsule := range initial {
		scope, err := normalizeRepositoryScope(repositoryScope)
		if err != nil {
			return nil, err
		}
		cloned, err := validatedCapsuleClone(capsule)
		if err != nil {
			return nil, fmt.Errorf("repository scope %q: %w", scope, err)
		}
		coordinator.scopes[scope] = &capsuleRefreshScope{
			capsule:    cloned,
			hasCapsule: true,
		}
	}
	return coordinator, nil
}

// Schedule starts one background refresh and returns a buffered result channel
// without waiting for Build. Callers may ignore the channel when refresh status
// is observed elsewhere.
func (coordinator *CapsuleRefreshCoordinator) Schedule(
	ctx context.Context,
	request CapsuleRefreshRequest,
) (<-chan CapsuleRefreshResult, error) {
	if coordinator == nil {
		return nil, fmt.Errorf("refresh coordinator is required")
	}
	if ctx == nil {
		return nil, fmt.Errorf("refresh context is required")
	}
	scope, err := validateRefreshRequest(request)
	if err != nil {
		return nil, err
	}

	coordinator.mu.Lock()
	state := coordinator.scopes[scope]
	if state == nil {
		state = &capsuleRefreshScope{}
		coordinator.scopes[scope] = state
	}
	if state.hasCapsule && sourceOlderThanCapsule(request.Source, state.capsule) {
		coordinator.mu.Unlock()
		return nil, fmt.Errorf("refresh source is older than the verified capsule")
	}
	state.generation++
	generation := state.generation
	coordinator.mu.Unlock()

	result := make(chan CapsuleRefreshResult, 1)
	go coordinator.runRefresh(ctx, scope, generation, request, result)
	return result, nil
}

// ForegroundContext never waits for a refresh. It combines the current
// verified capsule with already validated operations that follow its cursor.
func (coordinator *CapsuleRefreshCoordinator) ForegroundContext(
	repositoryScope string,
	operations []reducer.OperationEnvelope,
) (compiler.PendingContext, error) {
	capsule, found, err := coordinator.LatestVerifiedCapsule(repositoryScope)
	if err != nil {
		return compiler.PendingContext{}, err
	}
	if !found {
		return compiler.PendingContext{}, ErrNoVerifiedCapsule
	}
	pending, err := compiler.ComposePendingContext(capsule, operations)
	if err != nil {
		return compiler.PendingContext{}, fmt.Errorf("compose foreground context: %w", err)
	}
	return pending, nil
}

func (coordinator *CapsuleRefreshCoordinator) LatestVerifiedCapsule(
	repositoryScope string,
) (compiler.VerifiedCapsule, bool, error) {
	if coordinator == nil {
		return compiler.VerifiedCapsule{}, false, fmt.Errorf("refresh coordinator is required")
	}
	scope, err := normalizeRepositoryScope(repositoryScope)
	if err != nil {
		return compiler.VerifiedCapsule{}, false, err
	}

	coordinator.mu.RLock()
	state := coordinator.scopes[scope]
	if state == nil || !state.hasCapsule {
		coordinator.mu.RUnlock()
		return compiler.VerifiedCapsule{}, false, nil
	}
	capsule := state.capsule
	coordinator.mu.RUnlock()

	cloned, err := validatedCapsuleClone(capsule)
	if err != nil {
		return compiler.VerifiedCapsule{}, false, fmt.Errorf("clone verified capsule: %w", err)
	}
	return cloned, true, nil
}

func (coordinator *CapsuleRefreshCoordinator) runRefresh(
	ctx context.Context,
	scope string,
	generation uint64,
	request CapsuleRefreshRequest,
	result chan<- CapsuleRefreshResult,
) {
	outcome := CapsuleRefreshResult{
		RepositoryScope: scope,
		Generation:      generation,
	}
	defer func() {
		result <- outcome
		close(result)
	}()

	capsule, err := request.Build(ctx, request.Source)
	if err != nil {
		outcome.Err = fmt.Errorf("build context capsule: %w", err)
		return
	}
	cloned, err := validatedCapsuleClone(capsule)
	if err != nil {
		outcome.Err = err
		return
	}
	if !capsuleMatchesSource(cloned, request.Source) {
		outcome.Err = fmt.Errorf("compiled capsule does not match its fixed source snapshot")
		return
	}
	outcomeCapsule, err := validatedCapsuleClone(cloned)
	if err != nil {
		outcome.Err = err
		return
	}

	coordinator.mu.Lock()
	state := coordinator.scopes[scope]
	if state == nil || generation != state.generation ||
		(state.hasCapsule && sourceOlderThanCapsule(request.Source, state.capsule)) {
		coordinator.mu.Unlock()
		outcome.Discarded = true
		return
	}
	state.capsule = cloned
	state.hasCapsule = true
	coordinator.mu.Unlock()

	outcome.Published = true
	outcome.Capsule = outcomeCapsule
}

func validateRefreshRequest(request CapsuleRefreshRequest) (string, error) {
	scope, err := normalizeRepositoryScope(request.RepositoryScope)
	if err != nil {
		return "", err
	}
	switch request.Trigger {
	case RefreshAfterTurn, RefreshDuringIdle:
	default:
		return "", fmt.Errorf("unsupported capsule refresh trigger %q", request.Trigger)
	}
	if request.Source.EventSeq < 0 {
		return "", fmt.Errorf("source event sequence must not be negative")
	}
	if request.Source.OperationSeq < 0 {
		return "", fmt.Errorf("source operation sequence must not be negative")
	}
	if !isSHA256Digest(request.Source.ViewDigest) {
		return "", fmt.Errorf("source view digest must be a SHA-256 hex digest")
	}
	if request.Build == nil {
		return "", fmt.Errorf("capsule refresh builder is required")
	}
	return scope, nil
}

func normalizeRepositoryScope(repositoryScope string) (string, error) {
	scope := strings.TrimSpace(repositoryScope)
	if scope == "" {
		return "", fmt.Errorf("repository scope is required")
	}
	return scope, nil
}

func validatedCapsuleClone(capsule compiler.VerifiedCapsule) (compiler.VerifiedCapsule, error) {
	pending, err := compiler.ComposePendingContext(capsule, nil)
	if err != nil {
		return compiler.VerifiedCapsule{}, fmt.Errorf("verify context capsule: %w", err)
	}
	return pending.Capsule, nil
}

func capsuleMatchesSource(
	capsule compiler.VerifiedCapsule,
	source CapsuleSourceSnapshot,
) bool {
	return capsule.SourceEventSeq == source.EventSeq &&
		capsule.SourceOperationSeq == source.OperationSeq &&
		capsule.SourceViewDigest == source.ViewDigest
}

func sourceOlderThanCapsule(
	source CapsuleSourceSnapshot,
	capsule compiler.VerifiedCapsule,
) bool {
	return source.EventSeq < capsule.SourceEventSeq ||
		source.OperationSeq < capsule.SourceOperationSeq
}

func isSHA256Digest(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size
}
