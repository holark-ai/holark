package opencode

import (
	"context"
	"strings"

	"github.com/holark-ai/holark/internal/harness/cliprobe"
)

// LatestSupportedVersion is validated for this Holark release, not the latest
// upstream release. Support is informational and never gates execution.
const LatestSupportedVersion = "1.18.25"

// OfficialSupport deliberately declares only the owner-approved validated release.
func OfficialSupport() cliprobe.SupportPolicy {
	return cliprobe.SupportPolicy{Versions: []string{"1.18.25"}, Latest: LatestSupportedVersion}
}

type ProbeResult = cliprobe.Result

func Probe(ctx context.Context) ProbeResult {
	return ProbeExecutable(ctx, "opencode")
}

func ProbeExecutable(ctx context.Context, executable string) ProbeResult {
	return ProbeWithOptions(ctx, executable, cliprobe.Options{})
}

func ProbeWithOptions(ctx context.Context, executable string, options cliprobe.Options) ProbeResult {
	return cliprobe.Probe(ctx, executable, "OpenCode", "OpenCode CLI was not found; configure the node PATH", OfficialSupport(), normalizeVersion, options)
}

func normalizeVersion(output string) string {
	return strings.TrimPrefix(output, "opencode v")
}
