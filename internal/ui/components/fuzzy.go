package components

import (
	"strings"
	"unicode"
)

// FuzzyMatch performs a case-insensitive multi-token match where every token
// must match at least one provided field (substring or single-field subsequence).
// Empty query always matches.
func FuzzyMatch(query string, fields ...string) bool {
	q := strings.TrimSpace(strings.ToLower(query))
	if q == "" {
		return true
	}

	lowerFields := make([]string, 0, len(fields))
	for _, f := range fields {
		if s := strings.TrimSpace(strings.ToLower(f)); s != "" {
			lowerFields = append(lowerFields, s)
		}
	}
	if len(lowerFields) == 0 {
		return false
	}

	tokens := strings.Fields(q)
	for _, tok := range tokens {
		matched := false
		requireSubstr := strings.ContainsAny(tok, ".:/@")
		for _, field := range lowerFields {
			if strings.Contains(field, tok) {
				matched = true
				break
			}
			if !requireSubstr && subsequenceMatch(tok, field) {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	return true
}

func subsequenceMatch(needle, haystack string) bool {
	if needle == "" {
		return true
	}
	nr := []rune(needle)
	ni := 0
	for _, r := range haystack {
		if unicode.ToLower(r) == nr[ni] {
			ni++
			if ni == len(nr) {
				return true
			}
		}
	}
	return false
}
