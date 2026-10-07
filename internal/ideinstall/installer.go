package ideinstall

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/holark-ai/holark/internal/protocol"
	"golang.org/x/sys/unix"
)

const (
	DefaultMaxDownloadBytes = int64(256 << 20)
	DefaultMaxExpandedBytes = int64(1 << 30)
	officialUpdateBaseURL   = "https://update.code.visualstudio.com"
)

var requiredServeWebOptions = []string{
	"--host",
	"--port",
	"--server-base-path",
	"--default-folder",
	"--server-data-dir",
	"--without-connection-token",
	"--accept-server-license-terms",
	"--disable-telemetry",
}

type Options struct {
	ConfiguredPath   string
	DataDir          string
	GOOS             string
	GOARCH           string
	HTTPClient       *http.Client
	MaxDownloadBytes int64
	MaxExpandedBytes int64
	UpdateBaseURL    string
	Log              io.Writer
}

type Result struct {
	Executable string
	Version    string
	Source     protocol.IDEInstallSource
}

func Resolve(ctx context.Context, options Options) (Result, error) {
	options = defaultOptions(options)
	options.log("Resolving VS Code CLI")
	if strings.TrimSpace(options.DataDir) == "" {
		return Result{}, errors.New("IDE data directory is required")
	}
	if configured := strings.TrimSpace(options.ConfiguredPath); configured != "" {
		if result, err := probe(ctx, configured, protocol.IDEInstallConfigured); err == nil {
			return result, nil
		} else {
			options.log("Configured CLI unavailable: %v", err)
		}
	}
	if executable, err := exec.LookPath("code"); err == nil {
		if result, probeErr := probe(ctx, executable, protocol.IDEInstallPath); probeErr == nil {
			return result, nil
		} else {
			options.log("PATH CLI unavailable: %v", probeErr)
		}
	}
	managed := managedExecutable(options.DataDir)
	if result, err := probe(ctx, managed, protocol.IDEInstallManaged); err == nil {
		return result, nil
	}
	installLock, err := acquireManagedInstallLock(ctx, filepath.Dir(managed))
	if err != nil {
		return Result{}, err
	}
	defer releaseManagedInstallLock(installLock)
	if result, err := probe(ctx, managed, protocol.IDEInstallManaged); err == nil {
		return result, nil
	}
	if err := provision(ctx, options, managed); err != nil {
		return Result{}, err
	}
	return probe(ctx, managed, protocol.IDEInstallDownloaded)
}

func (o Options) log(format string, args ...any) {
	if o.Log != nil {
		fmt.Fprintf(o.Log, "%s %s\n", time.Now().UTC().Format(time.RFC3339), fmt.Sprintf(format, args...))
	}
}

func acquireManagedInstallLock(ctx context.Context, installRoot string) (*os.File, error) {
	if err := os.MkdirAll(installRoot, 0o755); err != nil {
		return nil, fmt.Errorf("create managed IDE directory: %w", err)
	}
	lockFile, err := os.OpenFile(filepath.Join(installRoot, ".install.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open managed IDE install lock: %w", err)
	}
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		if err := unix.Flock(int(lockFile.Fd()), unix.LOCK_EX|unix.LOCK_NB); err == nil {
			return lockFile, nil
		} else if !errors.Is(err, unix.EWOULDBLOCK) {
			_ = lockFile.Close()
			return nil, fmt.Errorf("lock managed IDE installation: %w", err)
		}
		select {
		case <-ctx.Done():
			_ = lockFile.Close()
			return nil, fmt.Errorf("wait for managed IDE installation: %w", ctx.Err())
		case <-ticker.C:
		}
	}
}

func releaseManagedInstallLock(lockFile *os.File) {
	if lockFile == nil {
		return
	}
	_ = unix.Flock(int(lockFile.Fd()), unix.LOCK_UN)
	_ = lockFile.Close()
}

func defaultOptions(options Options) Options {
	if options.GOOS == "" {
		options.GOOS = runtime.GOOS
	}
	if options.GOARCH == "" {
		options.GOARCH = runtime.GOARCH
	}
	if options.HTTPClient == nil {
		options.HTTPClient = &http.Client{Timeout: 2 * time.Minute}
	}
	if options.MaxDownloadBytes <= 0 {
		options.MaxDownloadBytes = DefaultMaxDownloadBytes
	}
	if options.MaxExpandedBytes <= 0 {
		options.MaxExpandedBytes = DefaultMaxExpandedBytes
	}
	if strings.TrimSpace(options.UpdateBaseURL) == "" {
		options.UpdateBaseURL = officialUpdateBaseURL
	}
	return options
}

