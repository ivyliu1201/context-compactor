package adapter

import "testing"

func TestClassifyResumeIntentDetectsNaturalLanguageAtNewSessionStart(t *testing.T) {
	tests := []string{
		"resume",
		"  Continue   where we left off. ",
		"pick up where we left off",
		"繼續",
		"繼續上次的工作。",
		"從上次進度繼續",
		"接著上次的進度！",
	}

	for _, prompt := range tests {
		t.Run(prompt, func(t *testing.T) {
			if got := ClassifyResumeIntent(prompt, ResumePromptNewSession); got != ResumeIntentNaturalLanguage {
				t.Fatalf("ClassifyResumeIntent(%q) = %q, want %q", prompt, got, ResumeIntentNaturalLanguage)
			}
		})
	}
}

func TestClassifyResumeIntentLimitsNaturalLanguageToNewSessionStart(t *testing.T) {
	tests := []string{
		"continue",
		"resume the project",
		"好，那繼續做下一個",
		"繼續上次的工作",
	}

	for _, prompt := range tests {
		t.Run(prompt, func(t *testing.T) {
			if got := ClassifyResumeIntent(prompt, ResumePromptActiveSession); got != ResumeIntentNone {
				t.Fatalf("ClassifyResumeIntent(%q) = %q, want %q", prompt, got, ResumeIntentNone)
			}
		})
	}
}

func TestClassifyResumeIntentAcceptsExplicitCommandInAnySession(t *testing.T) {
	for _, scope := range []ResumePromptScope{ResumePromptNewSession, ResumePromptActiveSession} {
		t.Run(string(scope), func(t *testing.T) {
			if got := ClassifyResumeIntent("  /CONTEXT-RESUME  ", scope); got != ResumeIntentExplicitCommand {
				t.Fatalf("ClassifyResumeIntent() = %q, want %q", got, ResumeIntentExplicitCommand)
			}
		})
	}
}

func TestClassifyResumeIntentRejectsAmbiguousOrHostSpecificPrompts(t *testing.T) {
	tests := []struct {
		name   string
		prompt string
		scope  ResumePromptScope
	}{
		{name: "blank", scope: ResumePromptNewSession},
		{name: "continue loop", prompt: "continue loop", scope: ResumePromptNewSession},
		{name: "resume download", prompt: "resume download", scope: ResumePromptNewSession},
		{name: "resume question", prompt: "can this API resume uploads?", scope: ResumePromptNewSession},
		{name: "claude command", prompt: "/resume", scope: ResumePromptNewSession},
		{name: "command arguments", prompt: "/context-resume now", scope: ResumePromptNewSession},
		{name: "unknown scope", prompt: "continue", scope: ResumePromptScope("unknown")},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := ClassifyResumeIntent(test.prompt, test.scope); got != ResumeIntentNone {
				t.Fatalf("ClassifyResumeIntent(%q) = %q, want %q", test.prompt, got, ResumeIntentNone)
			}
		})
	}
}
