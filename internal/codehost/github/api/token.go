package api

import (
	"context"
	"strings"
)

// Token reads the active GitHub CLI credential for server-side attachment
// requests. It is never sent to the frontend or persisted by Holark.
func (client *CLIClient) Token(ctx context.Context) (string, error) {
	output, err := client.run(ctx, "auth", "token", "--hostname", "github.com")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(output)), nil
}
