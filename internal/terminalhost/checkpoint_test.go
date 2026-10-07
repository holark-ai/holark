package terminalhost

import (
	"encoding/json"
	"testing"

	"github.com/holark-ai/holark/internal/terminals"
)

func TestEmptyCheckpointRestoreSerializesAsEmptyBase64(t *testing.T) {
	// JSON null was decoded by browsers into bytes ending in a phantom "e".
	restore := terminals.TerminalRestore{
		Checkpoint: cloneCheckpoint(terminals.NewCheckpoint(
			"terminal-empty-checkpoint",
			0,
			terminals.Dimensions{Columns: 80, Rows: 24},
			nil,
		)),
	}

	encoded, err := json.Marshal(restore)
	if err != nil {
		t.Fatal(err)
	}
	var wire struct {
		Checkpoint struct {
			ReplayPayload *string `json:"replay_payload"`
		} `json:"checkpoint"`
	}
	if err := json.Unmarshal(encoded, &wire); err != nil {
		t.Fatal(err)
	}
	if wire.Checkpoint.ReplayPayload == nil || *wire.Checkpoint.ReplayPayload != "" {
		t.Fatalf("empty replay payload = %s, want empty Base64 string", encoded)
	}
}
