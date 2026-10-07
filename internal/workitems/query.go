package workitems

import (
	"fmt"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Parse parses pull request searches. Nothing is silently ignored.
func Parse(raw string) (Query, error) { return parse(raw, false) }

func ParseIssues(raw string) (Query, error) { return parse(raw, true) }

func parse(raw string, issues bool) (Query, error) {
	q := Query{Sort: "created-desc", Issues: issues}
	tokens, err := Tokens(raw)
	if err != nil {
		return q, err
	}
	canonical := []string{}
	people := map[string]bool{}
	hasState := false
	hasSort := false
	labels := labelParser{}
	for _, t := range tokens {
		if issues && strings.HasPrefix(t, "label:") {
			label, err := labels.parse(strings.TrimPrefix(t, "label:"))
			if err != nil {
				return q, err
			}
			q.Labels = append(q.Labels, label)
			canonical = append(canonical, "label:"+label.String())
			continue
		}
		quoted := strings.HasPrefix(t, "\"")
		if quoted {
			v, e := strconv.Unquote(t)
			if e != nil || v == "" {
				return q, fmt.Errorf("use nonempty double-quoted phrases")
			}
			q.Terms = append(q.Terms, v)
			canonical = append(canonical, strconv.Quote(v))
			continue
		}
		if t == "AND" || t == "OR" || t == "NOT" || strings.ContainsAny(t, "()|") || strings.HasPrefix(t, "-") {
			return q, fmt.Errorf("Boolean expressions and exclusions are not supported")
		}
		if strings.Contains(t, ":") {
			key, value, _ := strings.Cut(t, ":")
			if value == "" {
				return q, fmt.Errorf("%s requires a value", key)
			}
			if issues && (key == "draft" || key == "user-review-requested") {
				return q, fmt.Errorf("%s is only supported for pull requests", key)
			}
			switch key {
			case "in":
				if value != "title" {
					return q, fmt.Errorf("only in:title is supported")
				}
			case "is":
				if issues && value != "open" && value != "closed" && value != "all" {
					return q, fmt.Errorf("issues support is:open, is:closed, or is:all")
				}
				switch value {
				case "active", "wip", "all", "open", "closed", "merged", "unmerged":
				default:
					return q, fmt.Errorf("unsupported lifecycle %q", value)
				}
				q.States = append(q.States, value)
				hasState = true
			case "state":
				if q.Statuses != nil {
					return q, fmt.Errorf("state may only appear once")
				}
				q.Statuses = strings.Split(value, ",")
				for _, status := range q.Statuses {
					if issues && status != "open" && status != "closed" && status != "none" {
						return q, fmt.Errorf("unsupported issue state %q", status)
					}
					switch status {
					case "wip", "draft", "open", "closed", "merged":
					case "none":
						if len(q.Statuses) != 1 {
							return q, fmt.Errorf("state:none cannot be combined with other states")
						}
					default:
						return q, fmt.Errorf("unsupported state %q", status)
					}
				}
				hasState = true
			case "draft":
				if q.Draft != nil || (value != "true" && value != "false") {
					return q, fmt.Errorf("use draft:true or draft:false once")
				}
				v := value == "true"
				q.Draft = &v
			case "author", "assignee", "user-review-requested":
				if people[key] {
					return q, fmt.Errorf("%s may only appear once", key)
				}
				people[key] = true
				if value != "@me" {
					login := strings.TrimSuffix(strings.ToLower(value), "[bot]")
					if login == "" {
						return q, fmt.Errorf("%s requires a login or @me", key)
					}
					for _, r := range login {
						if !(unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-') {
							return q, fmt.Errorf("%s requires a login or @me", key)
						}
					}
				}
				value = strings.ToLower(value)
				switch key {
				case "author":
					q.Author = value
				case "assignee":
					q.Assignee = value
				default:
					q.Reviewer = value
				}
			case "sort":
				if hasSort {
					return q, fmt.Errorf("sort may only appear once")
				}
				hasSort = true
				switch value {
				case "created-desc", "created-asc", "updated-desc", "updated-asc":
					q.Sort = value
				default:
					return q, fmt.Errorf("unsupported sort %q", value)
				}
			default:
				return q, fmt.Errorf("unsupported qualifier %q", key)
			}
			canonical = append(canonical, key+":"+value)
			continue
		}
		if strings.HasPrefix(t, "#") || (len(tokens) == 1 && allDigits(t)) {
			n, e := strconv.Atoi(strings.TrimPrefix(t, "#"))
			if e != nil || n <= 0 || q.Number != 0 {
				return q, fmt.Errorf("use one positive issue or PR number")
			}
			q.Number = n
			canonical = append(canonical, "#"+strconv.Itoa(n))
			continue
		}
		q.Terms = append(q.Terms, t)
		canonical = append(canonical, t)
	}
	if !hasState {
		state := "active"
		if issues {
			state = "open"
		}
		q.States = []string{state}
		canonical = append(canonical, "is:"+state)
	}
	if !hasSort {
		canonical = append(canonical, "sort:created-desc")
	}
	q.Canonical = strings.Join(canonical, " ")
	return q, nil
}
func allDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
func Tokens(raw string) ([]string, error) {
	var result []string
	for i := 0; i < len(raw); {
		r, size := utf8.DecodeRuneInString(raw[i:])
		if unicode.IsSpace(r) {
			i += size
			continue
		}
		start := i
		if strings.HasPrefix(raw[i:], "label:") {
			end, err := labelTokenEnd(raw, i+6)
			if err != nil {
				return nil, err
			}
			result = append(result, raw[i:end])
			i = end
			continue
		}
		if raw[i] == '"' {
			i++
			closed := false
			for i < len(raw) {
				if raw[i] == '\\' {
					i += 2
					continue
				}
				if raw[i] == '"' {
					i++
					closed = true
					break
				}
				i++
			}
			if !closed || i > len(raw) {
				return nil, fmt.Errorf("unterminated quoted phrase")
			}
			if i < len(raw) {
				r, _ := utf8.DecodeRuneInString(raw[i:])
				if !unicode.IsSpace(r) {
					return nil, fmt.Errorf("separate search terms with spaces")
				}
			}
		} else {
			for i < len(raw) {
				r, size := utf8.DecodeRuneInString(raw[i:])
				if unicode.IsSpace(r) {
					break
				}
				if r == '"' {
					return nil, fmt.Errorf("quotes must surround a whole title phrase")
				}
				i += size
			}
		}
		result = append(result, raw[start:i])
	}
	return result, nil
}
