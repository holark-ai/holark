package sqliteadapter

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	pr "github.com/holark-ai/holark/internal/pullrequestlifecycle"
	branches "github.com/holark-ai/holark/internal/repository/sqliteadapter"
)

func (s *Store) BeginOperation(ctx context.Context, operation pr.Operation) (pr.Operation, bool, error) {
	if operation.RequestID == "" {
		operation.RequestID = pr.RequestID(ctx)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return operation, false, err
	}
	defer tx.Rollback()
	existing, found, err := operationInTransaction(ctx, tx, operation.RequestID)
	if err != nil {
		return existing, false, err
	}
	if found {
		if !pr.MatchesOperationRequest(existing, operation) {
			return existing, false, pr.OperationRequestConflict()
		}
		return existing, false, nil
	}
	rows, err := tx.QueryContext(ctx, `select document from pull_request_operations where json_extract(document, '$.status') in ('running','uncertain') and ((pull_request_id = ? and pull_request_id != '') or (holon_id = ? and holon_id != ''))`, operation.PullRequestID, operation.HolonID)
	if err != nil {
		return operation, false, err
	}
	conflict := false
	for rows.Next() {
		var raw string
		if err = rows.Scan(&raw); err != nil {
			break
		}
		var other pr.Operation
		if err = json.Unmarshal([]byte(raw), &other); err != nil {
			break
		}
		if other.Active() && groupsOverlap(operation.Groups, other.Groups) {
			conflict = true
		}
	}
	rows.Close()
	if err != nil {
		return operation, false, err
	}
	if conflict {
		return operation, false, pr.ErrOperationInProgress
	}
	operation.Status = "running"
	operation.OwnerID = s.instanceID
	operation.CreatedAt = time.Now().UTC()
	operation.UpdatedAt = operation.CreatedAt
	if operation.PullRequestID != "" {
		current, err := getInTransaction(ctx, tx, operation.PullRequestID)
		if err != nil {
			return operation, false, err
		}
		if operation.ExpectedInputs != nil && *operation.ExpectedInputs != *pr.CaptureMutationInputs(current) {
			return operation, false, pr.ErrSynchronizationStale
		}
		if operation.ExpectedHead != "" && operation.ExpectedHead != current.HeadCommit {
			return operation, false, pr.ErrSynchronizationStale
		}
		if operation.ExpectedHead == "" {
			operation.ExpectedHead = current.HeadCommit
		}
		if err := claimOperationBranches(ctx, tx, &operation, current); err != nil {
			return operation, false, err
		}
		current, err = getInTransaction(ctx, tx, operation.PullRequestID)
		if err != nil {
			return operation, false, err
		}

		advanceGroups(&current, operation.Groups)
		if err = saveInTransaction(ctx, tx, &current); err != nil {
			return operation, false, err
		}
	}
	if err = saveOperation(ctx, tx, operation); err != nil {
		return operation, false, err
	}
	return operation, true, tx.Commit()
}
func (s *Store) SetActionCompletionHook(hook func(string)) {
	s.actionCompletionMu.Lock()
	defer s.actionCompletionMu.Unlock()
	s.actionCompletion = hook
}
func (s *Store) NotifyActionCompletion(id string) {
	s.actionCompletionMu.RLock()
	hook := s.actionCompletion
	s.actionCompletionMu.RUnlock()
	if hook != nil && id != "" {
		hook(id)
	}
}
func (s *Store) SetPublicationCompletionHook(hook func(string)) {
	s.actionCompletionMu.Lock()
	defer s.actionCompletionMu.Unlock()
	s.publicationCompletion = hook
}
func (s *Store) NotifyPublicationCompletion(id string) {
	s.actionCompletionMu.RLock()
	hook := s.publicationCompletion
	s.actionCompletionMu.RUnlock()
	if hook != nil && id != "" {
		hook(id)
	}
}
func (s *Store) CompleteOperation(ctx context.Context, requestID, outcome, errorText string) (operation pr.Operation, resultErr error) {
	defer func() {
		if resultErr == nil && operation.PullRequestID != "" {
			s.NotifyActionCompletion(operation.PullRequestID)
		}
	}()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return pr.Operation{}, err
	}
	defer tx.Rollback()
	operation, err = s.CompleteOperationInTransaction(ctx, tx, requestID, outcome, errorText)
	if err != nil {
		return operation, err
	}
	return operation, tx.Commit()
}

