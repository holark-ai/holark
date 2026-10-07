// Package manualterminals owns the durable product record for an open manual
// terminal tab. A record exists exactly while the tab is open.
package manualterminals

import (
	"errors"
	"time"

	"github.com/holark-ai/holark/internal/terminals"
)

const MaximumPerSession = 100

var (
	ErrLimit   = errors.New("manual terminal limit reached")
	ErrMissing = errors.New("manual terminal not found")
)

type Record struct {
	ID         string               `json:"id"`
	TerminalID terminals.TerminalID `json:"terminal_id"`
	SessionID  string               `json:"session_id"`
	Title      string               `json:"title"`
	CWD        string               `json:"cwd"`
	TabOrder   int                  `json:"tab_order,omitempty"`
	CreatedAt  time.Time            `json:"created_at"`
	UpdatedAt  time.Time            `json:"updated_at"`
}

type Terminal = Record

func cloneRecord(record Record) Record { return record }
