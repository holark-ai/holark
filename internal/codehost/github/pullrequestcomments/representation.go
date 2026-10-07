package pullrequestcomments

import (
	"strconv"
	"strings"

	"github.com/holark-ai/holark/internal/pullrequestcomments"
)

type quoteCandidate struct {
	externalID              string
	publishedBody           string
	effectiveParentExternal string
}

func normalizeIssueComments(comments []issueComment) []pullrequestcomments.RemoteComment {
	result := make([]pullrequestcomments.RemoteComment, 0, len(comments))
	earlier := make([]quoteCandidate, 0, len(comments))
	topLevel := make([]quoteCandidate, 0, len(comments))
	for _, comment := range comments {
		externalID := "conversation:" + strconv.FormatInt(comment.ID, 10)
		publishedBody, idempotencyKey, markerParent := parsePublicationMarker(comment.Body)
		body, parentExternalID := publishedBody, markerParent
		if parentExternalID == "" {
			if quote, response, hasQuote := parseLeadingMarkdownQuote(publishedBody); hasQuote {
				match, ambiguous := exactQuotedParent(quote, earlier)
				if match != "" {
					body, parentExternalID = response, match
				} else if !ambiguous {
					if match, ok := uniqueQuotedParent(quote, topLevel); ok {
						body, parentExternalID = response, match
					}
				}
			}
		}
		created, _ := parseGitHubTime(comment.CreatedAt)
		updated, _ := parseGitHubTime(comment.UpdatedAt)
		remote := pullrequestcomments.RemoteComment{
			ProviderIdentity: pullrequestcomments.ProviderIdentity{Provider: "github", ExternalID: externalID, Kind: "conversation"},
			ParentExternalID: parentExternalID,
			Body:             body,
			PublishedBody:    publishedBody,
			IdempotencyKey:   idempotencyKey,
			Scope:            pullrequestcomments.ScopePullRequest,
			Author:           providerAuthor(comment.User),
			CreatedAt:        created,
			UpdatedAt:        updated,
		}
		result = append(result, remote)

		effectiveParent := externalID
		if parentExternalID != "" {
			effectiveParent = parentExternalID
			for _, candidate := range earlier {
				if candidate.externalID == parentExternalID {
					effectiveParent = candidate.effectiveParentExternal
					break
				}
			}
		}
		candidate := quoteCandidate{externalID: externalID, publishedBody: publishedBody, effectiveParentExternal: effectiveParent}
		earlier = append(earlier, candidate)
		if parentExternalID == "" {
			topLevel = append(topLevel, candidate)
		}
	}
	return result
}

func renderBody(mutation pullrequestcomments.RemoteMutation) string {
	body := strings.TrimSpace(mutation.Body)
	if mutation.Scope == pullrequestcomments.ScopePullRequest && mutation.ParentCommentID != "" && strings.TrimSpace(mutation.ReplyContextBody) != "" {
		body = markdownQuote(mutation.ReplyContextBody) + "\n\n" + body
	}
	if mutation.IdempotencyKey != "" {
		body += "\n\n<!-- holark-comment:" + mutation.IdempotencyKey + " -->"
	}
	return body
}

func parsePublicationMarker(body string) (publishedBody, idempotencyKey, parentExternalID string) {
	index := strings.LastIndex(body, "<!-- holark-comment:")
	if index < 0 {
		return strings.TrimSpace(body), "", ""
	}
	end := strings.Index(body[index:], "-->")
	if end < 0 {
		return strings.TrimSpace(body), "", ""
	}
	marker := body[index+len("<!-- holark-comment:") : index+end]
	if parentIndex := strings.Index(marker, " parent:"); parentIndex >= 0 {
		parentExternalID = strings.TrimSpace(marker[parentIndex+len(" parent:"):])
		marker = marker[:parentIndex]
	}
	idempotencyKey = strings.TrimSpace(marker)
	publishedBody = strings.TrimSpace(body[:index] + body[index+end+3:])
	return publishedBody, idempotencyKey, parentExternalID
}

func markdownQuote(body string) string {
	lines := strings.Split(normalizeMarkdownText(body), "\n")
	for index, line := range lines {
		if line == "" {
			lines[index] = ">"
		} else {
			lines[index] = "> " + line
		}
	}
	return strings.Join(lines, "\n")
}

func parseLeadingMarkdownQuote(body string) (string, string, bool) {
	lines := strings.Split(normalizeMarkdownText(body), "\n")
	quoted := make([]string, 0, len(lines))
	index := 0
	for index < len(lines) {
		line, ok := removeMarkdownQuoteLevel(lines[index])
		if !ok {
			break
		}
		quoted = append(quoted, line)
		index++
	}
	if len(quoted) == 0 || index >= len(lines) || strings.TrimSpace(lines[index]) != "" {
		return "", "", false
	}
	for index < len(lines) && strings.TrimSpace(lines[index]) == "" {
		index++
	}
	quote := normalizeMarkdownText(strings.Join(quoted, "\n"))
	response := strings.TrimSpace(strings.Join(lines[index:], "\n"))
	if quote == "" || response == "" {
		return "", "", false
	}
	return quote, response, true
}

func removeMarkdownQuoteLevel(line string) (string, bool) {
	marker := 0
	for marker < len(line) && marker < 3 && line[marker] == ' ' {
		marker++
	}
	if marker >= len(line) || line[marker] != '>' {
		return "", false
	}
	remainder := line[marker+1:]
	if strings.HasPrefix(remainder, " ") || strings.HasPrefix(remainder, "\t") {
		remainder = remainder[1:]
	}
	return remainder, true
}

func uniqueQuotedParent(quote string, candidates []quoteCandidate) (string, bool) {
	quote = normalizeMarkdownText(quote)
	match := ""
	for _, candidate := range candidates {
		body := normalizeMarkdownText(candidate.publishedBody)
		if body != quote && !strings.Contains(body, quote) {
			continue
		}
		if match != "" {
			return "", false
		}
		match = candidate.externalID
	}
	return match, match != ""
}

func exactQuotedParent(quote string, candidates []quoteCandidate) (string, bool) {
	quote = normalizeExactQuoteText(quote)
	match := ""
	for _, candidate := range candidates {
		if normalizeExactQuoteText(candidate.publishedBody) != quote {
			continue
		}
		if match != "" {
			return "", true
		}
		match = candidate.effectiveParentExternal
	}
	return match, false
}

func normalizeExactQuoteText(body string) string {
	lines := strings.Split(normalizeMarkdownText(body), "\n")
	normalized := make([]string, 0, len(lines))
	previousBlankQuote := ""
	for _, line := range lines {
		blankQuote := normalizeBlankMarkdownQuoteLine(line)
		if blankQuote == "" {
			normalized = append(normalized, line)
			previousBlankQuote = ""
		} else if blankQuote != previousBlankQuote {
			normalized = append(normalized, blankQuote)
			previousBlankQuote = blankQuote
		}
	}
	return strings.Join(normalized, "\n")
}

func normalizeBlankMarkdownQuoteLine(line string) string {
	remainder := strings.TrimSpace(line)
	depth := 0
	for remainder != "" {
		if remainder[0] != '>' {
			return ""
		}
		depth++
		remainder = strings.TrimSpace(remainder[1:])
	}
	if depth == 0 {
		return ""
	}
	return strings.Repeat(">", depth)
}

func normalizeMarkdownText(body string) string {
	body = strings.ReplaceAll(body, "\r\n", "\n")
	body = strings.ReplaceAll(body, "\r", "\n")
	return strings.TrimSpace(body)
}
