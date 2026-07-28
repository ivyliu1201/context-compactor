package runtime

import (
	"context"
	"fmt"
	"strings"

	"github.com/ivyliu1201/context-compactor/internal/adapter"
	"github.com/ivyliu1201/context-compactor/internal/compiler"
	"github.com/ivyliu1201/context-compactor/internal/reducer"
)

// VerifiedCapsuleProvider supplies an immutable verified capsule snapshot for
// one repository scope.
type VerifiedCapsuleProvider interface {
	LatestVerifiedCapsule(string) (compiler.VerifiedCapsule, bool, error)
}

// OperationReader supplies durable operations after an exclusive sequence
// cursor.
type OperationReader interface {
	LoadOperationsAfter(
		context.Context,
		int64,
	) ([]reducer.OperationEnvelope, error)
}

// ForegroundLoader composes the latest observed verified capsule with all
// durable operations that follow that exact capsule's cursor.
type ForegroundLoader struct {
	Capsules   VerifiedCapsuleProvider
	Operations OperationReader
}

func (loader ForegroundLoader) Load(
	ctx context.Context,
	repositoryScope string,
) (compiler.PendingContext, error) {
	if ctx == nil {
		return compiler.PendingContext{}, fmt.Errorf("foreground context is required")
	}
	if loader.Capsules == nil {
		return compiler.PendingContext{}, fmt.Errorf("verified capsule provider is required")
	}
	if loader.Operations == nil {
		return compiler.PendingContext{}, fmt.Errorf("operation reader is required")
	}
	scope := strings.TrimSpace(repositoryScope)
	if scope == "" {
		return compiler.PendingContext{}, fmt.Errorf("repository scope is required")
	}

	capsule, found, err := loader.Capsules.LatestVerifiedCapsule(scope)
	if err != nil {
		return compiler.PendingContext{}, fmt.Errorf("load verified capsule: %w", err)
	}
	if !found {
		return compiler.PendingContext{}, adapter.ErrNoVerifiedCapsule
	}

	operations, err := loader.Operations.LoadOperationsAfter(
		ctx,
		capsule.SourceOperationSeq,
	)
	if err != nil {
		return compiler.PendingContext{}, fmt.Errorf("load operations after capsule: %w", err)
	}
	pending, err := compiler.ComposePendingContext(capsule, operations)
	if err != nil {
		return compiler.PendingContext{}, fmt.Errorf("compose foreground context: %w", err)
	}
	return pending, nil
}
