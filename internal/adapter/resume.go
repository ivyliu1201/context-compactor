package adapter

import (
	"strings"
	"unicode"
)

const ResumeCommand = "/context-resume"

type ResumeIntent string

const (
	ResumeIntentNone            ResumeIntent = "none"
	ResumeIntentNaturalLanguage ResumeIntent = "natural_language"
	ResumeIntentExplicitCommand ResumeIntent = "explicit_command"
)

type ResumePromptScope string

const (
	ResumePromptNewSession    ResumePromptScope = "new_session_first_prompt"
	ResumePromptActiveSession ResumePromptScope = "active_session"
)

var naturalResumePhrases = map[string]struct{}{
	"continue":                   {},
	"continue the project":       {},
	"continue the work":          {},
	"continue where we left off": {},
	"continue work":              {},
	"pick up where we left off":  {},
	"resume":                     {},
	"resume the project":         {},
	"resume the work":            {},
	"resume work":                {},
	"從上次進度繼續":                    {},
	"恢復專案":                       {},
	"接著上次的進度":                    {},
	"接著做":                        {},
	"繼續":                         {},
	"繼續上次的工作":                    {},
	"繼續專案":                       {},
	"繼續工作":                       {},
	"繼續未完成的工作":                   {},
	"繼續這個專案":                     {},
	"繼續做":                        {},
}

// ClassifyResumeIntent identifies only the trigger for the resume preview
// flow. It performs no checkpoint reads, repository reconciliation, or state
// changes.
func ClassifyResumeIntent(prompt string, scope ResumePromptScope) ResumeIntent {
	trimmed := strings.TrimSpace(prompt)
	if strings.EqualFold(trimmed, ResumeCommand) {
		return ResumeIntentExplicitCommand
	}
	if scope != ResumePromptNewSession || strings.HasPrefix(trimmed, "/") {
		return ResumeIntentNone
	}

	normalized := normalizeNaturalResumePhrase(trimmed)
	if _, ok := naturalResumePhrases[normalized]; ok {
		return ResumeIntentNaturalLanguage
	}
	return ResumeIntentNone
}

func normalizeNaturalResumePhrase(prompt string) string {
	trimmed := strings.TrimFunc(prompt, func(character rune) bool {
		return unicode.IsSpace(character) || unicode.IsPunct(character)
	})
	return strings.ToLower(strings.Join(strings.Fields(trimmed), " "))
}
