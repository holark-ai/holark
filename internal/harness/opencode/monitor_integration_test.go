//go:build integration

package opencode

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/holark-ai/holark/internal/protocol"
)

func TestOpenCodeMonitorRecoveryAndCorruption(t *testing.T) {
	t.Run("late initialization recovers", testMonitorTimeoutContinuesAndLateInitializationRecovers)
	t.Run("terminal corruption prevents false completion", testMonitorCorruptionStopsConsumptionWithoutFalseCompletion)
}

func TestOpenCodeMonitorExecutionLifecycle(t *testing.T) {
	for _, version := range []string{"1.18.25", "2.0.8"} {
		for _, outcome := range []struct {
			name     string
			activity protocol.AgentActivity
			input    protocol.InputState
		}{
			{"succeeded", protocol.ActivityCompleted, protocol.InputTaskComplete},
			{"failed", protocol.ActivityFailed, protocol.InputNone},
			{"interrupted", protocol.ActivityIdle, protocol.InputNone},
		} {
			t.Run(version+"/"+outcome.name, func(t *testing.T) {
				runtimeDir := opencodeTestDirectory(t, "runtime")
				if err := PrepareRuntime(runtimeDir, version); err != nil {
					t.Fatal(err)
				}
				plugin := filepath.Join(SharedConfigDirectory(runtimeDir, version), PluginsDirectoryName, PluginFileName)
				// Exercise the production plugin, spool, and monitor with a known
				// resume target. No CLI is replaced and no creation event is needed.
				script := `
const plugin = await import(process.argv[1]);
const v2 = process.argv[2] === "2.0.8";
const outcome = process.argv[3];
const sessionID = "ses_saved";
const started = v2
 ? {type:"session.execution.started",data:{sessionID}}
 : {type:"session.status",properties:{sessionID,status:{type:"busy"}}};
const terminal = (result) => v2
 ? [{type:"session.execution."+result,data:{sessionID,error:{name:"UnknownError"},reason:"user"}}]
 : [
  ...(result === "succeeded" ? [] : [{type:"session.error",properties:{sessionID,error:{name:result === "interrupted" ? "MessageAbortedError" : "UnknownError"}}}]),
  {type:"session.idle",properties:{sessionID}},
 ];
const events = [
 ...(v2 ? ["started","succeeded","failed","interrupted"].flatMap((type) => [
  {type:"session.execution."+type,data:{}},
  {type:"session.execution."+type,data:{sessionID:42}},
 ]) : []),
 started,
 {type:"permission.asked",properties:{sessionID,id:"permission"}},
 ...terminal(outcome),
 started,
 ...terminal("succeeded"),
];
if (v2) {
 plugin.default.setup({event:{async *subscribe() { yield* events; }}});
} else {
 const observer = await plugin.HolarkObserver();
 for (const event of events) await observer.event({event});
}
`
				command := exec.Command("node", "--input-type=module", "-e", script, plugin, version, outcome.name)
				command.Env = append(os.Environ(), "HOLARK_OPENCODE_OBSERVER=server", EventsEnvironment+"="+filepath.Join(runtimeDir, SpoolFileName))
				if output, err := command.CombinedOutput(); err != nil {
					t.Fatalf("observer: %v: %s", err, output)
				}
				ctx, cancel := context.WithTimeout(t.Context(), time.Second)
				defer cancel()
				var activities []protocol.AgentActivity
				var inputs []protocol.InputState
				MonitorWithOptions(ctx, runtimeDir, "ses_saved", MonitorOptions{PollInterval: 5 * time.Millisecond}, func(event MonitorEvent) {
					if event.Type == MonitorObservability && event.ObservabilityStatus == protocol.ObservabilityDegraded {
						t.Errorf("monitor degraded: %s", event.ObservabilityMessage)
						cancel()
					}
					if event.Type == MonitorActivity || event.Type == MonitorInputState {
						activities = append(activities, event.Activity)
						if len(activities) == 5 {
							cancel()
						}
					}
					if event.Type == MonitorInputState {
						inputs = append(inputs, event.InputState)
					}
				})
				wantActivities := []protocol.AgentActivity{
					protocol.ActivityWorking, protocol.ActivityNeedsInput, outcome.activity,
					protocol.ActivityWorking, protocol.ActivityCompleted,
				}
				wantInputs := []protocol.InputState{protocol.InputPermissionRequired, outcome.input}
				if outcome.input == protocol.InputTaskComplete {
					wantInputs = append(wantInputs, protocol.InputNone)
				}
				wantInputs = append(wantInputs, protocol.InputTaskComplete)
				if !reflect.DeepEqual(activities, wantActivities) || !reflect.DeepEqual(inputs, wantInputs) {
					t.Fatalf("activities = %v, want %v; inputs = %v, want %v", activities, wantActivities, inputs, wantInputs)
				}
			})
		}
	}
}