// CompleteOperationInTransaction commits intent, confirmation and view invalidation
// alongside the field changes made by lifecycle, metadata and publication writers.
func (s *Store) CompleteOperationInTransaction(ctx context.Context, tx *sql.Tx, requestID, outcome, errorText string) (pr.Operation, error) {
	operation, found, err := operationInTransaction(ctx, tx, requestID)
	if err != nil {
		return operation, err
	}
	if !found {
		return operation, pr.ErrNotFound
	}
	if !operation.Active() {
		return operation, nil
	}
	if outcome != "running" && outcome != "uncertain" {
		if err := branches.ReleaseBranches(ctx, tx, requestID); err != nil {
			return operation, err
		}
	}
	operation.Status = outcome
	operation.Error = errorText
	operation.UpdatedAt = time.Now().UTC()
	if operation.PullRequestID != "" {
		current, err := getInTransaction(ctx, tx, operation.PullRequestID)
		if err != nil {
			return operation, err
		}
		// Capture a retained comparison against the final operation generation.
		// Advancing lifecycle after capture would leave a ready snapshot stale.
		advanceGroups(&current, operation.Groups)
		if !operation.Active() && current.Status.Active() && current.ComparisonState == pr.ComparisonReady && current.Comparison != nil {
			// Release changes only mutation evidence. Keep a completed immutable pair
			// current if its content and relationships still match both branch records.
			pair, captureErr := captureComparison(ctx, tx, current)
			snapshot := current.Comparison.Inputs
			if captureErr == nil && pair.Base.Identity == snapshot.Base.Identity && pair.Head.Identity == snapshot.Head.Identity && pair.Base.Generation == snapshot.Base.Generation && pair.Head.Generation == snapshot.Head.Generation {
				current.Comparison.Inputs = pair
			} else {
				invalidateComparison(&current)
			}
		}
		if outcome == "succeeded" {
			if containsGroup(operation.Groups, pr.LifecycleGroup) && (operation.Kind == "transition" || operation.Kind == "merge") {
				current.LifecycleConfirmation = &pr.Confirmation{Status: current.Status}
			}
			if containsGroup(operation.Groups, pr.TopologyGroup) {
				head := current.HeadCommit
				if current.HeadRef.Valid() {
					published, _, err := branches.ReadBranch(ctx, tx, current.HeadRef)
					if err != nil {
						return operation, err
					}
					if published.Exists {
						head = published.Commit
					}
				}
				current.TopologyConfirmation = &pr.Confirmation{HeadCommit: head}
			}
		}
		if err = saveInTransaction(ctx, tx, &current); err != nil {
			return operation, err
		}
	}
	if err = saveOperation(ctx, tx, operation); err != nil {
		return operation, err
	}
	return operation, nil
}
func (s *Store) GetOperation(ctx context.Context, id string) (pr.Operation, bool, error) {
	var raw string
	err := s.db.QueryRowContext(ctx, `select document from pull_request_operations where request_id=?`, id).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return pr.Operation{}, false, nil
	}
	if err != nil {
		return pr.Operation{}, false, err
	}
	var operation pr.Operation
	err = json.Unmarshal([]byte(raw), &operation)
	return operation, err == nil, err
}
func (s *Store) ListUnfinishedOperations(ctx context.Context) ([]pr.Operation, error) {
	rows, err := s.db.QueryContext(ctx, `select document from pull_request_operations where json_extract(document, '$.status') in ('running','uncertain')`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []pr.Operation{}
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		var operation pr.Operation
		if err := json.Unmarshal([]byte(raw), &operation); err != nil {
			return nil, err
		}
		result = append(result, operation)
	}
	return result, rows.Err()
}
func operationInTransaction(ctx context.Context, tx *sql.Tx, id string) (pr.Operation, bool, error) {
	var raw string
	err := tx.QueryRowContext(ctx, `select document from pull_request_operations where request_id=?`, id).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return pr.Operation{}, false, nil
	}
	if err != nil {
		return pr.Operation{}, false, err
	}
	var operation pr.Operation
	err = json.Unmarshal([]byte(raw), &operation)
	return operation, err == nil, err
}
func saveOperation(ctx context.Context, tx *sql.Tx, operation pr.Operation) error {
	raw, err := json.Marshal(operation)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `insert into pull_request_operations(request_id,pull_request_id,holon_id,document) values(?,?,?,?) on conflict(request_id) do update set pull_request_id=excluded.pull_request_id,holon_id=excluded.holon_id,document=excluded.document`, operation.RequestID, operation.PullRequestID, operation.HolonID, string(raw))
	return err
}
func containsGroup(groups []pr.FieldGroup, group pr.FieldGroup) bool {
	for _, value := range groups {
		if value == group {
			return true
		}
	}
	return false
}
func groupsOverlap(a, b []pr.FieldGroup) bool {
	for _, group := range a {
		if containsGroup(b, group) {
			return true
		}
	}
	return false
}
func advanceGroups(current *pr.PullRequest, groups []pr.FieldGroup) {
	if containsGroup(groups, pr.LifecycleGroup) {
		current.LifecycleGeneration++
	}
	if containsGroup(groups, pr.TopologyGroup) {
		current.TopologyGeneration++
	}
	if containsGroup(groups, pr.MetadataGroup) {
		current.MetadataGeneration++
	}
}
func protected(current pr.PullRequest, group pr.FieldGroup) bool {
	for _, operation := range current.Operations {
		if operation.Active() && containsGroup(operation.Groups, group) {
			return true
		}
	}
	return false
}

