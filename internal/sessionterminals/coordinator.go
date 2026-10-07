// Package sessionterminals coordinates live terminal operations with the
// durable harness and manual-terminal records that own terminal bindings.
package sessionterminals

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/holark-ai/holark/internal/terminals"
)

type Binding struct {
	TerminalID terminals.TerminalID
	OwnerKind  terminals.OwnerKind
	OwnerID    string
	SessionID  string
	RuntimeID  string
	CWD        string
}

type Products interface {
	HarnessTerminalBinding(sessionID, harnessID string) (terminals.TerminalID, string, bool)
	ManualTerminalBinding(sessionID, recordID string) (terminals.TerminalID, string, string, bool)
	ResolveTerminalBinding(terminals.TerminalID) (Binding, bool)
	TerminalBindingsForRuntime(string) ([]Binding, error)
	SessionHarnessIDs(string) ([]string, error)
	SessionTerminalIDs(string) ([]terminals.TerminalID, error)
	CancelHarnessRuntime(sessionID, harnessID string) error
	RetireHarnessRuntime(sessionID, harnessID string) error
	ApplyTerminalCompletion(runtimeID string, completion terminals.ProcessCompletion) error
	ApplyTerminalLost(runtimeID string, terminalID terminals.TerminalID) error
	RemoveManualTerminal(sessionID, recordID string) error
}

type Attachment struct {
	Restore terminals.TerminalRestore
	Updates <-chan terminals.TerminalUpdateStream
	Retired <-chan struct{}

	binding Binding
	retired chan struct{}
	once    sync.Once
}

type Coordinator struct {
	gateway  terminals.LocalGateway
	products Products

	mu          sync.Mutex
	attachments map[terminals.TerminalID]*Attachment
}

func New(gateway terminals.LocalGateway, products Products) (*Coordinator, error) {
	if gateway == nil || products == nil {
		return nil, errors.New("session terminal coordinator dependencies are required")
	}
	return &Coordinator{gateway: gateway, products: products, attachments: make(map[terminals.TerminalID]*Attachment)}, nil
}

func (coordinator *Coordinator) LaunchManual(ctx context.Context, sessionID, recordID string, dimensions terminals.Dimensions) (Binding, error) {
	id, runtimeID, cwd, ok := coordinator.products.ManualTerminalBinding(sessionID, recordID)
	if !ok {
		return Binding{}, terminals.ErrNotFound
	}
	binding := Binding{TerminalID: id, OwnerKind: terminals.OwnerManual, OwnerID: recordID, SessionID: sessionID, RuntimeID: runtimeID, CWD: cwd}
	err := coordinator.gateway.Launch(ctx, runtimeID, terminals.LaunchSpec{
		TerminalID: id, Kind: terminals.LaunchShell, CWD: cwd, Dimensions: dimensions,
		Environment: []string{"HOLARK_HOLON_ID=" + sessionID, "HOLARK_REPO_PATH=" + cwd},
	})
	if err != nil {
		_ = coordinator.products.RemoveManualTerminal(sessionID, recordID)
		return Binding{}, err
	}
	return binding, nil
}

func (coordinator *Coordinator) EnsureHarness(_ context.Context, sessionID, harnessID string, _ terminals.Dimensions) (Binding, error) {
	id, runtimeID, ok := coordinator.products.HarnessTerminalBinding(sessionID, harnessID)
	if !ok {
		return Binding{}, terminals.ErrNotFound
	}
	return Binding{TerminalID: id, OwnerKind: terminals.OwnerHarness, OwnerID: harnessID, SessionID: sessionID, RuntimeID: runtimeID}, nil
}

func (coordinator *Coordinator) PrepareHarnessRelaunch(ctx context.Context, sessionID, harnessID string, dimensions terminals.Dimensions) (Binding, error) {
	return coordinator.EnsureHarness(ctx, sessionID, harnessID, dimensions)
}

