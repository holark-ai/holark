package harness

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/holark-ai/holark/internal/harness/codex"
	"github.com/holark-ai/holark/internal/protocol"
)

// Discovery is opt-in because it reads the developer's installed CLI config.
// No inference calls or fake agent executables are involved.
func TestInstalledModelDiscovery(t *testing.T) {
	if os.Getenv("HOLARK_MODEL_DISCOVERY") != "1" {
		t.Skip("set HOLARK_MODEL_DISCOVERY=1 to query installed CLIs")
	}
	manager := codex.NewManager()
	t.Cleanup(func() {
		if err := manager.Close(); err != nil {
			t.Error(err)
		}
	})
	registry := RegistryWithCodex(manager)
	repository, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	for _, agent := range []protocol.HarnessType{protocol.HarnessCodex, protocol.HarnessClaudeCode, protocol.HarnessOpenCode} {
		t.Run(string(agent), func(t *testing.T) {
			models, err := registry.Models(t.Context(), agent, repository)
			if err != nil {
				t.Fatal(err)
			}
			if len(models) == 0 {
				t.Fatal("no models discovered")
			}
			t.Logf("discovered %d models", len(models))
		})
	}
}