func managedExecutable(dataDir string) string {
	return filepath.Join(dataDir, "managed", "code")
}

func probe(ctx context.Context, executable string, source protocol.IDEInstallSource) (Result, error) {
	info, err := os.Stat(executable)
	if err != nil || info.IsDir() {
		return Result{}, errors.New("Code executable not found")
	}
	serveWebHelp, err := runProbeCommand(ctx, executable, "serve-web", "--help")
	if err != nil {
		return Result{}, fmt.Errorf("probe Code serve-web support: %w", err)
	}
	for _, option := range requiredServeWebOptions {
		if !strings.Contains(serveWebHelp, option) {
			return Result{}, fmt.Errorf("Code executable does not support required serve-web option %s", option)
		}
	}
	versionOutput, err := runProbeCommand(ctx, executable, "--version")
	if err != nil {
		return Result{}, fmt.Errorf("probe Code executable: %w", err)
	}
	version := ""
	for _, line := range strings.Split(versionOutput, "\n") {
		if strings.TrimSpace(line) != "" {
			version = strings.TrimSpace(line)
			break
		}
	}
	if version == "" {
		return Result{}, errors.New("Code executable returned no version")
	}
	if absolute, err := filepath.Abs(executable); err == nil {
		executable = absolute
	}
	return Result{Executable: executable, Version: version, Source: source}, nil
}

func runProbeCommand(ctx context.Context, executable string, arguments ...string) (string, error) {
	probeContext, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	command := exec.CommandContext(probeContext, executable, arguments...)
	output := &limitedBuffer{maximum: 8192}
	command.Stdout = output
	command.Stderr = output
	if err := command.Run(); err != nil {
		return "", fmt.Errorf("%w: %s", err, strings.TrimSpace(output.String()))
	}
	return output.String(), nil
}

func provision(ctx context.Context, options Options, destination string) error {
	platform, err := downloadPlatform(options.GOOS, options.GOARCH)
	if err != nil {
		return err
	}
	installRoot := filepath.Dir(destination)
	if err := os.MkdirAll(installRoot, 0o755); err != nil {
		return fmt.Errorf("create managed IDE directory: %w", err)
	}
	staging, err := os.MkdirTemp(installRoot, ".install-")
	if err != nil {
		return fmt.Errorf("create IDE staging directory: %w", err)
	}
	defer os.RemoveAll(staging)

	downloadURL := strings.TrimRight(options.UpdateBaseURL, "/") + "/latest/" + platform + "/stable"
	options.log("Downloading Code CLI: %s", downloadURL)
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, downloadURL, nil)
	if err != nil {
		return fmt.Errorf("create Code download request: %w", err)
	}
	response, err := options.HTTPClient.Do(request)
	if err != nil {
		return fmt.Errorf("download Code CLI: %w", err)
	}
	defer response.Body.Close()
	options.log("Download response: %s, content type: %s", response.Status, response.Header.Get("Content-Type"))
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("download Code CLI: server returned %s", response.Status)
	}
	if response.ContentLength > options.MaxDownloadBytes {
		return errors.New("Code CLI download exceeds size limit")
	}
	archivePath := filepath.Join(staging, "code.archive")
	archive, err := os.OpenFile(archivePath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("create Code CLI archive: %w", err)
	}
	written, copyErr := io.Copy(archive, io.LimitReader(response.Body, options.MaxDownloadBytes+1))
	closeErr := archive.Close()
	if copyErr != nil {
		return fmt.Errorf("download Code CLI: %w", copyErr)
	}
	if closeErr != nil {
		return fmt.Errorf("store Code CLI archive: %w", closeErr)
	}
	if written > options.MaxDownloadBytes {
		return errors.New("Code CLI download exceeds size limit")
	}

	archive, err = os.Open(archivePath)
	if err != nil {
		return fmt.Errorf("open Code CLI archive: %w", err)
	}
	payload := filepath.Join(staging, "payload")
	options.log("Extracting Code CLI (%d downloaded bytes)", written)
	extractErr := extractArchive(archive, payload, options.MaxExpandedBytes)
	_ = archive.Close()
	if extractErr != nil {
		return fmt.Errorf("extract Code CLI: %w", extractErr)
	}
	executable, err := findCodeExecutable(payload)
	if err != nil {
		return err
	}
	if err := os.Chmod(executable, 0o755); err != nil {
		return fmt.Errorf("make Code CLI executable: %w", err)
	}
	temporary := filepath.Join(staging, "code-new")
	if err := copyFileExclusive(executable, temporary, 0o755); err != nil {
		return fmt.Errorf("stage managed Code CLI: %w", err)
	}
	if err := os.Rename(temporary, destination); err != nil {
		return fmt.Errorf("install managed Code CLI: %w", err)
	}
	options.log("Installed Code CLI: %s", destination)
	return nil
}

