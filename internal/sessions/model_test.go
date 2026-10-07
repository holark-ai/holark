package sessions

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestTitleWithinLimitCountsUnicodeCharacters(t *testing.T) {
	boundary := strings.Repeat("é", MaxTitleCharacters-1) + "🙂"
	if !TitleWithinLimit(boundary) {
		t.Fatal("80 non-ASCII characters should fit the title limit")
	}
	if TitleWithinLimit(boundary + "界") {
		t.Fatal("81 Unicode characters should exceed the title limit")
	}
}

func TestNormalizeTitleTruncatesOnRuneBoundary(t *testing.T) {
	boundary := strings.Repeat("é", MaxTitleCharacters-1) + "🙂"
	for _, test := range []struct {
		name   string
		title  string
		prompt string
	}{
		{name: "explicit title", title: boundary + "界"},
		{name: "prompt fallback", prompt: "\n  " + boundary + "界"},
	} {
		t.Run(test.name, func(t *testing.T) {
			title := NormalizeTitle(test.title, test.prompt)
			if title != boundary {
				t.Fatalf("title = %q, want %q", title, boundary)
			}
			if !utf8.ValidString(title) {
				t.Fatalf("title is not valid UTF-8: %q", title)
			}
		})
	}
}
