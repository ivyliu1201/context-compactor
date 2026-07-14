// Package privacy contains shared guards for values that may become durable.
package privacy

import "regexp"

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
