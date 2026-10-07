package sqliteadapter

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/holark-ai/holark/internal/holons"
)

type Store struct{ db *sql.DB }

func New(ctx context.Context, db *sql.DB) (*Store, error) {
	s := &Store{db: db}
	if err := s.schema(ctx); err != nil {
		return nil, err
	}
	return s, nil
}
func (s *Store) schema(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `
create table if not exists holons (
 id text primary key, title text not null, prompt text not null, kind text not null, status text not null,
 base_branch text not null default '', base_commit text not null, work_session_start_commit text not null default '', worktree_branch text not null default '', worktree_path text not null default '',
 synchronized_target_commit text not null default '', rebase_attempt text not null default 'null', upstream_branch text not null default '', upstream_head_commit text not null default '', read_only integer not null default 0,
 reason text not null default '', exit_code integer, issue_id text not null default '', pull_request_id text not null default '', published integer not null default 0,
 last_selected_tab_id text not null default '', end_requested integer not null default 0,
 created_at text not null, started_at text, finished_at text, archived_at text);
create index if not exists holons_issue on holons(issue_id); create index if not exists holons_pr on holons(pull_request_id);
create table if not exists holon_agent_sessions (
 id text primary key, holon_id text not null references holons(id) on delete cascade, terminal_id text not null default '', agent_type text not null, model text not null default '', permissions text not null default '',
 title text not null, prompt text not null, status text not null, reason text not null default '', exit_code integer, resume_target text not null default '', rollout_path text not null default '', activity text not null default 'starting', input_state text not null default '', observability_status text not null default '', observability_message text not null default '', context_tokens integer, commit_prompt_state text not null default '', commit_prompt_change_hash text not null default '', commit_start_head text not null default '', tab_order integer not null,
 created_at text not null, updated_at text not null, started_at text, finished_at text, closed_at text);
create table if not exists holon_manual_terminals (
 id text primary key, holon_id text not null references holons(id) on delete cascade, terminal_id text not null, title text not null, cwd text not null, tab_order integer not null,
 created_at text not null, updated_at text not null, closed_at text);
create table if not exists holon_ides (
 id text primary key, holon_id text not null references holons(id) on delete cascade, provider text not null, state text not null,
 tab_order integer not null, desired_open integer not null, reason text not null default '', created_at text not null, updated_at text not null,
 ready_at text, closed_at text);`)
	if err != nil {
		return err
	}
	return nil
}
func ts(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }
func nts(t *time.Time) any {
	if t == nil {
		return nil
	}
	return ts(*t)
}
func parse(v sql.NullString) *time.Time {
	if !v.Valid {
		return nil
	}
	t, e := time.Parse(time.RFC3339Nano, v.String)
	if e != nil {
		return nil
	}
	return &t
}
func insertHolon(ctx context.Context, q interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}, h holons.Holon) error {
	h.BaseBranch = holons.CanonicalBaseBranch(h.BaseBranch)
	_, e := q.ExecContext(ctx, `insert into holons(id,title,prompt,kind,status,base_branch,base_commit,work_session_start_commit,worktree_branch,worktree_path,upstream_branch,upstream_head_commit,synchronized_target_commit,rebase_attempt,read_only,reason,exit_code,issue_id,pull_request_id,published,created_at,started_at,finished_at,archived_at,last_selected_tab_id,end_requested) values(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, h.ID, h.Title, h.Prompt, h.Kind, h.Status, h.BaseBranch, h.BaseCommit, h.WorkSessionStartCommit, h.WorktreeBranch, h.WorktreePath, h.UpstreamBranch, h.UpstreamHeadCommit, h.SynchronizedTargetCommit, rebaseJSON(h.RebaseAttempt), h.ReadOnly, h.Reason, h.ExitCode, h.IssueID, h.PullRequestID, h.Published, ts(h.CreatedAt), nts(h.StartedAt), nts(h.FinishedAt), nts(h.ArchivedAt), h.LastSelectedTabID, h.EndRequested)
	return e
}
func insertAgent(ctx context.Context, q interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}, a holons.AgentSession) error {
	state := ""
	if a.CommitPrompt != nil {
		state = a.CommitPrompt.State
	}
	_, e := q.ExecContext(ctx, `insert into holon_agent_sessions(id,holon_id,terminal_id,agent_type,model,permissions,title,prompt,status,reason,exit_code,resume_target,rollout_path,activity,input_state,observability_status,observability_message,context_tokens,commit_prompt_state,commit_prompt_change_hash,commit_start_head,tab_order,created_at,updated_at,started_at,finished_at,closed_at) values(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, a.ID, a.HolonID, a.TerminalID, a.AgentType, a.Model, a.Permissions, a.Title, a.Prompt, a.Status, a.Reason, a.ExitCode, a.ResumeTarget, a.RolloutPath, a.Activity, a.InputState, a.ObservabilityStatus, a.ObservabilityMessage, a.ContextTokens, state, a.CommitPromptChangeHash, a.CommitStartHead, a.TabOrder, ts(a.CreatedAt), ts(a.UpdatedAt), nts(a.StartedAt), nts(a.FinishedAt), nts(a.ClosedAt))
	return e
}
func (s *Store) Create(ctx context.Context, h holons.Holon) error {
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	if e = insertHolon(ctx, tx, h); e != nil {
		return e
	}
	for _, a := range h.AgentSessions {
		if e = insertAgent(ctx, tx, a); e != nil {
			return e
		}
	}
	return tx.Commit()
}
func scanHolon(row interface{ Scan(...any) error }) (holons.Holon, error) {
	var h holons.Holon
	var created, attempt string
	var st, ft, ct sql.NullString
	e := row.Scan(&h.ID, &h.Title, &h.Prompt, &h.Kind, &h.Status, &h.BaseBranch, &h.BaseCommit, &h.WorkSessionStartCommit, &h.WorktreeBranch, &h.WorktreePath, &h.UpstreamBranch, &h.UpstreamHeadCommit, &h.SynchronizedTargetCommit, &attempt, &h.ReadOnly, &h.Reason, &h.ExitCode, &h.IssueID, &h.PullRequestID, &h.Published, &created, &st, &ft, &ct, &h.LastSelectedTabID, &h.EndRequested)
	if errors.Is(e, sql.ErrNoRows) {
		return h, holons.ErrNotFound
	}
	if e != nil {
		return h, e
	}
	if e = json.Unmarshal([]byte(attempt), &h.RebaseAttempt); e != nil {
		return h, e
	}
	h.BaseBranch = holons.CanonicalBaseBranch(h.BaseBranch)
	h.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
	h.StartedAt = parse(st)
	h.FinishedAt = parse(ft)
	h.ArchivedAt = parse(ct)
	return h, nil
}

const selectHolon = `select id,title,prompt,kind,status,base_branch,base_commit,work_session_start_commit,worktree_branch,worktree_path,upstream_branch,upstream_head_commit,synchronized_target_commit,rebase_attempt,read_only,reason,exit_code,issue_id,pull_request_id,published,created_at,started_at,finished_at,archived_at,last_selected_tab_id,end_requested from holons`

func (s *Store) Get(ctx context.Context, id string) (holons.Holon, error) {
	h, e := scanHolon(s.db.QueryRowContext(ctx, selectHolon+` where id=?`, id))
	if e != nil {
		return h, e
	}
	e = s.children(ctx, &h)
	return h, e
}
func (s *Store) List(ctx context.Context) ([]holons.Holon, error) {
	rows, e := s.db.QueryContext(ctx, selectHolon+` order by created_at desc`)
	if e != nil {
		return nil, e
	}
	out := make([]holons.Holon, 0)
	for rows.Next() {
		h, e := scanHolon(rows)
		if e != nil {
			rows.Close()
			return nil, e
		}
		out = append(out, h)
	}
	if e = rows.Err(); e != nil {
		rows.Close()
		return nil, e
	}
	if e = rows.Close(); e != nil {
		return nil, e
	}
	for index := range out {
		if e = s.children(ctx, &out[index]); e != nil {
			return nil, e
		}
	}
	return out, nil
}
func (s *Store) children(ctx context.Context, h *holons.Holon) error {
	rows, e := s.db.QueryContext(ctx, `select id,holon_id,terminal_id,agent_type,model,permissions,title,prompt,status,reason,exit_code,resume_target,rollout_path,activity,input_state,observability_status,observability_message,context_tokens,commit_prompt_state,commit_prompt_change_hash,commit_start_head,tab_order,created_at,updated_at,started_at,finished_at,closed_at from holon_agent_sessions where holon_id=? order by tab_order,created_at`, h.ID)
	if e != nil {
		return e
	}
	defer rows.Close()
	for rows.Next() {
		var a holons.AgentSession
		var c, u string
		var st, ft, ct sql.NullString
		var commitState string
		if e = rows.Scan(&a.ID, &a.HolonID, &a.TerminalID, &a.AgentType, &a.Model, &a.Permissions, &a.Title, &a.Prompt, &a.Status, &a.Reason, &a.ExitCode, &a.ResumeTarget, &a.RolloutPath, &a.Activity, &a.InputState, &a.ObservabilityStatus, &a.ObservabilityMessage, &a.ContextTokens, &commitState, &a.CommitPromptChangeHash, &a.CommitStartHead, &a.TabOrder, &c, &u, &st, &ft, &ct); e != nil {
			return e
		}
		a.CreatedAt, _ = time.Parse(time.RFC3339Nano, c)
		a.UpdatedAt, _ = time.Parse(time.RFC3339Nano, u)
		a.StartedAt = parse(st)
		a.FinishedAt = parse(ft)
		a.ClosedAt = parse(ct)
		if commitState != "" {
			a.CommitPrompt = &holons.CommitPrompt{State: commitState}
		}
		h.AgentSessions = append(h.AgentSessions, a)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	terminals, err := s.db.QueryContext(ctx, `select id,holon_id,terminal_id,title,cwd,tab_order,created_at,updated_at,closed_at from holon_manual_terminals where holon_id=? order by tab_order,created_at`, h.ID)
	if err != nil {
		return err
	}
	defer terminals.Close()
	for terminals.Next() {
		var t holons.ManualTerminal
		var c, u string
		var closed sql.NullString
		if err = terminals.Scan(&t.ID, &t.HolonID, &t.TerminalID, &t.Title, &t.CWD, &t.TabOrder, &c, &u, &closed); err != nil {
			return err
		}
		t.CreatedAt, _ = time.Parse(time.RFC3339Nano, c)
		t.UpdatedAt, _ = time.Parse(time.RFC3339Nano, u)
		t.ClosedAt = parse(closed)
		h.ManualTerminals = append(h.ManualTerminals, t)
	}
	if err = terminals.Err(); err != nil {
		return err
	}
	ides, err := s.db.QueryContext(ctx, `select id,holon_id,provider,state,tab_order,desired_open,reason,created_at,updated_at,ready_at,closed_at from holon_ides where holon_id=? order by tab_order,created_at`, h.ID)
	if err != nil {
		return err
	}
	defer ides.Close()
	for ides.Next() {
		var v holons.IDE
		var c, u string
		var ready, closed sql.NullString
		if err = ides.Scan(&v.ID, &v.HolonID, &v.Provider, &v.State, &v.TabOrder, &v.DesiredOpen, &v.Reason, &c, &u, &ready, &closed); err != nil {
			return err
		}
		v.CreatedAt, _ = time.Parse(time.RFC3339Nano, c)
		v.UpdatedAt, _ = time.Parse(time.RFC3339Nano, u)
		v.ReadyAt = parse(ready)
		v.ClosedAt = parse(closed)
		h.IDEs = append(h.IDEs, v)
	}
	return ides.Err()
}
func (s *Store) Update(ctx context.Context, h holons.Holon) error {
	r, e := s.db.ExecContext(ctx, `update holons set title=?,status=?,reason=?,exit_code=?,published=?,base_branch=?,base_commit=?,work_session_start_commit=?,worktree_branch=?,upstream_branch=?,upstream_head_commit=?,synchronized_target_commit=?,rebase_attempt=?,started_at=?,finished_at=?,archived_at=?,end_requested=? where id=?`, h.Title, h.Status, h.Reason, h.ExitCode, h.Published, holons.CanonicalBaseBranch(h.BaseBranch), h.BaseCommit, h.WorkSessionStartCommit, h.WorktreeBranch, h.UpstreamBranch, h.UpstreamHeadCommit, h.SynchronizedTargetCommit, rebaseJSON(h.RebaseAttempt), nts(h.StartedAt), nts(h.FinishedAt), nts(h.ArchivedAt), h.EndRequested, h.ID)
	if e != nil {
		return e
	}
	n, _ := r.RowsAffected()
	if n == 0 {
		return holons.ErrNotFound
	}
	return nil
}

// UpdatePublicationInTransaction changes only the publication checkpoint so a
// linked PR workflow can save its head in the same transaction.
func (s *Store) UpdatePublicationInTransaction(ctx context.Context, tx *sql.Tx, h holons.Holon) error {
	r, err := tx.ExecContext(ctx, `update holons set published=?,upstream_branch=?,upstream_head_commit=?,synchronized_target_commit=? where id=?`, h.Published, h.UpstreamBranch, h.UpstreamHeadCommit, h.SynchronizedTargetCommit, h.ID)
	if err != nil {
		return err
	}
	n, err := r.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return holons.ErrNotFound
	}
	return nil
}

func (s *Store) SetStatus(ctx context.Context, id string, status holons.Status, now time.Time) (holons.Holon, error) {
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return holons.Holon{}, e
	}
	defer tx.Rollback()
	r, e := tx.ExecContext(ctx, `update holons set status=?,reason='',exit_code=null,finished_at=case when ?='queued' then null else finished_at end,end_requested=case when ?='queued' then 0 else end_requested end where id=?`, status, status, status, id)
	if e != nil {
		return holons.Holon{}, e
	}
	n, _ := r.RowsAffected()
	if n == 0 {
		return holons.Holon{}, holons.ErrNotFound
	}
	if status == holons.StatusQueued {
		_, e = tx.ExecContext(ctx, `update holon_agent_sessions set terminal_id='',status='queued',reason='',exit_code=null,started_at=null,finished_at=null,updated_at=? where holon_id=? and closed_at is null and status in ('cancelled','completed','failed','lost','expired','recovery_failed')`, ts(now), id)
	} else if status == holons.StatusCancelling {
		_, e = tx.ExecContext(ctx, `update holon_agent_sessions set status='cancelling',updated_at=? where holon_id=? and closed_at is null and status not in ('cancelled','completed','failed','lost','expired')`, ts(now), id)
	}
	if e != nil {
		return holons.Holon{}, e
	}
	if e = tx.Commit(); e != nil {
		return holons.Holon{}, e
	}
	return s.Get(ctx, id)
}
func (s *Store) AddAgentSession(ctx context.Context, id string, a holons.AgentSession) (holons.Holon, error) {
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return holons.Holon{}, e
	}
	defer tx.Rollback()
	var next int
	if e = tx.QueryRowContext(ctx, `select coalesce(max(tab_order),-1)+1 from (select tab_order from holon_agent_sessions where holon_id=? and closed_at is null union all select tab_order from holon_manual_terminals where holon_id=? and closed_at is null union all select tab_order from holon_ides where holon_id=? and desired_open=1)`, id, id, id).Scan(&next); e != nil {
		return holons.Holon{}, e
	}
	a.TabOrder = next
	if e = insertAgent(ctx, tx, a); e != nil {
		return holons.Holon{}, e
	}
	if e = aggregateStatus(ctx, tx, id, a.UpdatedAt); e != nil {
		return holons.Holon{}, e
	}
	if e = tx.Commit(); e != nil {
		return holons.Holon{}, e
	}
	return s.Get(ctx, id)
}
func (s *Store) UpdateAgentSession(ctx context.Context, id string, a holons.AgentSession, aggregate bool) (holons.Holon, error) {
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return holons.Holon{}, e
	}
	defer tx.Rollback()
	state := ""
	if a.CommitPrompt != nil {
		state = a.CommitPrompt.State
	}
	r, e := tx.ExecContext(ctx, `update holon_agent_sessions set agent_type=?,model=?,permissions=?,prompt=?,title=?,terminal_id=?,status=?,reason=?,exit_code=?,resume_target=?,rollout_path=?,activity=?,input_state=?,observability_status=?,observability_message=?,context_tokens=?,commit_prompt_state=?,commit_prompt_change_hash=?,commit_start_head=?,updated_at=?,started_at=?,finished_at=?,closed_at=? where id=? and holon_id=?`, a.AgentType, a.Model, a.Permissions, a.Prompt, a.Title, a.TerminalID, a.Status, a.Reason, a.ExitCode, a.ResumeTarget, a.RolloutPath, a.Activity, a.InputState, a.ObservabilityStatus, a.ObservabilityMessage, a.ContextTokens, state, a.CommitPromptChangeHash, a.CommitStartHead, ts(a.UpdatedAt), nts(a.StartedAt), nts(a.FinishedAt), nts(a.ClosedAt), a.ID, id)
	if e != nil {
		return holons.Holon{}, e
	}
	n, _ := r.RowsAffected()
	if n != 1 {
		return holons.Holon{}, holons.ErrNotFound
	}
	if aggregate {
		if e = aggregateStatus(ctx, tx, id, a.UpdatedAt); e != nil {
			return holons.Holon{}, e
		}
	}
	if e = tx.Commit(); e != nil {
		return holons.Holon{}, e
	}
	return s.Get(ctx, id)
}
func aggregateStatus(ctx context.Context, tx *sql.Tx, id string, now time.Time) error {
	rows, e := tx.QueryContext(ctx, `select status,reason,exit_code,started_at,finished_at,updated_at from holon_agent_sessions where holon_id=? and closed_at is null`, id)
	if e != nil {
		return e
	}
	defer rows.Close()
	type candidate struct {
		status            holons.Status
		reason            string
		exit              *int
		started, finished *time.Time
		updated           time.Time
	}
	var selected *candidate
	var earliestStarted, latestFinished *time.Time
	allTerminal := true
	for rows.Next() {
		var v candidate
		var exit sql.NullInt64
		var started, finished sql.NullString
		var updated string
		if e = rows.Scan(&v.status, &v.reason, &exit, &started, &finished, &updated); e != nil {
			return e
		}
		if exit.Valid {
			n := int(exit.Int64)
			v.exit = &n
		}
		v.started = parse(started)
		v.finished = parse(finished)
		v.updated, _ = time.Parse(time.RFC3339Nano, updated)
		if v.started != nil && (earliestStarted == nil || v.started.Before(*earliestStarted)) {
			copy := *v.started
			earliestStarted = &copy
		}
		if !holons.IsTerminal(v.status) || v.finished == nil {
			allTerminal = false
		} else if latestFinished == nil || v.finished.After(*latestFinished) {
			copy := *v.finished
			latestFinished = &copy
		}
		if selected == nil || statusRank(v.status) > statusRank(selected.status) || (statusRank(v.status) == statusRank(selected.status) && v.updated.After(selected.updated)) {
			copy := v
			selected = &copy
		}
	}
	if e = rows.Err(); e != nil {
		return e
	}
	if selected == nil {
		var current holons.Status
		var finished sql.NullString
		if e = tx.QueryRowContext(ctx, `select status,finished_at from holons where id=?`, id).Scan(&current, &finished); e != nil {
			return e
		}
		if holons.IsTerminal(current) {
			return nil
		}
		var latest sql.NullString
		if e = tx.QueryRowContext(ctx, `select max(closed_at) from holon_agent_sessions where holon_id=?`, id).Scan(&latest); e != nil {
			return e
		}
		if !latest.Valid {
			latest = sql.NullString{String: ts(now), Valid: true}
		}
		_, e = tx.ExecContext(ctx, `update holons set status='cancelled',reason='',exit_code=null,started_at=null,finished_at=? where id=?`, latest.String, id)
		return e
	}
	if !allTerminal {
		latestFinished = nil
	}
	_, e = tx.ExecContext(ctx, `update holons set status=?,reason=?,exit_code=?,started_at=?,finished_at=? where id=?`, selected.status, selected.reason, selected.exit, nts(earliestStarted), nts(latestFinished), id)
	return e
}
func statusRank(status holons.Status) int {
	switch status {
	case holons.StatusCancelling:
		return 90
	case holons.StatusRestoring:
		return 85
	case holons.StatusRunning:
		return 80
	case holons.StatusPreparing:
		return 70
	case holons.StatusQueued, holons.StatusNaming:
		return 60
	case holons.StatusRecoveryFailed:
		return 50
	case holons.StatusFailed, holons.StatusLost, holons.StatusExpired:
		return 30
	case holons.StatusCancelled:
		return 20
	case holons.StatusCompleted:
		return 10
	}
	return 0
}
func (s *Store) AddManualTerminal(ctx context.Context, id string, t holons.ManualTerminal) (holons.Holon, error) {
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return holons.Holon{}, e
	}
	defer tx.Rollback()
	var next int
	if e = tx.QueryRowContext(ctx, `select coalesce(max(tab_order),-1)+1 from (select tab_order from holon_agent_sessions where holon_id=? and closed_at is null union all select tab_order from holon_manual_terminals where holon_id=? and closed_at is null union all select tab_order from holon_ides where holon_id=? and desired_open=1)`, id, id, id).Scan(&next); e != nil {
		return holons.Holon{}, e
	}
	t.TabOrder = next
	_, e = tx.ExecContext(ctx, `insert into holon_manual_terminals(id,holon_id,terminal_id,title,cwd,tab_order,created_at,updated_at,closed_at) values(?,?,?,?,?,?,?,?,?)`, t.ID, id, t.TerminalID, t.Title, t.CWD, t.TabOrder, ts(t.CreatedAt), ts(t.UpdatedAt), nts(t.ClosedAt))
	if e != nil {
		return holons.Holon{}, e
	}
	if e = tx.Commit(); e != nil {
		return holons.Holon{}, e
	}
	return s.Get(ctx, id)
}
func (s *Store) UpdateManualTerminal(ctx context.Context, id string, t holons.ManualTerminal) (holons.Holon, error) {
	r, e := s.db.ExecContext(ctx, `update holon_manual_terminals set title=?,terminal_id=?,cwd=?,updated_at=?,closed_at=? where id=? and holon_id=?`, t.Title, t.TerminalID, t.CWD, ts(t.UpdatedAt), nts(t.ClosedAt), t.ID, id)
	if e != nil {
		return holons.Holon{}, e
	}
	n, _ := r.RowsAffected()
	if n != 1 {
		return holons.Holon{}, holons.ErrNotFound
	}
	return s.Get(ctx, id)
}
func (s *Store) ListByPullRequest(ctx context.Context, prID string) ([]holons.Holon, error) {
	rows, e := s.db.QueryContext(ctx, selectHolon+` where pull_request_id=? order by case kind when 'pr_metadata' then 0 when 'pr_review' then 1 when 'pr_worker' then 2 when 'rebase' then 3 else 4 end, created_at`, prID)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := make([]holons.Holon, 0)
	for rows.Next() {
		h, e := scanHolon(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, h)
	}
	if e = rows.Err(); e != nil {
		return nil, e
	}
	for i := range out {
		if e = s.children(ctx, &out[i]); e != nil {
			return nil, e
		}
	}
	return out, nil
}
func (s *Store) ReorderTabs(ctx context.Context, id string, tabs []holons.TabRef) (holons.Holon, error) {
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return holons.Holon{}, e
	}
	defer tx.Rollback()
	rows, e := tx.QueryContext(ctx, `select 'agent',id from holon_agent_sessions where holon_id=? and closed_at is null union all select 'terminal',id from holon_manual_terminals where holon_id=? and closed_at is null union all select 'ide',id from holon_ides where holon_id=? and desired_open=1`, id, id, id)
	if e != nil {
		return holons.Holon{}, e
	}
	expected := map[string]bool{}
	for rows.Next() {
		var typ, tabID string
		if e = rows.Scan(&typ, &tabID); e != nil {
			rows.Close()
			return holons.Holon{}, e
		}
		expected[typ+":"+tabID] = true
	}
	if e = rows.Err(); e != nil {
		rows.Close()
		return holons.Holon{}, e
	}
	if e = rows.Close(); e != nil {
		return holons.Holon{}, e
	}
	if len(tabs) != len(expected) {
		return holons.Holon{}, fmt.Errorf("%w: tabs must be a complete permutation", holons.ErrInvalid)
	}
	seen := map[string]bool{}
	for order, t := range tabs {
		key := t.Type + ":" + t.ID
		if seen[key] {
			return holons.Holon{}, fmt.Errorf("%w: duplicate tab", holons.ErrInvalid)
		}
		seen[key] = true
		if !expected[key] {
			return holons.Holon{}, fmt.Errorf("%w: unknown tab", holons.ErrInvalid)
		}
		table := "holon_agent_sessions"
		if t.Type == "terminal" {
			table = "holon_manual_terminals"
		} else if t.Type == "ide" {
			table = "holon_ides"
		} else if t.Type != "agent" {
			return holons.Holon{}, fmt.Errorf("%w: tab type", holons.ErrInvalid)
		}
		r, e := tx.ExecContext(ctx, `update `+table+` set tab_order=? where holon_id=? and id=?`, order, id, t.ID)
		if e != nil {
			return holons.Holon{}, e
		}
		n, _ := r.RowsAffected()
		if n != 1 {
			return holons.Holon{}, holons.ErrInvalid
		}
	}
	if e = tx.Commit(); e != nil {
		return holons.Holon{}, e
	}
	return s.Get(ctx, id)
}

