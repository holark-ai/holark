package opencode

import (
	"bytes"
	"crypto/sha256"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/tailscale/hujson"
)

const (
	PluginsDirectoryName = "plugins"
	PluginFileName       = "holark-observer.js"
	SpoolFileName        = "events.jsonl"
	ConfigEnvironment    = "OPENCODE_CONFIG_DIR"
	EventsEnvironment    = "HOLARK_OPENCODE_EVENTS_PATH"
)

//go:embed plugin.js
var pluginSource []byte

//go:embed observer.js
var observerSource []byte

//go:embed terminal.js
var terminalSource []byte

//go:embed plugin_v2.js
var pluginV2Source []byte

//go:embed observer_v2.js
var observerV2Source []byte

//go:embed terminal_v2.js
var terminalV2Source []byte

var tuiConfig = []byte(`{"plugin":["./terminal.js"]}`)

// V2 discovers the TUI half through the server's combined plugin inventory.
// Its local plugin loader requires a directory with a tui.js entrypoint;
// a path to an individual JavaScript file is silently skipped.
// Inject this directory through config content so the global config root and
// all user configuration paths remain under OpenCode's control.
var serverEntryV2 = []byte(`export { default } from "./plugins/holark-observer.js"`)

func observerConfigContent(runtimeDir, version string) (string, error) {
	config := map[string]json.RawMessage{}
	if content := os.Getenv("OPENCODE_CONFIG_CONTENT"); content != "" {
		data, err := hujson.Standardize([]byte(content))
		if err != nil {
			return "", fmt.Errorf("invalid OpenCode config content: %w", err)
		}
		if err := json.Unmarshal(data, &config); err != nil {
			return "", fmt.Errorf("invalid OpenCode config content: %w", err)
		}
		if config == nil {
			return "", errors.New("OpenCode config content must be an object")
		}
	}
	var plugins []json.RawMessage
	if data, ok := config["plugins"]; ok {
		if err := json.Unmarshal(data, &plugins); err != nil {
			return "", fmt.Errorf("invalid OpenCode config plugins: %w", err)
		}
	}
	plugin, err := json.Marshal(SharedConfigDirectory(runtimeDir, version))
	if err != nil {
		return "", err
	}
	config["plugins"], err = json.Marshal(append(plugins, plugin))
	if err != nil {
		return "", err
	}
	data, err := json.Marshal(config)
	return string(data), err
}

type observerAsset struct {
	name string
	data []byte
}

// observerAssets selects the observer bundle for the installed version. An
// empty or unparseable version selects the validated v1 bundle. Common observer
// filenames stay stable; configuration and entrypoints differ by version.
func observerAssets(versions ...string) []observerAsset {
	if IsV2Version(firstVersion(versions)) {
		return []observerAsset{
			{filepath.Join(PluginsDirectoryName, PluginFileName), pluginV2Source},
			{"observer.js", observerV2Source}, {"tui.js", terminalV2Source},
			{"server.js", serverEntryV2},
		}
	}
	return []observerAsset{
		{filepath.Join(PluginsDirectoryName, PluginFileName), pluginSource},
		{"observer.js", observerSource}, {"terminal.js", terminalSource}, {"tui.json", tuiConfig},
	}
}

func firstVersion(versions []string) string {
	if len(versions) == 0 {
		return ""
	}
	return versions[0]
}

func ObserverPlugin() []byte {
	return append([]byte(nil), pluginSource...)
}

// ObserverPluginForVersion returns the server plugin source matching the
// installed version; unknown versions keep the validated v1 source.
func ObserverPluginForVersion(version string) []byte {
	if IsV2Version(version) {
		return append([]byte(nil), pluginV2Source...)
	}
	return ObserverPlugin()
}

func PrepareRuntime(runtimeDir string, versions ...string) error {
	if !validRuntimeDir(runtimeDir) {
		return errors.New("invalid OpenCode runtime directory")
	}
	entries, err := os.ReadDir(runtimeDir)
	if err != nil {
		return err
	}
	if len(entries) != 0 {
		return errors.New("OpenCode runtime directory is not empty")
	}
	if err := os.Chmod(runtimeDir, 0o700); err != nil {
		return err
	}
	if err := prepareSharedConfig(runtimeDir, firstVersion(versions)); err != nil {
		return err
	}
	spool, err := os.OpenFile(filepath.Join(runtimeDir, SpoolFileName), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	return spool.Close()
}

// SharedConfigDirectory keeps OpenCode's plugin dependencies across conversations.
// Only the observer code and its dependencies are shared; event spools remain in
// each agent's private runtime. A changed observer gets a new immutable location.
// The hash covers the version-selected assets, so v1 and v2 installations get
// different directories and never share a bundle.
func SharedConfigDirectory(runtimeDir string, versions ...string) string {
	hash := sha256.New()
	for _, asset := range observerAssets(versions...) {
		fmt.Fprintf(hash, "%s:%d:", asset.name, len(asset.data))
		hash.Write(asset.data)
	}
	return filepath.Join(filepath.Dir(runtimeDir), "opencode-config", fmt.Sprintf("%x", hash.Sum(nil)))
}

func prepareSharedConfig(runtimeDir, version string) error {
	pluginsDir := filepath.Join(SharedConfigDirectory(runtimeDir, version), PluginsDirectoryName)
	if err := os.MkdirAll(pluginsDir, 0o700); err != nil {
		return err
	}
	for _, asset := range observerAssets(version) {
		if err := publishObserverAsset(filepath.Join(SharedConfigDirectory(runtimeDir, version), asset.name), asset.data); err != nil {
			return err
		}
	}
	return nil
}

func publishObserverAsset(path string, source []byte) error {
	if data, err := os.ReadFile(path); err == nil {
		if !bytes.Equal(data, source) {
			return errors.New("shared OpenCode observer has unexpected contents")
		}
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	// Publish complete assets atomically across concurrent Holark processes.
	file, err := os.CreateTemp(filepath.Dir(path), ".observer-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if _, err := file.Write(source); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(file.Name(), path)
}

func validRuntimeDir(runtimeDir string) bool {
	if !cleanAbsolutePath(runtimeDir) {
		return false
	}
	info, err := os.Lstat(runtimeDir)
	return err == nil && info.IsDir() && info.Mode()&os.ModeSymlink == 0
}

func cleanAbsolutePath(path string) bool {
	return filepath.IsAbs(path) && filepath.Clean(path) == path
}