func (s *Store) BeginObservation(ctx context.Context, repositoryID string, groups []pr.FieldGroup) (pr.ObservationToken, error) {
	if err := ctx.Err(); err != nil {
		return pr.ObservationToken{}, err
	}
	// Finish this local snapshot transaction before returning to the refresh
	// worker. Cancellation-driven rollback can otherwise outlive shutdown.
	ctx = context.WithoutCancel(ctx)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return pr.ObservationToken{}, err
	}
	defer tx.Rollback()
	token := pr.ObservationToken{Groups: groups, Versions: map[string]pr.ObservationVersion{}}
	if err = tx.QueryRowContext(ctx, `update pull_request_observation_sequence set value=value+1 where id=1 returning value`).Scan(&token.Sequence); err != nil {
		return token, err
	}
	rows, err := tx.QueryContext(ctx, `select repository_id,document from pull_request_catalog where repository_id=?`, repositoryID)
	if err != nil {
		return token, err
	}
	for rows.Next() {
		var repo, raw string
		if err = rows.Scan(&repo, &raw); err != nil {
			break
		}
		var current pr.PullRequest
		current, err = decodePullRequest(repo, raw)
		if err != nil {
			break
		}
		token.Versions[current.ID] = pr.ObservationVersion{Lifecycle: current.LifecycleGeneration, Topology: current.TopologyGeneration, Metadata: current.MetadataGeneration}
	}
	rows.Close()
	if err != nil {
		return token, err
	}
	return token, tx.Commit()
}
func (s *Store) UpsertObservedPullRequests(repositoryID string, incoming []pr.PullRequest, token pr.ObservationToken) (int, int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	imported, updated := 0, 0
	for _, current := range incoming {
		created, err := s.upsertObservedPullRequest(repositoryID, current, token)
		if err != nil {
			return imported, updated, err
		}
		if created {
			imported++
		} else {
			updated++
		}
	}
	return imported, updated, nil
}
func acceptObservation(current *pr.PullRequest, incoming pr.PullRequest, token *pr.ObservationToken) pr.PullRequest {
	result := *current
	// Withdrawing Draft confirms WIP locally and Closed remotely.
	withdrawn := current.Status == pr.StatusWIP && current.LifecycleConfirmation != nil && incoming.Status == pr.StatusClosed
	if withdrawn {
		incoming.Status, incoming.ClosedAt = pr.StatusWIP, nil
	}
	captured, known := token.Versions[current.ID]
	lifecycle := known && containsGroup(token.Groups, pr.LifecycleGroup) && (!protected(*current, pr.LifecycleGroup) || confirmsOpeningPreparation(*current, incoming)) && captured.Lifecycle == current.LifecycleGeneration && token.Sequence > current.LifecycleObservation
	// A matching GitHub merge is terminal, including while an action owns the
	// lifecycle field. No later non-merged observation can reverse it.
	if current.SyncProvider == pr.SyncProviderGitHub && current.SyncExternalID != "" && incoming.SyncProvider == current.SyncProvider && incoming.SyncExternalID == current.SyncExternalID && incoming.Status == pr.StatusMerged && current.Status != pr.StatusMerged {
		lifecycle = true
	}
	if current.Status == pr.StatusMerged && incoming.Status != pr.StatusMerged {
		lifecycle = false
	}
	topology := known && containsGroup(token.Groups, pr.TopologyGroup) && !protected(*current, pr.TopologyGroup) && captured.Topology == current.TopologyGeneration && token.Sequence > current.TopologyObservation
	metadata := known && containsGroup(token.Groups, pr.MetadataGroup) && !protected(*current, pr.MetadataGroup) && captured.Metadata == current.MetadataGeneration && token.Sequence > current.MetadataObservation
	// A differing provider SHA is not evidence of ordering. The remote branch
	// verifier supplies VerifiedHead only after checking the actual remote ref.
	if marker := current.TopologyConfirmation; topology && marker != nil && !current.HeadRef.Valid() {
		if incoming.HeadCommit != marker.HeadCommit {
			if incoming.VerifiedHead {
				result.TopologyConfirmation = &pr.Confirmation{HeadCommit: incoming.HeadCommit}
			} else {
				topology = false
			}
		}
	}
	if lifecycle {
		result.LifecycleConfirmation = nil
		if withdrawn {
			result.LifecycleConfirmation = current.LifecycleConfirmation
		}
		result.Status = incoming.Status
		result.ClosedAt = incoming.ClosedAt
		result.MergedAt = incoming.MergedAt
		result.MergedCommit = incoming.MergedCommit
		result.MergeStrategy = incoming.MergeStrategy
		result.LifecycleObservation = token.Sequence
		result.SyncData = mergeSyncData(result.SyncData, incoming.SyncData, true)
	}
	if topology {
		result.HeadBranch = incoming.HeadBranch
		result.BaseBranch = incoming.BaseBranch
		result.SyncData = mergeTopologyData(result.SyncData, incoming.SyncData)
		result.TopologyObservation = token.Sequence
	}
	if marker := current.MetadataConfirmation; metadata && marker != nil {
		if incoming.Title != marker.Title || incoming.Summary != marker.Summary {
			if !incoming.UpdatedAt.After(marker.ProviderUpdatedAt) {
				metadata = false
			} else {
				result.MetadataConfirmation = &pr.MetadataConfirmation{Title: incoming.Title, Summary: incoming.Summary, ProviderUpdatedAt: incoming.UpdatedAt}
			}
		}
	}
	if metadata {
		result.Title = incoming.Title
		result.Summary = incoming.Summary
		result.MetadataObservation = token.Sequence
	}
	if lifecycle || topology || metadata {
		result.SyncedAt = incoming.SyncedAt
		if incoming.UpdatedAt.After(result.UpdatedAt) {
			result.UpdatedAt = incoming.UpdatedAt
		}
	}
	if !lifecycle || !topology || !metadata {
		slog.Debug("Rejected stale pull request observation groups", "pull_request_id", current.ID, "sequence", token.Sequence, "lifecycle", lifecycle, "topology", topology, "metadata", metadata)
	}
	return result
}