var _ holons.Store = (*Store)(nil)

func rebaseJSON(a *holons.RebaseAttempt) string { b, _ := json.Marshal(a); return string(b) }

// ReserveRebase atomically records the attempt and its destination tab.
func (s *Store) ReserveRebase(ctx context.Context, h holons.Holon, a holons.AgentSession) (holons.Holon, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return holons.Holon{}, err
	}
	defer tx.Rollback()
	if err = tx.QueryRowContext(ctx, `select coalesce(max(tab_order),-1)+1 from (select tab_order from holon_agent_sessions where holon_id=? union all select tab_order from holon_manual_terminals where holon_id=? union all select tab_order from holon_ides where holon_id=?)`, h.ID, h.ID, h.ID).Scan(&a.TabOrder); err != nil {
		return holons.Holon{}, err
	}
	if err = insertAgent(ctx, tx, a); err != nil {
		return holons.Holon{}, err
	}
	if _, err = tx.ExecContext(ctx, `update holons set rebase_attempt=? where id=?`, rebaseJSON(h.RebaseAttempt), h.ID); err != nil {
		return holons.Holon{}, err
	}
	if err = tx.Commit(); err != nil {
		return holons.Holon{}, err
	}
	return s.Get(ctx, h.ID)
}

// UpdateSynchronization does not touch publication fields or agent runtime rows.
func (s *Store) UpdateSynchronization(ctx context.Context, h holons.Holon) error {
	_, err := s.db.ExecContext(ctx, `update holons set synchronized_target_commit=?,rebase_attempt=? where id=?`, h.SynchronizedTargetCommit, rebaseJSON(h.RebaseAttempt), h.ID)
	return err
}

