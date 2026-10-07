package comments

import (
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

const ContextCommentLimit = 50
const ContextCharacterLimit = 20000

type Context struct {
	Text             string     `json:"text"`
	SyncedAt         *time.Time `json:"synced_at"`
	Freshness        string     `json:"freshness"`
	TotalComments    int        `json:"total_comments"`
	IncludedComments int        `json:"included_comments"`
	OmittedComments  int        `json:"omitted_comments"`
	Truncated        bool       `json:"truncated"`
}

// BuildContext reads no provider data. Newest content takes priority within the
// budget, but the selected comments are presented in chronological order.
func BuildContext(d Discussion) Context {
	out := Context{SyncedAt: d.SyncedAt, Freshness: "cached", TotalComments: len(d.Comments)}
	prefix := "Issue discussion (cached; freshness is not verified).\n"
	if d.SyncedAt != nil {
		prefix = "Issue discussion (cached; last synced " + d.SyncedAt.UTC().Format(time.RFC3339) + ").\n"
	}
	if len(d.Comments) == 0 {
		out.Text = prefix + "No cached comments.\n"
		return out
	}
	ordered := append([]Comment(nil), d.Comments...)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].CreatedAt.Before(ordered[j].CreatedAt) })
	remaining := ContextCharacterLimit - utf8.RuneCountInString(prefix) - 512
	entries := []string{}
	for i := len(ordered) - 1; i >= 0 && len(entries) < ContextCommentLimit && remaining > 0; i-- {
		c := ordered[i]
		author := c.Author.Login
		if author == "" {
			author = "Unknown author"
		}
		header := fmt.Sprintf("\nComment %s by %s at %s\n%s\n", c.ID, author, c.CreatedAt.UTC().Format(time.RFC3339), c.URL)
		headerSize := utf8.RuneCountInString(header)
		if headerSize+32 > remaining {
			break
		}
		body := []rune(c.Body)
		if len(body)+headerSize > remaining {
			body = body[:remaining-headerSize-24]
			out.Truncated = true
			entries = append(entries, header+string(body)+"\n[Comment truncated]\n")
			break
		}
		entries = append(entries, header+c.Body+"\n")
		remaining -= headerSize + len(body) + 1
	}
	out.IncludedComments = len(entries)
	out.OmittedComments = len(ordered) - len(entries)
	var text strings.Builder
	text.WriteString(prefix)
	if out.OmittedComments > 0 {
		fmt.Fprintf(&text, "%d older comments omitted.\n", out.OmittedComments)
	}
	if out.Truncated {
		text.WriteString("One comment is truncated to fit the discussion budget.\n")
	}
	for i := len(entries) - 1; i >= 0; i-- {
		text.WriteString(entries[i])
	}
	out.Text = text.String()
	return out
}