// mergeSyncData treats readiness as focused patches. It
// never writes a stale full provider document over publication or lifecycle.
func mergeSyncData(current, incoming json.RawMessage, lifecycle bool) json.RawMessage {
	var destination, source map[string]json.RawMessage
	if json.Unmarshal(current, &destination) != nil || destination == nil {
		destination = map[string]json.RawMessage{}
	}
	if json.Unmarshal(incoming, &source) != nil {
		return current
	}
	var oldGithub, newGithub map[string]json.RawMessage
	_ = json.Unmarshal(destination["github"], &oldGithub)
	_ = json.Unmarshal(source["github"], &newGithub)
	if oldGithub == nil {
		oldGithub = map[string]json.RawMessage{}
	}
	if newGithub == nil {
		return incoming
	}
	if lifecycle {
		for key, value := range newGithub {
			if key != "readiness" && key != "head_repository_url" && key != "base_repository_url" {
				oldGithub[key] = value
			}
		}
	} else {
		for _, key := range []string{"readiness"} {
			if value, ok := newGithub[key]; ok {
				oldGithub[key] = value
			}
		}
	}
	destination["github"], _ = json.Marshal(oldGithub)
	result, _ := json.Marshal(destination)
	return result
}

func (s *Store) UpdateMetadataInTransaction(ctx context.Context, tx *sql.Tx, id, title, description string, at, providerUpdatedAt time.Time) error {
	current, err := getInTransaction(ctx, tx, id)
	if err != nil {
		return err
	}
	current.Title = title
	current.Summary = description
	current.UpdatedAt = at
	if !providerUpdatedAt.IsZero() {
		current.MetadataConfirmation = &pr.MetadataConfirmation{Title: title, Summary: description, ProviderUpdatedAt: providerUpdatedAt}
	}
	return saveInTransaction(ctx, tx, &current)
}