func TestOpenCodeMonitorQuestions(t *testing.T) {
	for _, version := range []string{"1.18.25", "2.0.8"} {
		t.Run(version, func(t *testing.T) {
			runtimeDir := opencodeTestDirectory(t, "runtime")
			if err := PrepareRuntime(runtimeDir, version); err != nil {
				t.Fatal(err)
			}
			plugin := filepath.Join(SharedConfigDirectory(runtimeDir, version), PluginsDirectoryName, PluginFileName)
			// Replay question events through the production plugin, spool, and
			// monitor, including a child session. No agent CLI is substituted.
			script := `
const plugin = await import(process.argv[1]);
const v2 = process.argv[2] === "2.0.8";
const question = (action, sessionID, id) => v2
 ? {type:"form."+action,data:action === "created" ? {form:{sessionID,id,title:"private question",fields:[]}} : {sessionID,id,answer:{value:"private answer"}}}
 : {type:"question."+({created:"asked",replied:"replied",cancelled:"rejected"}[action]),properties:{sessionID,requestID:id}};
const events = v2 ? [
 {type:"session.created",data:{sessionID:"ses_child",parentID:"ses_root"}},
 {type:"session.execution.started",data:{sessionID:"ses_root"}},
 ...["created","replied","cancelled"].flatMap(action => [
  question(action,"global","frm_global"),
  question(action,undefined,"frm_missing_owner"),
  question(action,42,"frm_invalid_owner"),
  question(action,"ses_root",undefined),
  question(action,"ses_root",42),
  {type:"form."+action,data:{}},
 ]),
] : [
 {type:"session.created",properties:{info:{id:"ses_child",parentID:"ses_root"}}},
 {type:"session.status",properties:{sessionID:"ses_root",status:{type:"busy"}}},
];
for (const [sessionID, close] of [["ses_root","replied"],["ses_child","cancelled"]]) {
 events.push(question("created",sessionID,"frm_question"),question(close,sessionID,"frm_question"));
}
if (v2) {
 plugin.default.setup({event:{async *subscribe() { yield* events; }}});
} else {
 const observer = await plugin.HolarkObserver();
 for (const event of events) await observer.event({event});
}
`
			command := exec.Command("node", "--input-type=module", "-e", script, plugin, version)
			command.Env = append(os.Environ(), "HOLARK_OPENCODE_OBSERVER=server", EventsEnvironment+"="+filepath.Join(runtimeDir, SpoolFileName))
			if output, err := command.CombinedOutput(); err != nil {
				t.Fatalf("observer: %v: %s", err, output)
			}
			data, err := os.ReadFile(filepath.Join(runtimeDir, SpoolFileName))
			if err != nil {
				t.Fatal(err)
			}
			if bytes.Contains(data, []byte("global")) || bytes.Contains(data, []byte("private")) {
				t.Fatalf("spool retained non-session forms or question content: %s", data)
			}
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			var inputs []protocol.InputState
			MonitorWithOptions(ctx, runtimeDir, "ses_root", MonitorOptions{PollInterval: 5 * time.Millisecond}, func(event MonitorEvent) {
				if event.Type == MonitorObservability && event.ObservabilityStatus == protocol.ObservabilityDegraded {
					t.Errorf("monitor degraded: %s", event.ObservabilityMessage)
					cancel()
				}
				if event.Type == MonitorInputState {
					inputs = append(inputs, event.InputState)
					if len(inputs) == 4 {
						cancel()
					}
				}
			})
			want := []protocol.InputState{protocol.InputUserRequired, protocol.InputNone, protocol.InputUserRequired, protocol.InputNone}
			if !reflect.DeepEqual(inputs, want) {
				t.Fatalf("input states = %v, want %v", inputs, want)
			}
		})
	}
}

