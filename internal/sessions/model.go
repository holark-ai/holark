package sessions

import (
	"errors"
	"strings"
	"unicode/utf8"
)

const MaxTitleCharacters = 80

var ErrNotFound = errors.New("session not found")

// Session is the durable session metadata needed by session-domain operations.
type Session struct {
	ID     string
	Title  string
	Prompt string
}

// TitleWithinLimit reports whether title fits the session-title character limit.
func TitleWithinLimit(title string) bool {
	return utf8.RuneCountInString(title) <= MaxTitleCharacters
}

// NormalizeTitle applies the canonical session-title fallback rules.
func NormalizeTitle(title, prompt string) string {
	title = strings.TrimSpace(title)
	if title == "" {
		for _, line := range strings.Split(prompt, "\n") {
			if title = strings.TrimSpace(line); title != "" {
				break
			}
		}
	}
	if title == "" {
		title = "Session"
	}
	runes := 0
	for index := range title {
		if runes == MaxTitleCharacters {
			return strings.Clone(title[:index])
		}
		runes++
	}
	return strings.Clone(title)
}