func (s *Store) UpdatePullRequestReadiness(id string, generation int64, head string, data json.RawMessage, at time.Time) (pr.PullRequest, error) {
	return s.UpdatePullRequestReadinessObservation(id, generation, 0, head, data, at)
}
func (s *Store) UpdatePullRequestReadinessObservation(id string, generation, sequence int64, head string, data json.RawMessage, at time.Time) (pr.PullRequest, error) {
	return s.mutate(id, func(current *pr.PullRequest) error {
		if current.BaseRef.Valid() && !current.HasCurrentComparison() {
			return pr.ErrComparisonUnavailable
		}
		if current.TopologyGeneration != generation || head != "" && current.HeadCommit != head {
			return nil
		}
		if sequence > 0 && sequence <= current.ReadinessObservation {
			return nil
		}
		var incoming map[string]map[string]json.RawMessage
		if json.Unmarshal(data, &incoming) == nil {
			patch, _ := json.Marshal(map[string]map[string]json.RawMessage{"github": {"readiness": incoming["github"]["readiness"]}})
			current.SyncData = mergeSyncData(current.SyncData, patch, false)
		}
		current.ReadinessObservation = sequence
		return nil
	})
}

// RecordOperationStep persists progress independently so recovery can inspect
// completed remote effects before attempting another publication or creation.
func (s *Store) RecordOperationStep(ctx context.Context, requestID, pullRequestID, name string, step pr.OperationStep) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	operation, found, err := operationInTransaction(ctx, tx, requestID)
	if err != nil {
		return err
	}
	if !found {
		return pr.ErrNotFound
	}
	if name == "mutation" && operation.Steps[name].Status != "" {
		return pr.ErrOperationInProgress
	}
	if !operation.Active() {
		return pr.ErrInvalidTransition
	}
	if operation.Steps == nil {
		operation.Steps = map[string]pr.OperationStep{}
	}
	if operation.Kind == "create" && name == "preparation" && step.Creation != nil && step.Creation.BranchRead != nil {
		for _, b := range step.Creation.BranchRead.Branches {
			if b.Identity == step.Creation.HeadRef {
				if err := branches.ClaimBranch(ctx, tx, b, operation.RequestID); err != nil {
					return pr.ErrOperationInProgress
				}
				if err := invalidateBranch(ctx, tx, b.Identity); err != nil {
					return err
				}
			}
		}
	}
	operation.Steps[name] = step
	operation.UpdatedAt = time.Now().UTC()
	if pullRequestID != "" {
		operation.PullRequestID = pullRequestID
	}
	if pullRequestID != "" {
		current, err := getInTransaction(ctx, tx, pullRequestID)
		if err != nil {
			return err
		}
		advanceGroups(&current, operation.Groups)
		if err = saveInTransaction(ctx, tx, &current); err != nil {
			return err
		}
	}

	if err = saveOperation(ctx, tx, operation); err != nil {
		return err
	}
	return tx.Commit()
}

func mergeTopologyData(current, incoming json.RawMessage) json.RawMessage {
	var destination, source map[string]json.RawMessage
	if json.Unmarshal(current, &destination) != nil || destination == nil {
		destination = map[string]json.RawMessage{}
	}
	if json.Unmarshal(incoming, &source) != nil {
		return current
	}
	var oldGitHub, newGitHub map[string]json.RawMessage
	_ = json.Unmarshal(destination["github"], &oldGitHub)
	_ = json.Unmarshal(source["github"], &newGitHub)
	if newGitHub == nil {
		return current
	}
	if oldGitHub == nil {
		oldGitHub = map[string]json.RawMessage{}
	}
	for _, key := range []string{"head_repository_url", "base_repository_url"} {
		if value, ok := newGitHub[key]; ok {
			oldGitHub[key] = value
		}
	}
	destination["github"], _ = json.Marshal(oldGitHub)
	result, _ := json.Marshal(destination)
	return result
}

