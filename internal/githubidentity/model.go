// Package githubidentity owns project-scoped GitHub collaborator membership.
package githubidentity

import (
	"errors"
	"time"
)

type Member struct {
	ID         string `json:"id"`
	Login      string `json:"login"`
	AvatarURL  string `json:"avatar_url,omitempty"`
	ProfileURL string `json:"profile_url,omitempty"`
	Permission string `json:"permission"`
	IsMe       bool   `json:"is_me"`
}

type MemberResolution struct {
	Members    []Member `json:"members"`
	MissingIDs []string `json:"missing_ids"`
}

type SourceMember struct {
	NodeID     string
	Login      string
	Name       string
	AvatarURL  string
	ProfileURL string
	Permission string
}

type Profile struct {
	Login      string `json:"login"`
	Name       string `json:"name,omitempty"`
	AvatarURL  string `json:"avatar_url,omitempty"`
	ProfileURL string `json:"profile_url,omitempty"`
}

type MemberSnapshot struct {
	AuthenticatedNodeID string
	Members             []SourceMember
}

type StoredMember struct {
	Member
	NodeID     string
	LastSeenAt time.Time
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

var (
	ErrUnsupportedRepository = errors.New("repository is not a supported GitHub repository")
	ErrGitHubUnavailable     = errors.New("GitHub CLI is unavailable")
	ErrGitHubFailed          = errors.New("GitHub request failed")
	ErrMalformedSnapshot     = errors.New("GitHub member snapshot is malformed")
	ErrMemberUnresolved      = errors.New("project member identity could not be resolved")
)
