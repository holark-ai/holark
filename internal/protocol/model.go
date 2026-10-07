package protocol

import (
	"unicode"
	"unicode/utf8"
)

// ValidModel accepts a native model identifier, or empty for the agent default.
func ValidModel(value string) bool {
	if len(value) > 256 || !utf8.ValidString(value) {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) || unicode.IsSpace(r) {
			return false
		}
	}
	return true
}

type ModelChoice struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}
