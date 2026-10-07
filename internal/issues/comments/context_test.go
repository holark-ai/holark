package comments

import (
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestValidateBodyPreservesMarkdownAndCountsCharacters(t *testing.T) {
	for _, body := range []string{"", " \n\t", strings.Repeat("界", 65537)} {
		if ValidateBody(body) == nil {
			t.Fatal("invalid body accepted")
		}
	}
	for _, body := range []string{"> existing\n\nbody", strings.Repeat("界", 65536)} {
		if err := ValidateBody(body); err != nil {
			t.Fatal(err)
		}
	}
}
func TestContextRecentChronologicalAndBounded(t *testing.T) {
	d := Discussion{}
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 60; i++ {
		d.Comments = append(d.Comments, Comment{ID: fmt.Sprintf("c%02d", i), Body: fmt.Sprintf("body%02d", i), CreatedAt: at.Add(time.Duration(i) * time.Minute), URL: "https://github.com/o/r/issues/1#issuecomment-1"})
	}
	c := BuildContext(d)
	if c.IncludedComments != 50 || c.OmittedComments != 10 || strings.Contains(c.Text, "body09") || strings.Index(c.Text, "body10") > strings.Index(c.Text, "body59") || !strings.Contains(c.Text, "Unknown author") {
		t.Fatalf("context: %+v", c)
	}
	d.Comments[59].Body = "latest " + strings.Repeat("界", 30000)
	c = BuildContext(d)
	if !c.Truncated || c.IncludedComments != 1 || c.OmittedComments != 59 || !strings.Contains(c.Text, "latest") || utf8.RuneCountInString(c.Text) > ContextCharacterLimit {
		t.Fatalf("bounded context counts=%d/%d size=%d", c.IncludedComments, c.OmittedComments, utf8.RuneCountInString(c.Text))
	}
	if !strings.Contains(BuildContext(Discussion{}).Text, "No cached comments") {
		t.Fatal("missing empty context")
	}
}
