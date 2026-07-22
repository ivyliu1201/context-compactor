package compiler

import (
	"sort"
	"strings"
	"unicode"

	"context-compactor/internal/protocol"
	"context-compactor/internal/reducer"
)

const lexicalScoreScale = 1000

// RelevanceScore exposes each deterministic ranking component. Lexical is a
// 0..1000 query-term coverage score, Priority is 1..4, and Recency is the
// durable source operation sequence.
type RelevanceScore struct {
	Lexical  int
	Priority int
	Recency  int64
}

type ScoredRecord struct {
	Record reducer.MaterializedRecord
	Score  RelevanceScore
}

// RankRelevant orders active, non-mandatory records for later token-budget
// selection. Lexical relevance wins first, then explicit priority, recency, and
// stable record ID. Mandatory records remain governed by ReserveMandatory and
// never depend on query recall.
func RankRelevant(view reducer.View, query string) []ScoredRecord {
	queryTerms := tokenize(query)
	result := make([]ScoredRecord, 0, len(view.Records))
	for _, record := range view.Records {
		if record.Lifecycle != reducer.LifecycleActive {
			continue
		}
		if _, mandatory := mandatoryCategory(record.Record); mandatory {
			continue
		}
		result = append(result, ScoredRecord{
			Record: record,
			Score: RelevanceScore{
				Lexical:  lexicalScore(queryTerms, recordTerms(record.Record)),
				Priority: relevancePriority(record.Record.Priority),
				Recency:  record.SourceOperationSeq,
			},
		})
	}

	sort.Slice(result, func(left, right int) bool {
		leftScore := result[left].Score
		rightScore := result[right].Score
		if leftScore.Lexical != rightScore.Lexical {
			return leftScore.Lexical > rightScore.Lexical
		}
		if leftScore.Priority != rightScore.Priority {
			return leftScore.Priority > rightScore.Priority
		}
		if leftScore.Recency != rightScore.Recency {
			return leftScore.Recency > rightScore.Recency
		}
		return result[left].Record.Record.ID < result[right].Record.Record.ID
	})
	return result
}

func lexicalScore(queryTerms, memoryTerms map[string]struct{}) int {
	if len(queryTerms) == 0 {
		return 0
	}
	matches := 0
	for term := range queryTerms {
		if _, exists := memoryTerms[term]; exists {
			matches++
		}
	}
	return matches * lexicalScoreScale / len(queryTerms)
}

func recordTerms(record protocol.MemoryRecord) map[string]struct{} {
	return tokenize(strings.Join([]string{
		record.ID,
		record.ConflictKey,
		string(record.Kind),
		record.Value,
		record.Source.EventID,
		record.Source.Evidence,
		record.Source.Artifact,
	}, " "))
}

func tokenize(value string) map[string]struct{} {
	terms := make(map[string]struct{})
	current := make([]rune, 0, len(value))
	flush := func() {
		if len(current) == 0 {
			return
		}
		terms[string(current)] = struct{}{}
		current = current[:0]
	}

	var previous rune
	for _, character := range value {
		if unicode.Is(unicode.Han, character) {
			flush()
			terms[string(character)] = struct{}{}
			previous = 0
			continue
		}
		if !unicode.IsLetter(character) && !unicode.IsDigit(character) {
			flush()
			previous = 0
			continue
		}
		if unicode.IsUpper(character) &&
			len(current) > 0 &&
			(unicode.IsLower(previous) || unicode.IsDigit(previous)) {
			flush()
		}
		current = append(current, unicode.ToLower(character))
		previous = character
	}
	flush()
	return terms
}

func relevancePriority(priority protocol.Priority) int {
	switch priority {
	case protocol.PriorityCritical:
		return 4
	case protocol.PriorityHigh:
		return 3
	case protocol.PriorityNormal:
		return 2
	case protocol.PriorityLow:
		return 1
	default:
		return 0
	}
}