func (coordinator *Coordinator) Resolve(id terminals.TerminalID) (Binding, error) {
	binding, ok := coordinator.products.ResolveTerminalBinding(id)
	if !ok {
		return Binding{}, terminals.ErrNotFound
	}
	return binding, nil
}

func (coordinator *Coordinator) Attach(ctx context.Context, id terminals.TerminalID) (*Attachment, error) {
	binding, err := coordinator.Resolve(id)
	if err != nil {
		return nil, err
	}

	coordinator.mu.Lock()
	previous := coordinator.attachments[id]
	delete(coordinator.attachments, id)
	coordinator.mu.Unlock()
	if previous != nil {
		previous.once.Do(func() { close(previous.retired) })
		_ = coordinator.gateway.Detach(ctx, previous.binding.RuntimeID, id, previous.Restore.Attachment)
	}

	live, err := coordinator.gateway.Attach(ctx, binding.RuntimeID, id)
	if err != nil {
		// A product binding can become visible before its process is launched.
		// Inventory reconciliation is the authority that turns a genuinely
		// missing host process into durable product loss, so keep attachment
		// retries temporary while the binding still exists.
		if errors.Is(err, terminals.ErrProcessLost) {
			return nil, terminals.ErrConnectionUnavailable
		}
		return nil, err
	}
	if live.Restore.Validate() != nil || live.Restore.Checkpoint.TerminalID != id {
		_ = coordinator.gateway.Detach(ctx, binding.RuntimeID, id, live.Restore.Attachment)
		return nil, errors.New("terminal host returned an invalid restore barrier")
	}
	retired := make(chan struct{})
	attachment := &Attachment{
		Restore: live.Restore, Updates: live.Updates, Retired: retired,
		binding: binding, retired: retired,
	}
	coordinator.mu.Lock()
	coordinator.attachments[id] = attachment
	coordinator.mu.Unlock()
	return attachment, nil
}

func (coordinator *Coordinator) Detach(ctx context.Context, id terminals.TerminalID, attachment *Attachment) error {
	if !coordinator.current(id, attachment) {
		return nil
	}
	coordinator.mu.Lock()
	if coordinator.attachments[id] == attachment {
		delete(coordinator.attachments, id)
	}
	coordinator.mu.Unlock()
	attachment.once.Do(func() { close(attachment.retired) })
	return coordinator.gateway.Detach(ctx, attachment.binding.RuntimeID, id, attachment.Restore.Attachment)
}

func (coordinator *Coordinator) Input(ctx context.Context, id terminals.TerminalID, attachment *Attachment, data []byte) error {
	if !coordinator.current(id, attachment) {
		return terminals.ErrStaleAttachment
	}
	return coordinator.gateway.Input(ctx, attachment.binding.RuntimeID, id, attachment.Restore.Attachment, data)
}

func (coordinator *Coordinator) Resize(ctx context.Context, id terminals.TerminalID, attachment *Attachment, revision uint64, dimensions terminals.Dimensions) error {
	if !coordinator.current(id, attachment) {
		return terminals.ErrStaleAttachment
	}
	return coordinator.gateway.Resize(ctx, attachment.binding.RuntimeID, id, attachment.Restore.Attachment, revision, dimensions)
}

func (coordinator *Coordinator) CloseAttached(ctx context.Context, id terminals.TerminalID, attachment *Attachment) error {
	if !coordinator.current(id, attachment) {
		return terminals.ErrStaleAttachment
	}
	return coordinator.gateway.Close(ctx, attachment.binding.RuntimeID, id, attachment.Restore.Attachment)
}

func (coordinator *Coordinator) CloseTerminal(ctx context.Context, id terminals.TerminalID) error {
	binding, err := coordinator.Resolve(id)
	if err != nil {
		return err
	}
	closeErr := ignoreTerminalGone(coordinator.gateway.Close(ctx, binding.RuntimeID, id, terminals.TerminalAttachment{}))
	if closeErr != nil {
		return closeErr
	}
	if binding.OwnerKind == terminals.OwnerManual {
		return coordinator.products.RemoveManualTerminal(binding.SessionID, binding.OwnerID)
	}
	return nil
}

