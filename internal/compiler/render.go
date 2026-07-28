package compiler

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/ivyliu1201/context-compactor/internal/privacy"
	"github.com/ivyliu1201/context-compactor/internal/protocol"
)

const (
	RenderCounterIdentity = "context-compactor/jsonl-utf8-bytes/v1"

	renderedContextHeader    = "<CONTEXT_COMPACTOR_STATE version=\"1\" authority=\"derived\">\n"
	renderedContextFooter    = "</CONTEXT_COMPACTOR_STATE>"
	renderCounterDescription = "conservative upper bound of one token per rendered UTF-8 byte; " +
		"not an exact host-model token count"
)

// RenderCounterProfile returns the counter that must be used with
// RenderCompiledContext. It charges one token per rendered UTF-8 byte so the
// compiler and renderer share one deterministic, model-neutral upper bound.
func RenderCounterProfile() CounterProfile {
	return CounterProfile{
		Identity:            RenderCounterIdentity,
		Mode:                CounterConservative,
		Description:         renderCounterDescription,
		FixedOverheadTokens: len(renderedContextHeader) + len(renderedContextFooter),
		CountTokens: func(record protocol.MemoryRecord) (int, error) {
			encoded, err := encodeRenderedRecord(record)
			if err != nil {
				return 0, err
			}
			return len(encoded) + 1, nil
		},
	}
}

// RenderCompiledContext serializes only budgeted, structured memory. Recovery
// lookup IDs remain control-plane data and are intentionally not rendered.
func RenderCompiledContext(compiled CompiledContext) (string, error) {
	profile := RenderCounterProfile()
	if err := validateRenderedContext(compiled, profile); err != nil {
		return "", err
	}

	var builder strings.Builder
	builder.Grow(compiled.UsedTokens)
	builder.WriteString(renderedContextHeader)
	if compiled.Recovery != nil {
		for _, record := range compiled.Recovery.Records {
			encoded, err := encodeRenderedRecord(record.Record)
			if err != nil {
				return "", fmt.Errorf("render recovery record %q: %w", record.Record.ID, err)
			}
			builder.Write(encoded)
			builder.WriteByte('\n')
		}
	} else {
		for _, record := range compiled.Records {
			encoded, err := encodeRenderedRecord(record.Record.Record)
			if err != nil {
				return "", fmt.Errorf("render record %q: %w", record.Record.Record.ID, err)
			}
			builder.Write(encoded)
			builder.WriteByte('\n')
		}
	}
	builder.WriteString(renderedContextFooter)

	rendered := builder.String()
	if len(rendered) != compiled.UsedTokens {
		return "", fmt.Errorf(
			"rendered context size %d does not match compiled token count %d",
			len(rendered),
			compiled.UsedTokens,
		)
	}
	if len(rendered) > compiled.Limits.Hard {
		return "", fmt.Errorf(
			"rendered context size %d exceeds hard limit %d",
			len(rendered),
			compiled.Limits.Hard,
		)
	}
	return rendered, nil
}

func validateRenderedContext(compiled CompiledContext, profile CounterProfile) error {
	if err := validateBudgetConfiguration(compiled.Limits, profile); err != nil {
		return fmt.Errorf("validate render budget: %w", err)
	}
	if compiled.CounterIdentity != profile.Identity ||
		compiled.CounterMode != profile.Mode ||
		compiled.CounterDescription != profile.Description ||
		compiled.FixedOverheadTokens != profile.FixedOverheadTokens {
		return fmt.Errorf("compiled context token counter does not match renderer profile")
	}
	if compiled.Recovery != nil && len(compiled.Records) != 0 {
		return fmt.Errorf("compiled context must not contain records and recovery together")
	}

	recordTokens := 0
	if compiled.Recovery != nil {
		for _, record := range compiled.Recovery.Records {
			tokens, err := profile.CountTokens(record.Record)
			if err != nil {
				return fmt.Errorf("count recovery record %q: %w", record.Record.ID, err)
			}
			if record.Tokens != tokens {
				return fmt.Errorf(
					"recovery record %q token count %d does not match rendered size %d",
					record.Record.ID,
					record.Tokens,
					tokens,
				)
			}
			recordTokens += tokens
		}
		if compiled.Recovery.Tokens != recordTokens {
			return fmt.Errorf(
				"recovery token count %d does not match rendered records %d",
				compiled.Recovery.Tokens,
				recordTokens,
			)
		}
	} else {
		for _, record := range compiled.Records {
			tokens, err := profile.CountTokens(record.Record.Record)
			if err != nil {
				return fmt.Errorf("count record %q: %w", record.Record.Record.ID, err)
			}
			if record.Tokens != tokens {
				return fmt.Errorf(
					"record %q token count %d does not match rendered size %d",
					record.Record.Record.ID,
					record.Tokens,
					tokens,
				)
			}
			recordTokens += tokens
		}
	}

	usedTokens := profile.FixedOverheadTokens + recordTokens
	if compiled.UsedTokens != usedTokens {
		return fmt.Errorf(
			"compiled token count %d does not match rendered size %d",
			compiled.UsedTokens,
			usedTokens,
		)
	}
	if compiled.RemainingHardTokens != compiled.Limits.Hard-usedTokens {
		return fmt.Errorf("compiled remaining hard tokens do not match rendered size")
	}
	if usedTokens > compiled.Limits.Hard {
		return fmt.Errorf(
			"rendered context size %d exceeds hard limit %d",
			usedTokens,
			compiled.Limits.Hard,
		)
	}
	return nil
}

func encodeRenderedRecord(record protocol.MemoryRecord) ([]byte, error) {
	encoded, err := json.Marshal(record)
	if err != nil {
		return nil, fmt.Errorf("encode memory record: %w", err)
	}
	if privacy.ContainsPotentialSecret(string(encoded)) {
		return nil, fmt.Errorf("rendered memory record contains a potential secret")
	}
	return encoded, nil
}