// CancelOperationBeforeStep cancels preparation only while no remote mutation
// has started. The check and cancellation share the step writer's transaction.
func (s *Store) CancelOperationBeforeStep(ctx context.Context, id, step string) (bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	operation, found, err := operationInTransaction(ctx, tx, id)
	if err != nil {
		return false, err
	}
	if !found || !operation.Active() {
		return false, nil
	}
	if operation.Steps[step].Status != "" {
		return false, nil
	}
	if _, err = s.CompleteOperationInTransaction(ctx, tx, id, "failed", "Opening was cancelled by closing the pull request."); err != nil {
		return false, err
	}

	return true, tx.Commit()
}

func (s *Store) AttachObservedPullRequest(id string, incoming pr.PullRequest, token pr.ObservationToken) (pr.PullRequest, bool, error) {
	accepted := false
	current, err := s.mutate(id, func(current *pr.PullRequest) error {
		version, known := token.Versions[id]
		if !known || protected(*current, pr.LifecycleGroup) || protected(*current, pr.TopologyGroup) || version.Lifecycle != current.LifecycleGeneration || version.Topology != current.TopologyGeneration || token.Sequence <= current.LifecycleObservation || token.Sequence <= current.TopologyObservation {
			return nil
		}
		*current = acceptObservation(current, incoming, &token)
		current.SyncProvider = incoming.SyncProvider
		current.SyncExternalID = incoming.SyncExternalID
		accepted = true
		return nil
	})
	return current, accepted, err
}

// ListRecoverableOperations excludes work still owned by the running process.
// Unknown remote outcomes become eligible only after the execution owner has
// persisted its uncertain result, or when a later process opens the database.
func (s *Store) ListRecoverableOperations(ctx context.Context) ([]pr.Operation, error) {
	operations, err := s.ListUnfinishedOperations(ctx)
	if err != nil {
		return nil, err
	}
	result := []pr.Operation{}
	for _, operation := range operations {
		if operation.OwnerID != s.instanceID || operation.Status == "uncertain" {
			result = append(result, operation)
		}
	}
	return result, nil
}

// A fresh observation that already satisfies a pending opening request can
// finish preparation externally. It cannot supersede an executing mutation.
func confirmsOpeningPreparation(current, incoming pr.PullRequest) bool {
	matched := false
	for _, operation := range current.Operations {
		if !operation.Active() || !containsGroup(operation.Groups, pr.LifecycleGroup) {
			continue
		}
		if operation.Kind != "transition" || operation.RequestedStatus != incoming.Status || operation.MutationStarted {
			return false
		}
		matched = true
	}
	return matched
}
func (s *Store) CompleteRecoveredHead(ctx context.Context, requestID, id, expected, intended string) (pr.PullRequest, bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return pr.PullRequest{}, false, err
	}
	defer tx.Rollback()
	operation, found, err := operationInTransaction(ctx, tx, requestID)
	if err != nil {
		return pr.PullRequest{}, false, err
	}
	if !found || !operation.Active() || operation.PullRequestID != id || operation.ExpectedHead != expected || operation.Steps["publication"].HeadCommit != intended {
		return pr.PullRequest{}, false, pr.ErrPublicationStale
	}
	matched, err := s.CompleteHeadInTransaction(ctx, tx, id, expected, intended, nil, nil)
	if err != nil || !matched {
		return pr.PullRequest{}, matched, err
	}
	if _, err = s.CompleteOperationInTransaction(ctx, tx, requestID, "succeeded", ""); err != nil {
		return pr.PullRequest{}, false, err
	}
	current, err := getInTransaction(ctx, tx, id)
	if err != nil {
		return current, false, err
	}
	if err := tx.Commit(); err != nil {
		return current, false, err
	}
	s.NotifyActionCompletion(id)
	return current, true, nil
}

func (s *Store) ConfirmRecoveredMetadata(ctx context.Context, requestID, id, title, description string, providerUpdatedAt time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	operation, found, err := operationInTransaction(ctx, tx, requestID)
	if err != nil {
		return err
	}
	if !found || !operation.Active() || operation.PullRequestID != id {
		return pr.ErrNotFound
	}
	current, err := getInTransaction(ctx, tx, id)
	if err != nil {
		return err
	}
	current.Title = title
	current.Summary = description
	current.UpdatedAt = providerUpdatedAt
	current.MetadataConfirmation = &pr.MetadataConfirmation{Title: title, Summary: description, ProviderUpdatedAt: providerUpdatedAt}
	if err = saveInTransaction(ctx, tx, &current); err != nil {
		return err
	}
	if _, err = s.CompleteOperationInTransaction(ctx, tx, requestID, "succeeded", ""); err != nil {
		return err
	}
	return tx.Commit()
}

