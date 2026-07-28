package journal

import (
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ivyliu1201/context-compactor/internal/privacy"
	"github.com/ivyliu1201/context-compactor/internal/protocol"
)

const (
	maxSummaryRunes   = 2000
	maxGitHeadRunes   = 128
	maxGitBranchRunes = 256
)

var (
	identifierPattern  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)
	fingerprintPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)
)

func validateIdentifier(name, value string) error {
	if !identifierPattern.MatchString(value) {
		return fmt.Errorf("%s must match %s", name, identifierPattern.String())
	}
	return nil
}

func validatePrivacyMode(mode protocol.PrivacyMode) error {
	switch mode {
	case protocol.PrivacyStrict, protocol.PrivacyBalanced, protocol.PrivacyAudit:
		return nil
	default:
		return fmt.Errorf("unsupported privacy mode %q", mode)
	}
}

func validateDurableText(name, value string, maxRunes int, required bool) error {
	trimmed := strings.TrimSpace(value)
	if required && trimmed == "" {
		return fmt.Errorf("%s is required", name)
	}
	if utf8.RuneCountInString(value) > maxRunes {
		return fmt.Errorf("%s exceeds %d characters", name, maxRunes)
	}
	if privacy.ContainsPotentialSecret(value) {
		return fmt.Errorf("%s appears to contain a secret", name)
	}
	return nil
}

func validateUTCTime(name string, value time.Time) error {
	if value.IsZero() {
		return fmt.Errorf("%s is required", name)
	}
	_, offset := value.Zone()
	if offset != 0 {
		return fmt.Errorf("%s must use UTC", name)
	}
	return nil
}
