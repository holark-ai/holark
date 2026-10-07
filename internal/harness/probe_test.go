package harness

import (
	"context"
	"testing"

	"github.com/holark-ai/holark/internal/harness/cliprobe"
	"github.com/holark-ai/holark/internal/protocol"
)

type discoveryOutputs map[string]string

func (outputs discoveryOutputs) Discover(_ context.Context, executable string) cliprobe.Observation {
	return cliprobe.Observation{Path: "/installed/" + executable, Output: outputs[executable]}
}

func TestRegistryClassifiesNativeVersionFormats(t *testing.T) {
	registry := RegistryWithProbeOptions(nil, ProbeOptions{Discovery: discoveryOutputs{
		"codex": "codex-cli 0.153.4\n", "claude": "2.1.220 (Claude Code)\n", "opencode": "1.18.25\n",
	}})
	want := map[protocol.HarnessType]string{protocol.HarnessCodex: "0.153.4", protocol.HarnessClaudeCode: "2.1.220", protocol.HarnessOpenCode: "1.18.25"}
	capabilities := registry.Probe(t.Context())
	if len(capabilities) != len(want) {
		t.Fatalf("capabilities = %+v", capabilities)
	}
	for _, capability := range capabilities {
		version := want[capability.Type]
		if !capability.Available || !capability.AutomatedWorkflows || capability.Version != version || capability.SupportStatus != "supported" || capability.Warning != "" || capability.UnavailableReason != "" {
			t.Fatalf("capability = %+v", capability)
		}
		if capability.LatestSupportedVersion != version || len(capability.SupportedRanges) != 1 || capability.SupportedRanges[0] != "="+version {
			t.Fatalf("support declaration lost: %+v", capability)
		}
	}
	registry = RegistryWithProbeOptions(nil, ProbeOptions{Discovery: discoveryOutputs{"opencode": "opencode v2.0.8\n"}})
	driver, _ := registry.Driver(protocol.HarnessOpenCode)
	capability := driver.Probe(t.Context())
	if !capability.Available || !capability.AutomatedWorkflows || capability.Version != "2.0.8" || capability.SupportStatus != "unsupported" || capability.Warning == "" || capability.UnavailableReason != "" {
		t.Fatalf("prefixed OpenCode version = %+v", capability)
	}
}
