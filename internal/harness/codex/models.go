package codex

import (
	"context"
	"errors"

	"github.com/holark-ai/holark/internal/protocol"
)

// Models uses the application's existing app-server, without starting a thread.
func (m *Manager) Models(ctx context.Context) ([]protocol.ModelChoice, error) {
	m.mu.Lock()
	err := m.startLocked(ctx)
	url, token := m.url, m.token
	m.mu.Unlock()
	if err != nil {
		return nil, err
	}
	c, err := connectRPC(ctx, url, token)
	if err != nil {
		return nil, err
	}
	defer c.close()
	if err = c.initialize(ctx); err != nil {
		return nil, err
	}
	models := []protocol.ModelChoice{}
	var cursor *string
	seen := map[string]bool{}
	for {
		var page struct {
			Data []struct {
				Model       string `json:"model"`
				DisplayName string `json:"displayName"`
				Hidden      bool   `json:"hidden"`
			} `json:"data"`
			NextCursor *string `json:"nextCursor"`
		}
		if err := c.call(ctx, "model/list", map[string]any{"limit": 100, "includeHidden": false, "cursor": cursor}, &page); err != nil {
			return nil, err
		}
		for _, model := range page.Data {
			if !model.Hidden {
				models = append(models, protocol.ModelChoice{ID: model.Model, Label: model.DisplayName})
			}
		}
		if page.NextCursor == nil || *page.NextCursor == "" {
			return models, nil
		}
		if seen[*page.NextCursor] {
			return nil, errors.New("Codex returned a repeated model-list cursor")
		}
		seen[*page.NextCursor] = true
		cursor = page.NextCursor
	}
}
