package opencode

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/holark-ai/holark/internal/protocol"
)

func TestObserverWriterRecoversAfterFailedAppend(t *testing.T) {
	runtimeDir := filepath.Join(t.TempDir(), "runtime")
	if err := os.Mkdir(runtimeDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := PrepareRuntime(runtimeDir); err != nil {
		t.Fatal(err)
	}
	observer := filepath.Join(SharedConfigDirectory(runtimeDir), "observer.js")
	script := `
import assert from "node:assert/strict";
import { mkdir, readFile, rm, rmdir } from "node:fs/promises";
const { writer } = await import(process.argv[1]);
const append = writer();
const spool = process.env.HOLARK_OPENCODE_EVENTS_PATH;
// A directory at the spool path causes a real, recoverable append failure.
await rm(spool);
await mkdir(spool);
await assert.rejects(append({type:"session_status",session_id:"ses_root",status:"busy"}), {code:"EISDIR"});
await rmdir(spool);
await append({type:"observer_failed"});
assert.equal(await readFile(spool, "utf8"), '{"type":"observer_failed"}\n');
`
	command := exec.Command("node", "--input-type=module", "-e", script, observer)
	command.Env = append(os.Environ(), EventsEnvironment+"="+filepath.Join(runtimeDir, SpoolFileName))
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("observer writer: %v: %s", err, output)
	}
}

func TestObserverNormalizesCompletedRootAssistantContextUsage(t *testing.T) {
	runtimeDir := filepath.Join(t.TempDir(), "runtime")
	if err := os.Mkdir(runtimeDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := PrepareRuntime(runtimeDir); err != nil {
		t.Fatal(err)
	}
	plugin := filepath.Join(SharedConfigDirectory(runtimeDir), PluginsDirectoryName, PluginFileName)
	// Exercise the production observer against captured OpenCode event shapes.
	// This tests Holark's normalizer; it does not replace or impersonate the CLI.
	script := `
const { HolarkObserver } = await import(process.argv[1]);
const observer = await HolarkObserver();
for (const event of [
 {type:"session.created",properties:{info:{id:"ses_root"}}},
 {type:"session.created",properties:{info:{id:"ses_child",parentID:"ses_root"}}},
 {type:"message.updated",properties:{info:{id:"msg_user",sessionID:"ses_root",role:"user",time:{completed:1},tokens:{total:10}}}},
 {type:"message.updated",properties:{info:{id:"msg_incomplete",sessionID:"ses_root",role:"assistant",time:{},tokens:{total:20}}}},
 {type:"message.updated",properties:{info:{id:"msg_root",sessionID:"ses_root",role:"assistant",time:{completed:1},tokens:{input:46,output:21,reasoning:0,cache:{read:8000,write:0},total:8067}}}},
 {type:"message.updated",properties:{info:{id:"msg_root",sessionID:"ses_root",role:"assistant",time:{completed:1},tokens:{total:8067}}}},
 {type:"message.updated",properties:{info:{id:"msg_child",sessionID:"ses_child",role:"assistant",time:{completed:1},tokens:{total:9999}}}},
 {type:"message.updated",properties:{info:{id:"msg_fallback",sessionID:"ses_root",role:"assistant",time:{completed:1},tokens:{input:47,output:21,reasoning:0,cache:{read:9000,write:0}}}}},
]) await observer.event({event});
`
	command := exec.Command("node", "--input-type=module", "-e", script, plugin)
	command.Env = append(os.Environ(), "HOLARK_OPENCODE_OBSERVER=server", EventsEnvironment+"="+filepath.Join(runtimeDir, SpoolFileName))
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("observer: %v: %s", err, output)
	}

	data, err := os.ReadFile(filepath.Join(runtimeDir, SpoolFileName))
	if err != nil {
		t.Fatal(err)
	}
	reducer := NewReducer("ses_root", protocol.InputNone)
	reducer.Apply(Fact{Type: "session_created", SessionID: "ses_child", ParentID: "ses_root"})
	var projected []int64
	for _, line := range bytes.Split(data, []byte{'\n'}) {
		if len(line) == 0 {
			continue
		}
		fact, known, err := DecodeFact(line)
		if err != nil {
			t.Fatal(err)
		}
		if !known || fact.Type != "context_usage" {
			continue
		}
		projection := reducer.Apply(fact)
		if projection.ContextChanged {
			projected = append(projected, *projection.ContextTokens)
		}
	}
	if len(projected) != 2 || projected[0] != 8067 || projected[1] != 9068 {
		t.Fatalf("projected context usage = %v", projected)
	}
}
