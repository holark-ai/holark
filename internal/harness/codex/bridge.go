package codex

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/coder/websocket"
	"github.com/holark-ai/holark/internal/protocol"
	"github.com/pelletier/go-toml/v2"
)

func cutEnv(s string) (string, string, bool) { return strings.Cut(s, "=") }
func isHolarkEnvironment(s string) bool {
	return strings.HasPrefix(s, "HOLARK_") && s != "HOLARK_CODEX_BRIDGE_TOKEN"
}

type threadSnapshot struct {
	ID           string         `json:"id"`
	Parent       string         `json:"parentThreadId"`
	ForkedFromID string         `json:"forkedFromId"`
	Turns        []turnSnapshot `json:"turns"`
}
type turnSnapshot struct {
	ID     string `json:"id"`
	Status string `json:"status"`
	Items  []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"items"`
}
type threadResponse struct {
	Thread threadSnapshot `json:"thread"`
}

func canBindThread(method, previous, startSource string, thread threadSnapshot) bool {
	if thread.ID == "" || thread.Parent != "" {
		return false
	}
	return previous == "" || thread.ID == previous || startSource == "clear" ||
		(method == "thread/fork" && thread.ForkedFromID == previous)
}

func (b *launchBinding) lockReduction() {
	b.reductionMu.Lock()
	b.mu.Lock()
}
func (b *launchBinding) unlockReduction() {
	b.mu.Unlock()
	b.reductionMu.Unlock()
}

func (b *launchBinding) publish(o Observation) {
	if o.Status == protocol.ObservabilityDegraded {
		o.Activity = protocol.ActivityUnknown
	}
	// Callers hold mu for reduction, but persistence can synchronously stop this
	// very agent (for example after importing metadata). Never hold mu across it.
	sink := b.sink
	if sink != nil && b.ctx.Err() == nil && !b.stopping {
		b.mu.Unlock()
		sink(o)
		b.mu.Lock()
	}
}
func (b *launchBinding) apply(f Fact) error {
	if err := b.ctx.Err(); err != nil {
		return err
	}
	f.Launch = b.generation
	f.Connection = b.connection
	p := b.reducer.Apply(f)
	if p.Identity != "" {
		err := errors.New("Codex identity persistence was not acknowledged")
		b.publish(Observation{Identity: p.Identity, Acknowledge: func(result error) { err = result }})
		if err != nil {
			return err
		}
	}
	if p.Changed {
		b.publish(Observation{Input: p.Input, Activity: p.Activity, TurnID: f.TurnID})
	}
	if p.ContextChanged {
		b.publish(Observation{ContextTokens: p.ContextTokens})
	}
	return nil
}
func (b *launchBinding) configure(ctx context.Context, observer *rpcConn, params map[string]any, inheritSavedPermissions bool) error {
	var effective struct {
		Config map[string]any `json:"config"`
	}
	if err := observer.call(ctx, "config/read", map[string]any{"cwd": b.cwd, "includeLayers": false}, &effective); err != nil {
		return err
	}
	config := effective.Config
	if config == nil {
		config = map[string]any{}
	}
	if err := b.applyProfile(config); err != nil {
		return err
	}

	// Native explicit settings take precedence over the effective project config.
	if native, ok := params["config"].(map[string]any); ok {
		for k, v := range native {
			config[k] = v
		}
	}
	if inheritSavedPermissions {
		// Resume/fork keep the conversation's model. The full config/read
		// response must not reintroduce a current default as an override.
		delete(config, "model")
		delete(params, "model")
		// Remote resume and fork restore the thread's saved permissions. Codex
		// deliberately omits these overrides from the native request; do not add
		// them back from the full effective config/read response.
		for _, key := range []string{
			"approval_policy",
			"approvals_reviewer",
			"default_permissions",
			"network",
			"permissions",
			"sandbox_mode",
			"sandbox_workspace_write",
		} {
			delete(config, key)
		}
		for _, key := range []string{"approvalPolicy", "approvalsReviewer", "permissions", "sandbox"} {
			delete(params, key)
		}
	}
	delete(config, "profile")
	policy, _ := config["shell_environment_policy"].(map[string]any)
	if policy == nil {
		policy = map[string]any{}
	}
	set, _ := policy["set"].(map[string]any)
	if set == nil {
		set = map[string]any{}
	}
	policy["set"] = b.toolEnvironment(set)
	config["shell_environment_policy"] = policy
	delete(config, "shell_environment_policy.set")
	if servers, ok := config["mcp_servers"].(map[string]any); ok {
		for _, value := range servers {
			if server, ok := value.(map[string]any); ok {
				// Only stdio MCP servers launch a process. HTTP transports reject env.
				if command, _ := server["command"].(string); command == "" {
					continue
				}
				env, _ := server["env"].(map[string]any)
				if env == nil {
					env = map[string]any{}
				}
				server["env"] = b.toolEnvironment(env)
			}
		}
	}
	params["cwd"] = b.cwd
	params["runtimeWorkspaceRoots"] = []string{b.cwd}
	pruneNullConfig(config)
	params["config"] = config
	// Path/history must never override the durable API identity on resume.
	delete(params, "path")
	delete(params, "history")
	return nil
}

// toolEnvironment preserves configured tool values while adding the launch
// identity and the composition's CLI discovery settings. Prefix the effective
// PATH for each tool, falling back to the app-server's inherited PATH.
func (b *launchBinding) toolEnvironment(env map[string]any) map[string]any {
	for k, v := range b.env {
		env[k] = v
	}
	path, ok := env["PATH"].(string)
	if !ok {
		path = os.Getenv("PATH")
	}
	discovery := b.manager.terminalContext
	for _, entry := range discovery.Environment([]string{"PATH=" + path}) {
		name, value, _ := cutEnv(entry)
		if name == "HOLARK_RUNTIME_DIR" || (name == "PATH" && strings.TrimSpace(discovery.ExecutablePath) != "") {
			env[name] = value
		}
	}
	return env
}

func (b *launchBinding) bridge(down *websocket.Conn, up *rpcConn, url, token string) {
	ctx, cancel := context.WithCancel(b.ctx)
	defer cancel()
	select {
	case <-b.ready:
	case <-time.After(20 * time.Second):
		return
	case <-ctx.Done():
		return
	}
	b.lockReduction()
	observer := b.observer
	b.unlockReduction()
	var err error
	if observer == nil {
		observer, err = connectRPC(b.ctx, url, token)
		if err != nil {
			return
		}
		if err = observer.initialize(ctx); err != nil {
			observer.close()
			return
		}
		b.lockReduction()
		b.observer = observer
		b.connection++
		b.unlockReduction()
		go b.observe(observer, url, token)
	}
	requests := make(chan rpcMessage, 64)
	go func() {
		defer cancel()
		for {
			_, data, err := down.Read(ctx)
			if err != nil {
				return
			}
			var msg rpcMessage
			if json.Unmarshal(data, &msg) != nil {
				return
			}
			select {
			case requests <- msg:
			case <-ctx.Done():
				return
			}
		}
	}()
	pending := map[string]rpcMessage{}
	for {
		select {
		case <-ctx.Done():
			return
		case msg := <-requests:
			// Preserve native mode changes even while the observer is reconnecting.
			// These are bound terminal requests, never guessed from plan text.
			if msg.Method == "turn/start" || msg.Method == "thread/settings/update" || msg.Method == "turn/settings/update" {
				var settings struct {
					ThreadID          string `json:"threadId"`
					CollaborationMode struct {
						Mode string `json:"mode"`
					} `json:"collaborationMode"`
				}
				if json.Unmarshal(msg.Params, &settings) == nil && settings.CollaborationMode.Mode != "" {
					b.lockReduction()
					b.apply(Fact{Kind: "settings", ThreadID: settings.ThreadID, Mode: settings.CollaborationMode.Mode})
					b.unlockReduction()
				}
			}
			if msg.Method == "config/read" {
				if len(pending) >= 64 {
					return
				}
				pending[string(msg.ID)] = msg
			}
			if msg.Method == "thread/start" || msg.Method == "thread/resume" || msg.Method == "thread/fork" {
				if len(pending) >= 64 {
					return
				}
				var params map[string]any
				if json.Unmarshal(msg.Params, &params) != nil || params == nil {
					return
				}
				if err = b.configure(ctx, up, params, msg.Method != "thread/start"); err != nil {
					return
				}
				if msg.Method == "thread/start" {
					params["historyMode"] = "legacy"
				}
				msg.Params, _ = json.Marshal(params)
				pending[string(msg.ID)] = msg
			}
			if err = up.write(ctx, msg); err != nil {
				return
			}
		case msg, ok := <-up.events:
			if !ok {
				return
			}
			if len(msg.Error) > 0 {
				msg.Error = b.redactError(msg.Error, token)
			}
			if request, exists := pending[string(msg.ID)]; exists && msg.Method == "" {
				delete(pending, string(msg.ID))

				if len(msg.Error) > 0 {
					var failure struct {
						Message string `json:"message"`
					}
					_ = json.Unmarshal(msg.Error, &failure)
					b.lockReduction()
					b.publish(Observation{Status: protocol.ObservabilityDegraded, Message: failure.Message})
					b.unlockReduction()
				}
				if request.Method == "config/read" && len(msg.Error) == 0 {
					var response map[string]any
					if json.Unmarshal(msg.Result, &response) != nil {
						return
					}
					config, ok := response["config"].(map[string]any)
					if !ok {
						return
					}
					if err = b.applyProfile(config); err != nil {
						return
					}
					msg.Result, _ = json.Marshal(response)
				} else if len(msg.Error) == 0 {
					var result threadResponse
					if json.Unmarshal(msg.Result, &result) != nil || result.Thread.ID == "" {
						return
					}
					var params map[string]any
					_ = json.Unmarshal(request.Params, &params)
					b.lockReduction()
					previous := b.reducer.ThreadID
					observer = b.observer
					b.unlockReduction()
					startSource, _ := params["sessionStartSource"].(string)
					if canBindThread(request.Method, previous, startSource, result.Thread) {
						if request.Method == "thread/start" {
							if err = observer.call(ctx, "thread/name/set", map[string]string{"threadId": result.Thread.ID, "name": "Holark " + b.agent}, nil); err != nil {
								return
							}
						}
						// Hold reduction until the imported history is marked and identity has
						// synchronously reached the persistence sink, before releasing the TUI.
						b.lockReduction()
						err = observer.call(ctx, "thread/resume", map[string]any{"threadId": result.Thread.ID, "excludeTurns": true}, nil)
						if err == nil {
							err = b.apply(Fact{Kind: "binding", ThreadID: result.Thread.ID, Predecessor: previous, Verified: true})
							if !b.bound || previous != result.Thread.ID {
								for _, turn := range result.Thread.Turns {
									b.apply(Fact{ThreadID: result.Thread.ID, TurnID: turn.ID, Baseline: true})
								}
								b.bound = true
							} else {
								b.reconcile(result.Thread)
							}
							b.publish(Observation{Status: protocol.ObservabilityHealthy, Activity: b.reducer.Activity})
						}
						b.unlockReduction()
						if err != nil {
							return
						}
					}
				}
			}
			data, _ := json.Marshal(msg)
			writeCtx, stop := context.WithTimeout(ctx, 10*time.Second)
			err = down.Write(writeCtx, websocket.MessageText, data)
			stop()
			if err != nil {
				return
			}
		}
	}
}

func (b *launchBinding) redactError(raw json.RawMessage, upstreamToken string) json.RawMessage {
	var value any
	if json.Unmarshal(raw, &value) != nil {
		return json.RawMessage(`{"code":-32603,"message":"Codex request failed"}`)
	}
	var redact func(any) any
	redact = func(v any) any {
		switch v := v.(type) {
		case string:
			for _, secret := range b.env {
				if secret != "" {
					v = strings.ReplaceAll(v, secret, "[redacted]")
				}
			}
			v = strings.ReplaceAll(v, upstreamToken, "[redacted]")
			return strings.ReplaceAll(v, b.token, "[redacted]")
		case map[string]any:
			for k, nested := range v {
				v[k] = redact(nested)
			}
			return v
		case []any:
			for i, nested := range v {
				v[i] = redact(nested)
			}
			return v
		default:
			return v
		}
	}
	result, _ := json.Marshal(redact(value))
	return result
}

func (b *launchBinding) observeMessage(c *rpcConn, msg rpcMessage) bool {
	b.lockReduction()
	if b.observer != c {
		b.unlockReduction()
		return false
	}
	b.reduceMessage(msg)
	b.unlockReduction()
	return true
}

func (b *launchBinding) observe(c *rpcConn, url, token string) {
	defer func() { c.close() }()
	for {
		for msg := range c.events {
			if !b.observeMessage(c, msg) {
				return
			}
		}
		if b.ctx.Err() != nil {
			return
		}
		b.lockReduction()
		if b.observer != c {
			b.unlockReduction()
			return
		}
		b.publish(Observation{Status: protocol.ObservabilityDegraded, Message: "Codex observation disconnected; reconnecting."})
		b.unlockReduction()
		recovered := false
		for delay := 100 * time.Millisecond; !recovered; {
			select {
			case <-b.ctx.Done():
				return
			case <-time.After(delay):
			}
			next, err := connectRPC(b.ctx, url, token)
			if err == nil {
				err = next.initialize(b.ctx)
			}
			if err == nil {
				b.lockReduction()
				b.connection++
				b.observer = next
				thread := b.reducer.ThreadID
				b.unlockReduction()
				if thread != "" {
					err = next.call(b.ctx, "thread/resume", map[string]any{"threadId": thread, "excludeTurns": true}, nil)
					if err == nil {
						// A loaded thread's listener sends the resume response before
						// replaying pending requests. A second resume on that same
						// listener responds after the first replay has been queued.
						// thread/read alone is not a replay boundary. Duplicate replay
						// is harmless because requests retain their stable IDs.
						err = next.call(b.ctx, "thread/resume", map[string]any{"threadId": thread, "excludeTurns": true}, nil)
					}
					var snapshot threadResponse
					if err == nil {
						err = next.call(b.ctx, "thread/read", map[string]any{"threadId": thread, "includeTurns": true}, &snapshot)
					}
					if err == nil {
						b.lockReduction()
						b.reconcile(snapshot.Thread)
						b.unlockReduction()
					}
				}
				if err == nil {
					// The socket reader queues subscription replay while resume/read
					// run. Process that buffered prefix before retiring old requests;
					// later live events continue through the normal observer loop.
					for queued := len(next.events); queued > 0; queued-- {
						msg, ok := <-next.events
						if !ok || !b.observeMessage(next, msg) {
							err = errors.New("Codex observation disconnected during recovery")
							break
						}
					}
					if next.ctx.Err() != nil {
						err = next.ctx.Err()
					}
				}
				if err == nil {
					c.close()
					c = next
					recovered = true
					b.lockReduction()
					b.apply(Fact{Kind: "requests_recovered", ThreadID: thread})
					b.publish(Observation{Status: protocol.ObservabilityHealthy, Activity: b.reducer.Activity})
					b.unlockReduction()
					break
				}
			}
			if next != nil {
				next.close()
			}
			if delay < 3*time.Second {
				delay *= 2
			}
		}
	}
}
func (b *launchBinding) reconcile(thread threadSnapshot) {
	// History is ordered. Skip turns preceding the current turn, then establish
	// each newer turn before its completion, including entirely missed turns.
	// Publishing each reducer transition preserves the rearm and completion
	// semantics even when reconciliation starts and ends in the same display state.
	start := 0
	for i, turn := range thread.Turns {
		if turn.ID == b.reducer.turn {
			start = i
			break
		}
	}
	for _, turn := range thread.Turns[start:] {
		b.apply(Fact{Kind: "turn_started", ThreadID: thread.ID, TurnID: turn.ID})
		for _, item := range turn.Items {
			if item.Type == "plan" && strings.TrimSpace(item.Text) != "" {
				b.apply(Fact{Kind: "plan_completed", ThreadID: thread.ID, TurnID: turn.ID, Plan: true})
			}
		}
		if turn.Status == "inProgress" {
			b.apply(Fact{Kind: "turn_started", ThreadID: thread.ID, TurnID: turn.ID})
		} else {
			b.apply(Fact{Kind: "turn_completed", ThreadID: thread.ID, TurnID: turn.ID, Status: turn.Status})
		}
	}
}
func (b *launchBinding) reduceMessage(m rpcMessage) {
	var p struct {
		ThreadID       string          `json:"threadId"`
		TurnID         string          `json:"turnId"`
		RequestID      json.RawMessage `json:"requestId"`
		Turn           turnSnapshot    `json:"turn"`
		ThreadSettings struct {
			CollaborationMode struct {
				Mode string `json:"mode"`
			} `json:"collaborationMode"`
		} `json:"threadSettings"`
		Item struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"item"`
		TokenUsage struct {
			Last struct {
				TotalTokens *int64 `json:"totalTokens"`
			} `json:"last"`
		} `json:"tokenUsage"`
	}
	if json.Unmarshal(m.Params, &p) != nil {
		return
	}
	f := Fact{ThreadID: p.ThreadID, TurnID: p.TurnID, RequestID: string(m.ID)}
	switch m.Method {
	case "thread/tokenUsage/updated":
		f.Kind = "context_usage"
		f.ContextTokens = p.TokenUsage.Last.TotalTokens
	case "thread/settings/updated":
		f.Kind = "settings"
		f.Mode = p.ThreadSettings.CollaborationMode.Mode
	case "turn/started":
		f.Kind = "turn_started"
		f.TurnID = p.Turn.ID
	case "turn/completed":
		f.Kind = "turn_completed"
		f.TurnID = p.Turn.ID
		f.Status = p.Turn.Status
	case "item/completed":
		if p.Item.Type != "plan" {
			return
		}
		f.Kind = "plan_completed"
		f.Plan = strings.TrimSpace(p.Item.Text) != ""
	case "item/tool/requestUserInput", "mcpServer/elicitation/request":
		f.Kind = "question"
	case "item/commandExecution/requestApproval", "item/fileChange/requestApproval", "item/permissions/requestApproval":
		f.Kind = "permission"
	case "serverRequest/resolved":
		f.Kind = "resolved"
		f.RequestID = string(p.RequestID)
	default:
		return
	}
	b.apply(f)
}

func mergeConfig(dst, src map[string]any) {
	for k, v := range src {
		if nested, ok := v.(map[string]any); ok {
			if old, ok := dst[k].(map[string]any); ok {
				mergeConfig(old, nested)
				continue
			}
		}
		dst[k] = v
	}
}

func (b *launchBinding) applyProfile(config map[string]any) error {
	if b.profile == "" {
		return nil
	}
	home, err := resolveCodexHome()
	if err != nil {
		return err
	}
	contents, err := os.ReadFile(filepath.Join(home, b.profile+".config.toml"))
	if err != nil {
		return err
	}
	var profile map[string]any
	if err = toml.Unmarshal(contents, &profile); err != nil {
		return err
	}
	mergeConfig(config, profile)
	return nil
}

// config/read includes absent optional values as JSON null, whereas thread
// configuration is TOML and has no null value. Hook definitions contain nested
// arrays, so visit their entries as well as nested tables.
func pruneNullConfig(value any) {
	switch config := value.(type) {
	case map[string]any:
		for key, value := range config {
			if value == nil {
				delete(config, key)
				continue
			}
			pruneNullConfig(value)
		}
	case []any:
		for _, value := range config {
			pruneNullConfig(value)
		}
	}
}
