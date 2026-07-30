package privacy

import (
	"strings"
	"testing"
)

func TestRedactPotentialSecretsRemovesCredentialLikeSpans(t *testing.T) {
	input := "Keep UTC. Authorization: Bearer example-credential\napi_key=example-key"

	redacted, count := RedactPotentialSecrets(input)

	if count != 2 {
		t.Fatalf("redaction count = %d, want 2", count)
	}
	if ContainsPotentialSecret(redacted) {
		t.Fatalf("redacted text still resembles a secret: %q", redacted)
	}
	if strings.Contains(redacted, "example-credential") ||
		strings.Contains(redacted, "example-key") {
		t.Fatalf("redacted text retains credential content: %q", redacted)
	}
	if !strings.Contains(redacted, "Keep UTC.") {
		t.Fatalf("redacted text lost safe content: %q", redacted)
	}
}

func TestRedactPotentialSecretsLeavesSafeTextUnchanged(t *testing.T) {
	const input = "Use UTC for project timestamps."

	redacted, count := RedactPotentialSecrets(input)

	if count != 0 || redacted != input {
		t.Fatalf("RedactPotentialSecrets() = %q, %d; want unchanged", redacted, count)
	}
}