func TestOpenCodeMonitorReopensCompletedTerminal(t *testing.T) {
	for _, test := range []struct {
		name    string
		saved   protocol.InputState
		request string
		want    protocol.InputState
	}{
		{name: "saved completion", saved: protocol.InputTaskComplete, want: protocol.InputTaskComplete},
		{name: "stale permission", saved: protocol.InputPermissionRequired, want: protocol.InputNone},
		{name: "stale question", saved: protocol.InputUserRequired, want: protocol.InputNone},
		{name: "live permission", saved: protocol.InputTaskComplete, request: "permission_opened", want: protocol.InputPermissionRequired},
		{name: "live question", saved: protocol.InputTaskComplete, request: "question_opened", want: protocol.InputUserRequired},
	} {
		t.Run(test.name, func(t *testing.T) {
			runtimeDir := opencodeTestDirectory(t, "runtime")
			if err := PrepareRuntime(runtimeDir); err != nil {
				t.Fatal(err)
			}
			// Replay terminal hydration on reopen, before any new prompt.
			appendOpenCodeSpool(t, runtimeDir, `{"type":"observer_initialized","status":"terminal"}
{"type":"session_created","session_id":"ses_saved"}
{"type":"session_status","session_id":"ses_saved","status":"idle"}
`)
			if test.request != "" {
				appendOpenCodeSpool(t, runtimeDir, `{"type":"`+test.request+`","session_id":"ses_saved","request_id":"request"}`+"\n")
			}
			appendOpenCodeSpool(t, runtimeDir, `{"type":"session_selected","session_id":"ses_saved"}`+"\n")
			ctx, cancel := context.WithCancel(t.Context())
			events := make(chan MonitorEvent, 32)
			done := make(chan struct{})
			defer func() {
				cancel()
				select {
				case <-done:
				case <-time.After(time.Second):
					t.Error("monitor did not stop")
				}
			}()
			go func() {
				defer close(done)
				MonitorWithOptions(ctx, runtimeDir, "ses_saved", MonitorOptions{
					InitialInputState: test.saved, PollInterval: 5 * time.Millisecond,
				}, func(event MonitorEvent) { events <- event })
			}()
			assertInput := func(want protocol.InputState) {
				t.Helper()
				event := waitOpenCodeMonitorEvent(t, events, func(event MonitorEvent) bool { return event.Type == MonitorInputState })
				if event.InputState != want {
					t.Fatalf("input state = %q, want %q", event.InputState, want)
				}
			}
			assertInput(test.want)
			appendOpenCodeSpool(t, runtimeDir, `{"type":"session_created","session_id":"ses_other"}
{"type":"session_status","session_id":"ses_other","status":"idle"}
{"type":"session_selected","session_id":"ses_other"}
{"type":"session_selected","session_id":"ses_saved"}
`)
			assertInput(protocol.InputNone)
			assertInput(test.want)
			if test.want == protocol.InputTaskComplete {
				appendOpenCodeSpool(t, runtimeDir, `{"type":"session_status","session_id":"ses_saved","status":"busy"}`+"\n")
				assertInput(protocol.InputNone)
			}
		})
	}
}

func TestOpenCodeMonitorDiscoversCreatedRoot(t *testing.T) {
	for _, version := range []string{"1.18.25", "2.0.8"} {
		t.Run(version, func(t *testing.T) {
			runtimeDir := opencodeTestDirectory(t, "runtime")
			if err := PrepareRuntime(runtimeDir, version); err != nil {
				t.Fatal(err)
			}
			plugin := filepath.Join(SharedConfigDirectory(runtimeDir, version), PluginsDirectoryName, PluginFileName)
			// Feed creation events through the production plugin and spool. A child
			// arriving first must retain its parent and never become the resume target.
			script := `
const plugin = await import(process.argv[1]);
const events = process.argv[2] === "2.0.8" ? [
 {type:"session.created",data:{}},
 {type:"session.created",data:{sessionID:42}},
 {type:"session.created",data:{sessionID:"ses_child",parentID:"ses_root"}},
 {type:"session.created",data:{sessionID:"ses_root"}},
] : [
 {type:"session.created",properties:{info:{id:"ses_child",parentID:"ses_root"}}},
 {type:"session.created",properties:{info:{id:"ses_root"}}},
];
if (process.argv[2] === "2.0.8") {
 plugin.default.setup({event:{async *subscribe() { yield* events; }}});
} else {
 const observer = await plugin.HolarkObserver();
 for (const event of events) await observer.event({event});
}
`
			command := exec.Command("node", "--input-type=module", "-e", script, plugin, version)
			command.Env = append(os.Environ(), "HOLARK_OPENCODE_OBSERVER=server", EventsEnvironment+"="+filepath.Join(runtimeDir, SpoolFileName))
			if output, err := command.CombinedOutput(); err != nil {
				t.Fatalf("observer: %v: %s", err, output)
			}
			data, err := os.ReadFile(filepath.Join(runtimeDir, SpoolFileName))
			if err != nil {
				t.Fatal(err)
			}
			var facts []Fact
			for _, line := range bytes.Split(bytes.TrimSpace(data), []byte{'\n'}) {
				fact, known, err := DecodeFact(line)
				if err != nil || !known {
					t.Fatalf("decode fact %s: known=%v err=%v", line, known, err)
				}
				facts = append(facts, fact)
			}
			want := []Fact{
				{Type: "observer_initialized"},
				{Type: "session_created", SessionID: "ses_child", ParentID: "ses_root"},
				{Type: "session_created", SessionID: "ses_root"},
			}
			if !reflect.DeepEqual(facts, want) {
				t.Fatalf("facts = %#v, want %#v", facts, want)
			}
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			var discovered []string
			MonitorWithOptions(ctx, runtimeDir, "", MonitorOptions{PollInterval: 5 * time.Millisecond}, func(event MonitorEvent) {
				if event.Type == MonitorMetadata {
					discovered = append(discovered, event.ResumeTarget)
					cancel()
				}
			})
			if len(discovered) != 1 || discovered[0] != "ses_root" {
				t.Fatalf("discovered = %#v", discovered)
			}
		})
	}
}

