package workitems

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// LabelExpression is a label name or a group of conditions sharing an operator.
// Operators are deliberately scoped to labels, not lifecycle or people filters.
type LabelExpression struct {
	Name     string
	Operator string
	Children []*LabelExpression
}

func (e *LabelExpression) String() string {
	if e.Operator == "" {
		value, _ := json.Marshal(e.Name)
		return string(value)
	}
	parts := make([]string, len(e.Children))
	for i, child := range e.Children {
		parts[i] = child.String()
	}
	return "(" + strings.Join(parts, " "+e.Operator+" ") + ")"
}

// parse keeps condition and length totals across every label qualifier in a query.
func (p *labelParser) parse(raw string) (*LabelExpression, error) {
	p.rawLength += len(raw)
	if p.rawLength > 8192 {
		return nil, fmt.Errorf("label expression is too long")
	}
	p.raw, p.pos = raw, 0
	expr, err := p.expression(0, false)
	if err != nil {
		return nil, err
	}
	p.space()
	if p.pos != len(raw) {
		return nil, fmt.Errorf("expected AND or OR between labels")
	}
	if labelDepth(expr) > 8 {
		return nil, fmt.Errorf("label groups may nest at most eight levels")
	}
	p.canonicalLength += len(expr.String())
	if p.canonicalLength > 8192 {
		return nil, fmt.Errorf("label expression is too long")
	}
	return expr, nil
}

func labelDepth(e *LabelExpression) int {
	if e.Operator == "" {
		return 0
	}
	depth := 0
	for _, child := range e.Children {
		if d := labelDepth(child); d > depth {
			depth = d
		}
	}
	return depth + 1
}

type labelParser struct {
	raw                        string
	pos, count                 int
	rawLength, canonicalLength int
}

func (p *labelParser) space() {
	for p.pos < len(p.raw) {
		r, size := utf8.DecodeRuneInString(p.raw[p.pos:])
		if !unicode.IsSpace(r) {
			break
		}
		p.pos += size
	}
}
func (p *labelParser) operator(op string) bool {
	p.space()
	if !strings.HasPrefix(p.raw[p.pos:], op) {
		return false
	}
	end := p.pos + len(op)
	if end < len(p.raw) {
		r, _ := utf8.DecodeRuneInString(p.raw[end:])
		if !unicode.IsSpace(r) && r != '(' && r != ')' {
			return false
		}
	}
	p.pos = end
	return true
}
func (p *labelParser) expression(depth int, andOnly bool) (*LabelExpression, error) {
	if depth > 8 {
		return nil, fmt.Errorf("label groups may nest at most eight levels")
	}
	var left *LabelExpression
	var err error
	if andOnly {
		left, err = p.atom(depth)
	} else {
		left, err = p.expression(depth, true)
	}
	if err != nil {
		return nil, err
	}
	op := "OR"
	if andOnly {
		op = "AND"
	}
	children := []*LabelExpression{left}
	for p.operator(op) {
		var right *LabelExpression
		if andOnly {
			right, err = p.atom(depth)
		} else {
			right, err = p.expression(depth, true)
		}
		if err != nil {
			return nil, err
		}
		children = append(children, right)
	}
	if len(children) == 1 {
		return left, nil
	}
	return &LabelExpression{Operator: op, Children: children}, nil
}
func (p *labelParser) atom(depth int) (*LabelExpression, error) {
	p.space()
	if p.pos == len(p.raw) {
		return nil, fmt.Errorf("expected a label name")
	}
	if p.raw[p.pos] == '(' {
		p.pos++
		result, err := p.expression(depth+1, false)
		if err != nil {
			return nil, err
		}
		p.space()
		if p.pos == len(p.raw) || p.raw[p.pos] != ')' {
			return nil, fmt.Errorf("unclosed label group")
		}
		p.pos++
		return result, nil
	}
	start := p.pos
	name := ""
	if p.raw[p.pos] == '"' {
		p.pos++
		for p.pos < len(p.raw) && p.raw[p.pos] != '"' {
			if p.raw[p.pos] == '\\' {
				p.pos++
			}
			p.pos++
		}
		if p.pos >= len(p.raw) {
			return nil, fmt.Errorf("unterminated quoted label")
		}
		p.pos++
		if err := json.Unmarshal([]byte(p.raw[start:p.pos]), &name); err != nil {
			return nil, fmt.Errorf("invalid quoted label")
		}
	} else {
		for p.pos < len(p.raw) {
			r, size := utf8.DecodeRuneInString(p.raw[p.pos:])
			if unicode.IsSpace(r) || strings.ContainsRune("()\"", r) {
				break
			}
			p.pos += size
		}
		name = p.raw[start:p.pos]
		if name == "AND" || name == "OR" || name == "NOT" {
			return nil, fmt.Errorf("quote label names that are operators")
		}
	}
	if strings.TrimSpace(name) == "" {
		return nil, fmt.Errorf("expected a nonempty label name")
	}
	p.count++
	if p.count > 100 {
		return nil, fmt.Errorf("use at most 100 label conditions")
	}
	return &LabelExpression{Name: name}, nil
}

// labelTokenEnd keeps a quoted label or an entire group in one search token.
func labelTokenEnd(raw string, start int) (int, error) {
	depth, quoted, escaped := 0, false, false
	for i := start; i < len(raw); {
		r, size := utf8.DecodeRuneInString(raw[i:])
		if quoted {
			if escaped {
				escaped = false
			} else if r == '\\' {
				escaped = true
			} else if r == '"' {
				quoted = false
			}
		} else {
			if unicode.IsSpace(r) && depth == 0 {
				return i, nil
			}
			switch r {
			case '"':
				quoted = true
			case '(':
				depth++
				if depth > 8 {
					return 0, fmt.Errorf("label groups may nest at most eight levels")
				}
			case ')':
				depth--
				if depth < 0 {
					return 0, fmt.Errorf("unexpected closing label parenthesis")
				}
			}
		}
		i += size
	}
	if quoted || depth != 0 {
		return 0, fmt.Errorf("unclosed label group or quoted label")
	}
	return len(raw), nil
}
