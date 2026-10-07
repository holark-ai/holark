package sqliteadapter

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/holark-ai/holark/internal/agentsettings"
	"github.com/holark-ai/holark/internal/database"
	"github.com/holark-ai/holark/internal/protocol"
)

type Store struct{ db *sql.DB }

func New(ctx context.Context, db *sql.DB) (*Store, error) {
	if err := database.CreateSchema(ctx, db, `
create table if not exists agent_settings (
  key text primary key,
  value text not null
);`); err != nil {
		return nil, err
	}
	return &Store{db: db}, nil
}

func (s *Store) LoadDefault(ctx context.Context, workflow agentsettings.Workflow) (protocol.HarnessType, bool, error) {
	value, err := s.LoadPreference(ctx, workflow)
	return value.HarnessType, value.Explicit, err
}

func (s *Store) LoadPreference(ctx context.Context, workflow agentsettings.Workflow) (agentsettings.Default, error) {
	var value agentsettings.Default
	err := s.db.QueryRowContext(ctx, `select value, coalesce((select value from agent_settings where key = ?), ''), coalesce((select value from agent_settings where key = ?), '') from agent_settings where key = ?`, modelKey(workflow), permissionsKey(workflow), settingKey(workflow)).Scan(&value.HarnessType, &value.Model, &value.Permissions)
	if errors.Is(err, sql.ErrNoRows) {
		return value, nil
	}
	value.Explicit = err == nil
	return value, err
}

func (s *Store) SaveDefault(ctx context.Context, workflow agentsettings.Workflow, value protocol.HarnessType) error {
	return s.SavePreference(ctx, workflow, agentsettings.Default{HarnessType: value})
}

func (s *Store) SavePreference(ctx context.Context, workflow agentsettings.Workflow, value agentsettings.Default) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for key, val := range map[string]string{settingKey(workflow): string(value.HarnessType), modelKey(workflow): value.Model, permissionsKey(workflow): value.Permissions} {
		if _, err = tx.ExecContext(ctx, `insert into agent_settings(key,value) values(?,?) on conflict(key) do update set value=excluded.value`, key, val); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) DeleteDefault(ctx context.Context, workflow agentsettings.Workflow) error {
	_, err := s.db.ExecContext(ctx, `delete from agent_settings where key in (?, ?, ?)`, settingKey(workflow), modelKey(workflow), permissionsKey(workflow))
	return err
}

func permissionsKey(workflow agentsettings.Workflow) string {
	return settingKey(workflow) + ".permissions"
}

func modelKey(workflow agentsettings.Workflow) string { return settingKey(workflow) + ".model" }

func settingKey(workflow agentsettings.Workflow) string {
	if workflow == agentsettings.WorkflowDefault {
		return "default_agent_harness"
	}
	return "default_agent_harness." + string(workflow)
}

var _ agentsettings.Store = (*Store)(nil)

// All commands share one value so readers always observe an atomic snapshot.
func (s *Store) LoadCommands(ctx context.Context) (agentsettings.Commands, error) {
	var raw string
	err := s.db.QueryRowContext(ctx, `select value from agent_settings where key = 'launch_commands'`).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return agentsettings.Commands{}, nil
	}
	if err != nil {
		return nil, err
	}
	var commands agentsettings.Commands
	if err := json.Unmarshal([]byte(raw), &commands); err != nil {
		return nil, err
	}
	return commands, nil
}

func (s *Store) SaveCommands(ctx context.Context, commands agentsettings.Commands) error {
	raw, err := json.Marshal(commands)
	if err != nil {
		return err
	}
	// Merge in the write statement so other repositories' updates cannot be lost
	// between reading the existing commands and saving the changed fields.
	_, err = s.db.ExecContext(ctx, `insert into agent_settings(key,value) values('launch_commands',?) on conflict(key) do update set value=json_patch(agent_settings.value,excluded.value)`, string(raw))
	return err
}
