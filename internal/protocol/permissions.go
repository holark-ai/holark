package protocol

// ValidPermissions accepts a harness-native permission preset. Empty uses
// Holark's harness default; inherit leaves permission configuration to the CLI.
func ValidPermissions(harness HarnessType, permissions string) bool {
	if permissions == "" || permissions == "inherit" {
		return true
	}
	switch harness {
	case HarnessCodex:
		return permissions == "workspace-write" || permissions == "read-only" || permissions == "full-access"
	case HarnessClaudeCode:
		return permissions == "default" || permissions == "acceptEdits" || permissions == "plan" || permissions == "dontAsk" || permissions == "bypassPermissions"
	case HarnessOpenCode:
		return permissions == "ask" || permissions == "allow" || permissions == "deny"
	}
	return false
}