func (coordinator *Coordinator) CloseHarness(ctx context.Context, sessionID, harnessID, _ string) error {
	runtimeErr := coordinator.products.CancelHarnessRuntime(sessionID, harnessID)
	id, _, ok := coordinator.products.HarnessTerminalBinding(sessionID, harnessID)
	if !ok {
		return runtimeErr
	}
	return errors.Join(runtimeErr, ignoreTerminalGone(coordinator.CloseTerminal(ctx, id)))
}

func (coordinator *Coordinator) CloseSession(ctx context.Context, sessionID, _ string) error {
	return coordinator.closeSession(ctx, sessionID, false)
}

func (coordinator *Coordinator) RetireSession(ctx context.Context, sessionID, _ string) error {
	return coordinator.closeSession(ctx, sessionID, true)
}

func (coordinator *Coordinator) closeSession(ctx context.Context, sessionID string, retire bool) error {
	harnessIDs, err := coordinator.products.SessionHarnessIDs(sessionID)
	if err != nil {
		return err
	}
	ids, err := coordinator.products.SessionTerminalIDs(sessionID)
	if err != nil {
		return err
	}
	var result error
	for _, harnessID := range harnessIDs {
		if retire {
			result = errors.Join(result, coordinator.products.RetireHarnessRuntime(sessionID, harnessID))
		} else {
			result = errors.Join(result, coordinator.products.CancelHarnessRuntime(sessionID, harnessID))
		}
	}
	for _, id := range ids {
		result = errors.Join(result, ignoreTerminalGone(coordinator.CloseTerminal(ctx, id)))
	}
	return result
}

func (coordinator *Coordinator) ApplyCompletions(_ context.Context, runtimeID string, completions []terminals.ProcessCompletion) ([]terminals.TerminalID, error) {
	acknowledged := make([]terminals.TerminalID, 0, len(completions))
	for _, completion := range completions {
		if !completion.TerminalID.Valid() {
			return acknowledged, errors.New("invalid terminal completion")
		}
		if err := coordinator.products.ApplyTerminalCompletion(runtimeID, completion); err != nil {
			return acknowledged, err
		}
		acknowledged = append(acknowledged, completion.TerminalID)
	}
	return acknowledged, nil
}

func (coordinator *Coordinator) ReconcileInventory(ctx context.Context, runtimeID string, inventory []terminals.TerminalID) error {
	expected, err := coordinator.products.TerminalBindingsForRuntime(runtimeID)
	if err != nil {
		return err
	}
	present := make(map[terminals.TerminalID]struct{}, len(inventory))
	for _, id := range inventory {
		if !id.Valid() {
			return errors.New("invalid terminal inventory")
		}
		present[id] = struct{}{}
	}
	expectedSet := make(map[terminals.TerminalID]struct{}, len(expected))
	var result error
	for _, binding := range expected {
		expectedSet[binding.TerminalID] = struct{}{}
		if _, ok := present[binding.TerminalID]; !ok {
			result = errors.Join(result, coordinator.products.ApplyTerminalLost(runtimeID, binding.TerminalID))
		}
	}
	for _, id := range inventory {
		if _, ok := expectedSet[id]; ok {
			continue
		}
		closeContext, cancel := context.WithTimeout(ctx, 5*time.Second)
		result = errors.Join(result, ignoreTerminalGone(coordinator.gateway.Close(closeContext, runtimeID, id, terminals.TerminalAttachment{})))
		cancel()
	}
	return result
}

func (coordinator *Coordinator) current(id terminals.TerminalID, attachment *Attachment) bool {
	if attachment == nil || attachment.binding.TerminalID != id {
		return false
	}
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	return coordinator.attachments[id] == attachment
}

func ignoreTerminalGone(err error) error {
	if errors.Is(err, terminals.ErrNotFound) || errors.Is(err, terminals.ErrProcessLost) || errors.Is(err, terminals.ErrProcessEnded) || errors.Is(err, terminals.ErrNotRunning) {
		return nil
	}
	return err
}
