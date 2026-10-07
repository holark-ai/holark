// Package terminals owns the transport-independent live terminal contract.
// Durable ownership belongs to harness and manual-terminal product records;
// PTYs, checkpoints, notification tails, and process state belong to the host.
package terminals

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
)

type TerminalID string

const ProtocolVersion = 12

type OwnerKind string

const (
	OwnerHarness OwnerKind = "harness"
	OwnerManual  OwnerKind = "manual"
)

type Dimensions struct {
	Columns int `json:"columns"`
	Rows    int `json:"rows"`
}

func NewID() (TerminalID, error) {
	bytes := make([]byte, 16)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return TerminalID("terminal-" + hex.EncodeToString(bytes)), nil
}

func (id TerminalID) Valid() bool {
	value := string(id)
	if value == "" || len(value) > 128 {
		return false
	}
	for _, character := range value {
		if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') || character == '-' || character == '_' {
			continue
		}
		return false
	}
	return true
}

func (dimensions Dimensions) Validate() error {
	if dimensions.Columns < 20 || dimensions.Columns > 500 || dimensions.Rows < 5 || dimensions.Rows > 300 {
		return errors.New("terminal size is out of range")
	}
	return nil
}