type operationQuerier interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

// Lists use one query for all active summaries. Historical step payloads never
// enter PR documents or list/detail responses.
func activeOperations(ctx context.Context, db operationQuerier, predicate string, args ...any) (map[string][]pr.ActiveOperation, error) {
	rows, err := db.QueryContext(ctx, `select pull_request_id, request_id,
 json_extract(document,'$.kind'), coalesce(json_extract(document,'$.requested_status'),''),
 json_extract(document,'$.groups'), json_extract(document,'$.status'),
 coalesce(json_extract(document,'$.steps.mutation.status'),'') != ''
 from pull_request_operations where json_extract(document,'$.status') in ('running','uncertain') and `+predicate, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := map[string][]pr.ActiveOperation{}
	for rows.Next() {
		var id, groups string
		var operation pr.ActiveOperation
		if err = rows.Scan(&id, &operation.RequestID, &operation.Kind, &operation.RequestedStatus, &groups, &operation.Status, &operation.MutationStarted); err != nil {
			return nil, err
		}
		if err = json.Unmarshal([]byte(groups), &operation.Groups); err != nil {
			return nil, err
		}
		result[id] = append(result[id], operation)
	}
	return result, rows.Err()
}

// CompleteTransition applies only the confirmed action fields. Concurrent
// publication and metadata changes remain owned by their own transactions.
func (s *Store) CompleteTransition(ctx context.Context, requestID string, expected, confirmed pr.PullRequest) (pr.PullRequest, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return pr.PullRequest{}, err
	}
	defer tx.Rollback()
	current, err := getInTransaction(ctx, tx, expected.ID)
	if err != nil {
		return current, err
	}
	operation, found, err := operationInTransaction(ctx, tx, requestID)
	if err != nil {
		return current, err
	}
	if !found || operation.PullRequestID != current.ID {
		return current, pr.ErrNotFound
	}
	if !operation.Active() {
		return current, nil
	}
	if current.Status != pr.StatusMerged {
		current.Status, current.ClosedAt, current.MergedAt = confirmed.Status, confirmed.ClosedAt, confirmed.MergedAt
		current.MergedCommit, current.MergeStrategy = confirmed.MergedCommit, confirmed.MergeStrategy
		current.SyncData = mergeSyncData(current.SyncData, confirmed.SyncData, true)
		current.UpdatedAt, current.SyncedAt = confirmed.UpdatedAt, confirmed.SyncedAt
	}
	if current.SyncExternalID == "" && confirmed.SyncExternalID != "" {
		current.SyncProvider, current.SyncExternalID = confirmed.SyncProvider, confirmed.SyncExternalID
		// Opening establishes provider relationships even when identical branch
		// observations advanced the display revision during the network request.
		current.SyncData = mergeTopologyData(current.SyncData, confirmed.SyncData)
		if current.MetadataGeneration == expected.MetadataGeneration {
			current.Title, current.Summary = confirmed.Title, confirmed.Summary
		}
		if current.TopologyGeneration == expected.TopologyGeneration {
			current.HeadBranch, current.HeadCommit = confirmed.HeadBranch, confirmed.HeadCommit
			current.BaseBranch, current.BaseCommit, current.DiffBaseCommit = confirmed.BaseBranch, confirmed.BaseCommit, confirmed.DiffBaseCommit
			current.SyncData = mergeTopologyData(current.SyncData, confirmed.SyncData)
		}
	}
	if current.Status == pr.StatusMerged && current.BaseRef.Valid() {
		if err := invalidateBranch(ctx, tx, current.BaseRef); err != nil {
			return current, err
		}
	}
	if err = saveInTransaction(ctx, tx, &current); err != nil {
		return current, err
	}
	if _, err = s.CompleteOperationInTransaction(ctx, tx, requestID, "succeeded", ""); err != nil {
		return current, err
	}
	current, err = getInTransaction(ctx, tx, current.ID)
	if err != nil {
		return current, err
	}
	if err = tx.Commit(); err != nil {
		return current, err
	}
	s.NotifyActionCompletion(current.ID)
	return current, nil
}