func TestOpenCodeMonitorDiscoversParentlessForkAndIgnoresSourceAndChildren(t *testing.T) {
	for _, version := range []string{"1.18.25", "2.0.8"} {
		t.Run(version, func(t *testing.T) {
			testMonitorDiscoversParentlessFork(t, version)
		})
	}
}

func testMonitorDiscoversParentlessFork(t *testing.T, version string) {
	runtimeDir := opencodeTestDirectory(t, "runtime")
	if err := PrepareRuntime(runtimeDir, version); err != nil {
		t.Fatal(err)
	}
	// Both versions create a new parentless root; v2's session.forked parentID
	// identifies copied history, not a child relationship.
	// Exercise the actual observer plugin with external events, then consume its
	// durable facts through the real monitor. No agent CLI is replaced or invoked.
	plugin := filepath.Join(SharedConfigDirectory(runtimeDir, version), PluginsDirectoryName, PluginFileName)
	script := `
const plugin = await import(process.argv[1]);
const events = [
 {type:"session.status",properties:{sessionID:"ses_source",status:{type:"busy"}}},
 ...(process.argv[2] === "2.0.8" ? [
  {type:"session.forked",data:{parentID:"ses_source"}},
  {type:"session.forked",data:{sessionID:42,parentID:"ses_source"}},
  {type:"session.forked",data:{sessionID:"ses_fork",parentID:"ses_source"}},
 ] : [{type:"session.created",properties:{info:{id:"ses_fork"}}}]),
 {type:"session.created",properties:{info:{id:"ses_child",parentID:"ses_fork"}}},
 {type:"session.status",properties:{sessionID:"ses_fork",status:{type:"busy"}}},
 {type:"session.status",properties:{sessionID:"ses_source",status:{type:"idle"}}},
 {type:"session.status",properties:{sessionID:"ses_fork",status:{type:"idle"}}},
];
if (process.argv[2] === "2.0.8") {
 plugin.default.setup({event:{async *subscribe() { yield* events; }}});
} else {
 const observer = await plugin.HolarkObserver();
 for (const event of events) await observer.event({event});
}
`
	command := exec.Command("node", "--input-type=module", "-e", script, plugin, version)
	command.Env = append(os.Environ(), "HOLARK_OPENCODE_OBSERVER=server", EventsEnvironment+"="+filepath.Join(runtimeDir, SpoolFileName))
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("observer: %v: %s", err, output)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events := make(chan MonitorEvent, 16)
	done := make(chan struct{})
	go func() {
		defer close(done)
		MonitorWithOptions(ctx, runtimeDir, "", MonitorOptions{StartupTimeout: time.Second, PollInterval: 5 * time.Millisecond}, func(event MonitorEvent) {
			events <- event
		})
	}()
	var discovered []string
	waitOpenCodeMonitorEvent(t, events, func(event MonitorEvent) bool {
		if event.Type == MonitorMetadata {
			discovered = append(discovered, event.ResumeTarget)
		}
		return event.Type == MonitorInputState && event.InputState == protocol.InputTaskComplete
	})
	if len(discovered) != 1 || discovered[0] != "ses_fork" {
		t.Fatalf("discovered = %#v", discovered)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("monitor did not stop")
	}
}

