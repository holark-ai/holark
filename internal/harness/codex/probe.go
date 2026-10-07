package codex

import (
	"context"
	"strings"

	"github.com/holark-ai/holark/internal/harness/cliprobe"
)

// LatestSupportedVersion is validated for this Holark release, not the latest
// upstream release. Support is informational and never gates execution.
const LatestSupportedVersion = "0.153.4"

// OfficialSupport deliberately declares only the owner-approved validated release.
func OfficialSupport() cliprobe.SupportPolicy {
	return cliprobe.SupportPolicy{Versions: []string{"0.153.4"}, Latest: LatestSupportedVersion}
}

type ProbeResult = cliprobe.Result

func Probe(ctx context.Context) ProbeResult {
	return ProbeExecutable(ctx, "codex")
}

func ProbeExecutable(ctx context.Context, executable string) ProbeResult {
	return ProbeWithOptions(ctx, executable, cliprobe.Options{})
}

func ProbeWithOptions(ctx context.Context, executable string, options cliprobe.Options) ProbeResult {
	return cliprobe.Probe(ctx, executable, "Codex", "Codex CLI was not found", OfficialSupport(), normalizeVersion, options)
}

func normalizeVersion(output string) string {
	if !strings.HasPrefix(output, "codex-cli ") {
		return ""
	}
	return strings.TrimPrefix(output, "codex-cli ")
}