// SetSelectedTab leaves lifecycle fields untouched.
func (s *Store) SetSelectedTab(ctx context.Context, id, tabID string) error {
	result, err := s.db.ExecContext(ctx, `update holons set last_selected_tab_id=? where id=?`, tabID, id)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count == 0 {
		return holons.ErrNotFound
	}
	return nil
}

// ReserveSynchronizationInTransaction is the composition boundary for a fresh
// PR-targeted attempt. Existing running/conflicted attempts keep their target.
func (s *Store) ReserveSynchronizationInTransaction(ctx context.Context, tx *sql.Tx, h holons.Holon) error {
	var raw string
	if err := tx.QueryRowContext(ctx, `select coalesce(rebase_attempt,'') from holons where id=?`, h.ID).Scan(&raw); err != nil {
		return err
	}
	var previous holons.RebaseAttempt
	if raw != "" && raw != "null" {
		if err := json.Unmarshal([]byte(raw), &previous); err != nil {
			return err
		}
		if previous.Active() {
			return holons.ErrRebaseActive
		}
	}
	_, err := tx.ExecContext(ctx, `update holons set synchronized_target_commit=?,rebase_attempt=? where id=?`, h.SynchronizedTargetCommit, rebaseJSON(h.RebaseAttempt), h.ID)
	return err
}

// CompleteReservation writes only the fields populated by queued preparation.
func (s *Store) CompleteReservation(ctx context.Context, h holons.Holon) error {
	result, err := s.db.ExecContext(ctx, `update holons set title=?,prompt=?,base_branch=?,base_commit=?,work_session_start_commit=?,worktree_path=?,worktree_branch=?,upstream_branch=?,upstream_head_commit=? where id=? and status='preparing' and end_requested=0`, h.Title, h.Prompt, holons.CanonicalBaseBranch(h.BaseBranch), h.BaseCommit, h.WorkSessionStartCommit, h.WorktreePath, h.WorktreeBranch, h.UpstreamBranch, h.UpstreamHeadCommit, h.ID)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return holons.ErrInvalid
	}
	return nil
}