func downloadPlatform(goos, goarch string) (string, error) {
	switch goos + "/" + goarch {
	case "linux/amd64":
		return "cli-linux-x64", nil
	case "linux/arm64":
		return "cli-linux-arm64", nil
	case "darwin/amd64":
		return "cli-darwin-x64", nil
	case "darwin/arm64":
		return "cli-darwin-arm64", nil
	default:
		return "", fmt.Errorf("VS Code web IDE is unsupported on %s/%s", goos, goarch)
	}
}

func ExtractTarGzip(reader io.Reader, destination string, maximumBytes int64) error {
	if maximumBytes <= 0 {
		return errors.New("archive expansion limit is required")
	}
	if err := os.MkdirAll(destination, 0o755); err != nil {
		return err
	}
	root, err := filepath.Abs(destination)
	if err != nil {
		return err
	}
	compressed, err := gzip.NewReader(reader)
	if err != nil {
		return err
	}
	defer compressed.Close()
	archive := tar.NewReader(compressed)
	var expanded int64
	for {
		header, err := archive.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		name := filepath.FromSlash(header.Name)
		if header.Name == "" || filepath.IsAbs(name) || strings.Contains(header.Name, "\\") {
			return errors.New("Code CLI archive contains an unsafe path")
		}
		for _, component := range strings.Split(filepath.ToSlash(name), "/") {
			if component == ".." {
				return errors.New("Code CLI archive contains an unsafe path")
			}
		}
		cleanName := filepath.Clean(name)
		if cleanName == "." {
			continue
		}
		target := filepath.Join(root, cleanName)
		relative, err := filepath.Rel(root, target)
		if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return errors.New("Code CLI archive escapes extraction directory")
		}
		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg, tar.TypeRegA:
			if header.Size < 0 || expanded > maximumBytes-header.Size {
				return errors.New("Code CLI archive exceeds expansion limit")
			}
			expanded += header.Size
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			mode := os.FileMode(header.Mode) & 0o777
			if mode == 0 {
				mode = 0o644
			}
			file, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
			if err != nil {
				return err
			}
			_, copyErr := io.CopyN(file, archive, header.Size)
			closeErr := file.Close()
			if copyErr != nil {
				return copyErr
			}
			if closeErr != nil {
				return closeErr
			}
		default:
			return errors.New("Code CLI archive contains unsupported entries")
		}
	}
}

func findCodeExecutable(root string) (string, error) {
	var result string
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		if entry.Type().IsRegular() && entry.Name() == "code" {
			if result != "" {
				return errors.New("Code CLI archive contains multiple executables")
			}
			result = path
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	if result == "" {
		return "", errors.New("Code CLI archive does not contain the code executable")
	}
	return result, nil
}

func copyFileExclusive(source, destination string, mode os.FileMode) error {
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	output, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(output, input)
	closeErr := output.Close()
	if copyErr != nil {
		_ = os.Remove(destination)
		return copyErr
	}
	if closeErr != nil {
		_ = os.Remove(destination)
		return closeErr
	}
	return nil
}

type limitedBuffer struct {
	data    []byte
	maximum int
}

func (buffer *limitedBuffer) Write(data []byte) (int, error) {
	original := len(data)
	remaining := buffer.maximum - len(buffer.data)
	if remaining > 0 {
		if len(data) > remaining {
			data = data[:remaining]
		}
		buffer.data = append(buffer.data, data...)
	}
	return original, nil
}

func (buffer *limitedBuffer) String() string {
	return string(buffer.data)
}
