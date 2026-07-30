// Package privacy contains shared guards for values that may become durable.
package privacy

import (
	"regexp"
	"strings"
)

var secretPattern = regexp.MustCompile(
	`(?i)(authorization\s*:\s*(bearer|basic)\s+\S+|` +
		`(?:api[_-]?key|token|secret|password|passwd|pwd)\s*[:=]\s*["']?\S+|` +
		`-----BEGIN (?:RSA |EC |OPENSSH )?PRIVATE KEY-----|` +
		`\bsk-[A-Za-z0-9_-]{8,}\b)`,
)

// ContainsPotentialSecret reports whether text resembles a credential that
// must not be persisted in memory, checkpoints, fixtures, or logs.
func ContainsPotentialSecret(text string) bool {
	return secretPattern.MatchString(text)
}

// RedactPotentialSecrets replaces credential-like spans before text crosses a
// durable boundary. The count is useful for diagnostics without revealing the
// matched values.
func RedactPotentialSecrets(text string) (string, int) {
	count := 0
	redacted := secretPattern.ReplaceAllStringFunc(text, func(string) string {
		count++
		return "[REDACTED]"
	})
	return strings.TrimSpace(redacted), count
}
