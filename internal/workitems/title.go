package workitems

import (
	"strings"
	"unicode"
)

// MatchTitle matches every search term, allowing one typo in words of at least
// four characters. Short terms and punctuation remain literal substrings.
func MatchTitle(title, search string) bool {
	title = strings.ToLower(title)
	words := strings.FieldsFunc(title, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
	for _, term := range strings.Fields(strings.ToLower(search)) {
		if strings.Contains(title, term) {
			continue
		}
		letters := []rune(term)
		if len(letters) < 4 || strings.IndexFunc(term, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }) >= 0 {
			return false
		}
		matched := false
		for _, word := range words {
			if oneEditApart(letters, []rune(word)) {
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

func oneEditApart(a, b []rune) bool {
	if len(a) > len(b) {
		a, b = b, a
	}
	if len(b)-len(a) > 1 {
		return false
	}
	i := 0
	for i < len(a) && a[i] == b[i] {
		i++
	}
	if i == len(a) {
		return true
	}
	if len(a) != len(b) {
		return string(a[i:]) == string(b[i+1:])
	}
	if string(a[i+1:]) == string(b[i+1:]) {
		return true
	}
	return i+1 < len(a) && a[i] == b[i+1] && a[i+1] == b[i] && string(a[i+2:]) == string(b[i+2:])
}