func testMonitorTimeoutContinuesAndLateInitializationRecovers(t *testing.T) {
	runtimeDir := opencodeTestDirectory(t, "runtime")
	if err := PrepareRuntime(runtimeDir); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events := make(chan MonitorEvent, 16)
	done := make(chan struct{})
	go func() {
		defer close(done)
		MonitorWithOptions(ctx, runtimeDir, "", MonitorOptions{StartupTimeout: 30 * time.Millisecond, PollInterval: 5 * time.Millisecond}, func(event MonitorEvent) {
			events <- event
		})
	}()
	waitOpenCodeMonitorEvent(t, events, func(event MonitorEvent) bool { return event.ObservabilityStatus == protocol.ObservabilityStarting })
	waitOpenCodeMonitorEvent(t, events, func(event MonitorEvent) bool { return event.ObservabilityStatus == protocol.ObservabilityDegraded })
	appendOpenCodeSpool(t, runtimeDir, `{"type":"observer_initialized"}`+"\n"+`{"type":"session_created","session_id":"ses_root"}`+"\n")
	waitOpenCodeMonitorEvent(t, events, func(event MonitorEvent) bool { return event.ObservabilityStatus == protocol.ObservabilityHealthy })
	metadata := waitOpenCodeMonitorEvent(t, events, func(event MonitorEvent) bool { return event.Type == MonitorMetadata })
	if metadata.ResumeTarget != "ses_root" {
		t.Fatalf("metadata = %#v", metadata)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("monitor did not stop")
	}
}

func testMonitorCorruptionStopsConsumptionWithoutFalseCompletion(t *testing.T) {
	tests := []struct {
		name    string
		corrupt func(*testing.T, string)
	}{
		{name: "terminal observer failed", corrupt: func(t *testing.T, runtimeDir string) {
			appendOpenCodeSpool(t, runtimeDir, `{"type":"observer_failed"}`+"\n")
		}},
		{name: "malformed known fact", corrupt: func(t *testing.T, runtimeDir string) {
			appendOpenCodeSpool(t, runtimeDir, `{"type":"session_status","session_id":"ses_root","status":"complete"}`+"\n")
		}},
		{name: "spool shrink", corrupt: func(t *testing.T, runtimeDir string) {
			if err := os.Truncate(filepath.Join(runtimeDir, SpoolFileName), 0); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runtimeDir := opencodeTestDirectory(t, "runtime")
			if err := PrepareRuntime(runtimeDir); err != nil {
				t.Fatal(err)
			}
			appendOpenCodeSpool(t, runtimeDir, `{"type":"observer_initialized"}`+"\n"+`{"type":"session_created","session_id":"ses_root"}`+"\n"+`{"type":"session_status","session_id":"ses_root","status":"busy"}`+"\n")
			events := make(chan MonitorEvent, 16)
			done := make(chan struct{})
			go func() {
				defer close(done)
				MonitorWithOptions(context.Background(), runtimeDir, "", MonitorOptions{StartupTimeout: time.Second, PollInterval: 5 * time.Millisecond}, func(event MonitorEvent) { events <- event })
			}()
			waitOpenCodeMonitorEvent(t, events, func(event MonitorEvent) bool { return event.ObservabilityStatus == protocol.ObservabilityHealthy })
			waitOpenCodeMonitorEvent(t, events, func(event MonitorEvent) bool { return event.Type == MonitorMetadata })
			test.corrupt(t, runtimeDir)
			degraded := waitOpenCodeMonitorEvent(t, events, func(event MonitorEvent) bool { return event.ObservabilityStatus == protocol.ObservabilityDegraded })
			if degraded.ObservabilityMessage == "" {
				t.Fatalf("degraded event = %#v", degraded)
			}
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("corrupt monitor did not terminate")
			}
			appendOpenCodeSpool(t, runtimeDir, `{"type":"session_status","session_id":"ses_root","status":"idle"}`+"\n")
			select {
			case event := <-events:
				if event.Type == MonitorInputState && event.InputState == protocol.InputTaskComplete {
					t.Fatalf("corrupt monitor emitted false completion: %#v", event)
				}
			case <-time.After(30 * time.Millisecond):
			}
		})
	}
}
func opencodeTestDirectory(t *testing.T, name string) string {
	t.Helper()
	directory := filepath.Join(t.TempDir(), name)
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	return directory
}

func appendOpenCodeSpool(t *testing.T, runtimeDir, data string) {
	t.Helper()
	file, err := os.OpenFile(filepath.Join(runtimeDir, SpoolFileName), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString(data); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}

func waitOpenCodeMonitorEvent(t *testing.T, events <-chan MonitorEvent, match func(MonitorEvent) bool) MonitorEvent {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		select {
		case event := <-events:
			if match(event) {
				return event
			}
		case <-deadline:
			t.Fatal("timed out waiting for OpenCode monitor event")
		}
	}
}
