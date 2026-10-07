package opencode

import (
	"strings"

	"github.com/holark-ai/holark/internal/harness/cliprobe"
)

// opencodeV2Boundary is the first OpenCode major release using the v2 plugin
// API (Plugin.define with setup(ctx), ctx.event.subscribe, event.data
// payloads, the mini subcommand, and opencode.jsonc/cli.json plugin keys).
// Dispatch is per operation: only the assets and argv that changed select on
// this boundary, and unparseable versions fall back to the validated v1 path.
const opencodeV2Boundary uint64 = 2

// IsV2Version reports whether raw names an OpenCode release at or above the
// v2 boundary. The output is normalized with the existing normalizeVersion
// wrapper before the strict-semver check; anything unparseable returns false
// so unknown installations keep the validated v1 behavior. Pure, no I/O.
func IsV2Version(raw string) bool {
	return cliprobe.AtLeastVersion(normalizeVersion(strings.TrimSpace(raw)), opencodeV2Boundary, 0, 0)
}
