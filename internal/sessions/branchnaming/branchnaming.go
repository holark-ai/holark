package branchnaming

import (
	"context"
	"errors"
	"regexp"
	"strings"

	"github.com/holark-ai/holark/internal/protocol"
)

type Input struct {
	SessionID      string
	Project        protocol.Project
	Title          string
	Prompt         string
	PromptTemplate string
	GenerateTitle  bool
}

type Proposal struct {
	Slug  string `json:"slug"`
	Title string `json:"title"`
}

type Namer interface {
	Name(context.Context, Input) (Proposal, error)
}

var (
	ErrInvalidSlug   = errors.New("invalid branch slug")
	ErrInvalidBranch = errors.New("invalid proposed upstream branch")
	slugPattern      = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,46}[a-z0-9]$`)
	branchPattern    = regexp.MustCompile(`^holark/([a-z0-9][a-z0-9-]{1,46}[a-z0-9])$`)
)

func ComposeBranch(slug string) (string, error) {
	if !ValidSlug(slug) {
		return "", ErrInvalidSlug
	}
	branch := "holark/" + slug
	if !ValidBranch(branch) {
		return "", ErrInvalidBranch
	}
	return branch, nil
}

func ValidSlug(slug string) bool {
	return slugPattern.MatchString(slug) && !strings.Contains(slug, "--")
}

func ValidBranch(branch string) bool {
	matches := branchPattern.FindStringSubmatch(branch)
	if len(matches) != 2 {
		return false
	}
	return ValidSlug(matches[1])
}
