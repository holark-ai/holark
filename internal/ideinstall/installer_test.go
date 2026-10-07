package ideinstall

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/holark-ai/holark/internal/protocol"
)

func TestResolveUsesConfiguredExecutableBeforeDownload(t *testing.T) {
	t.Setenv("PATH", "")
	executable := writeCodeFixture(t, t.TempDir(), "configured")
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		t.Fatal("configured executable should avoid download")
		return nil, errors.New("unexpected download")
	})}
	result, err := Resolve(t.Context(), Options{
		ConfiguredPath: executable, DataDir: t.TempDir(), GOOS: "linux", GOARCH: "amd64", HTTPClient: client,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Executable != executable || result.Version != "configured" || result.Source != protocol.IDEInstallConfigured {
		t.Fatalf("resolved = %+v", result)
	}
}

func TestResolveDownloadsAtomicallyThenReusesManagedCLI(t *testing.T) {
	t.Setenv("PATH", "")
	for _, platform := range []struct{ os, arch, format, download string }{
		{"linux", "amd64", "gzip", "cli-linux-x64"},
		{"darwin", "arm64", "zip", "cli-darwin-arm64"},
	} {
		t.Run(platform.download, func(t *testing.T) {
			entries := map[string]tarEntry{
				"cli/":     {typeflag: tar.TypeDir, mode: 0o755},
				"cli/code": {body: codeFixtureScript("1.99.0"), mode: 0o755},
			}
			archive := codeArchive(t, entries)
			if platform.format == "zip" {
				archive = codeZIPArchive(t, entries)
			}

			var requestedPath string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requestedPath = r.URL.Path
				w.Header().Set("Content-Type", "application/gzip")
				_, _ = w.Write(archive)
			}))
			defer server.Close()
			dataDir := t.TempDir()
			result, err := Resolve(t.Context(), Options{
				DataDir: dataDir, GOOS: platform.os, GOARCH: platform.arch, UpdateBaseURL: server.URL,
			})
			if err != nil {
				t.Fatal(err)
			}
			if result.Source != protocol.IDEInstallDownloaded || result.Version != "1.99.0" {
				t.Fatalf("downloaded result = %+v", result)
			}
			if requestedPath != "/latest/"+platform.download+"/stable" {
				t.Fatalf("download path = %q", requestedPath)
			}
			info, err := os.Stat(result.Executable)
			if err != nil || info.Mode()&0o111 == 0 {
				t.Fatalf("managed executable = %+v, err=%v", info, err)
			}

			result, err = Resolve(t.Context(), Options{
				DataDir: dataDir, GOOS: platform.os, GOARCH: platform.arch,
				HTTPClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
					t.Fatal("managed executable should avoid download")
					return nil, errors.New("unexpected download")
				})},
			})
			if err != nil {
				t.Fatal(err)
			}
			if result.Source != protocol.IDEInstallManaged || result.Version != "1.99.0" {
				t.Fatalf("managed result = %+v", result)
			}
		})
	}
}

func TestResolveConcurrentProvisioningUsesOneManagedInstall(t *testing.T) {
	t.Setenv("PATH", "")
	archive := codeArchive(t, map[string]tarEntry{
		"cli/code": {body: codeFixtureScript("1.99.0"), mode: 0o755},
	})
	const callers = 8
	var requests atomic.Int32
	allRequests := make(chan struct{})
	releaseDownload := make(chan struct{})
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		if requests.Add(1) == callers {
			close(allRequests)
		}
		<-releaseDownload
		return &http.Response{
			StatusCode:    http.StatusOK,
			Status:        http.StatusText(http.StatusOK),
			ContentLength: int64(len(archive)),
			Body:          io.NopCloser(bytes.NewReader(archive)),
		}, nil
	})}

	type outcome struct {
		result Result
		err    error
	}
	dataDir := t.TempDir()
	start := make(chan struct{})
	results := make(chan outcome, callers)
	var ready sync.WaitGroup
	ready.Add(callers)
	for range callers {
		go func() {
			ready.Done()
			<-start
			result, err := Resolve(t.Context(), Options{
				DataDir: dataDir, GOOS: "linux", GOARCH: "amd64", HTTPClient: client,
			})
			results <- outcome{result: result, err: err}
		}()
	}
	ready.Wait()
	close(start)
	select {
	case <-allRequests:
	case <-time.After(250 * time.Millisecond):
	}
	close(releaseDownload)

	downloaded := 0
	for range callers {
		outcome := <-results
		if outcome.err != nil {
			t.Errorf("Resolve() error = %v", outcome.err)
			continue
		}
		if outcome.result.Version != "1.99.0" || outcome.result.Executable != managedExecutable(dataDir) {
			t.Errorf("Resolve() = %+v", outcome.result)
		}
		if outcome.result.Source == protocol.IDEInstallDownloaded {
			downloaded++
		} else if outcome.result.Source != protocol.IDEInstallManaged {
			t.Errorf("Resolve() source = %q", outcome.result.Source)
		}
	}
	if got := requests.Load(); got != 1 {
		t.Errorf("download requests = %d, want 1", got)
	}
	if downloaded != 1 {
		t.Errorf("downloaded results = %d, want 1", downloaded)
	}
}

func TestResolveSkipsPathExecutableWithoutServeWeb(t *testing.T) {
	pathDirectory := t.TempDir()
	pathExecutable := filepath.Join(pathDirectory, "code")
	if err := os.WriteFile(pathExecutable, []byte("#!/bin/sh\nif [ \"$1\" = \"--version\" ]; then printf 'desktop-1.0\\n'; else printf 'desktop options only\\n'; fi\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", pathDirectory)

	archive := codeArchive(t, map[string]tarEntry{
		"code": {body: codeFixtureScript("standalone-1.0"), mode: 0o755},
	})
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++
		w.Header().Set("Content-Type", "application/gzip")
		_, _ = w.Write(archive)
	}))
	defer server.Close()

	result, err := Resolve(t.Context(), Options{
		DataDir: t.TempDir(), GOOS: "linux", GOARCH: "amd64", UpdateBaseURL: server.URL,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Source != protocol.IDEInstallDownloaded || result.Version != "standalone-1.0" {
		t.Fatalf("resolved incompatible PATH executable: %+v", result)
	}
	if result.Executable == pathExecutable || requests != 1 {
		t.Fatalf("path executable = %q, result = %+v, download requests = %d", pathExecutable, result, requests)
	}
}

func TestResolveRejectsUnsupportedPlatformWithoutDownloading(t *testing.T) {
	t.Setenv("PATH", "")
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		t.Fatal("unsupported platform should not download")
		return nil, errors.New("unexpected download")
	})}
	_, err := Resolve(t.Context(), Options{
		DataDir: t.TempDir(), GOOS: "plan9", GOARCH: "amd64", HTTPClient: client,
	})
	if err == nil || !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("unsupported platform error = %v", err)
	}
}

func TestExtractTarGzipAllowsDirectoriesAndRejectsUnsafeEntries(t *testing.T) {
	valid := codeArchive(t, map[string]tarEntry{
		"folder/":         {typeflag: tar.TypeDir, mode: 0o755},
		"folder/code":     {body: "binary", mode: 0o755},
		"folder/data.txt": {body: "data", mode: 0o644},
	})
	destination := t.TempDir()
	if err := ExtractTarGzip(bytes.NewReader(valid), destination, 1024); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(destination, "folder", "code"))
	if err != nil || string(data) != "binary" {
		t.Fatalf("extracted code = %q, err=%v", data, err)
	}

	for name, archive := range map[string][]byte{
		"traversal": codeArchive(t, map[string]tarEntry{"../escape": {body: "bad"}}),
		"symlink":   codeArchive(t, map[string]tarEntry{"code": {typeflag: tar.TypeSymlink, linkname: "/tmp/escape"}}),
		"oversized": codeArchive(t, map[string]tarEntry{"code": {body: strings.Repeat("x", 32)}}),
	} {
		t.Run(name, func(t *testing.T) {
			limit := int64(1024)
			if name == "oversized" {
				limit = 8
			}
			if err := ExtractTarGzip(bytes.NewReader(archive), t.TempDir(), limit); err == nil {
				t.Fatal("expected unsafe archive to fail")
			}
		})
	}
}

type tarEntry struct {
	body     string
	mode     int64
	typeflag byte
	linkname string
}

func codeArchive(t *testing.T, entries map[string]tarEntry) []byte {
	t.Helper()
	var compressed bytes.Buffer
	gzipWriter := gzip.NewWriter(&compressed)
	archive := tar.NewWriter(gzipWriter)
	for name, entry := range entries {
		typeflag := entry.typeflag
		if typeflag == 0 {
			typeflag = tar.TypeReg
		}
		mode := entry.mode
		if mode == 0 {
			mode = 0o644
		}
		header := &tar.Header{Name: name, Mode: mode, Typeflag: typeflag, Linkname: entry.linkname}
		if typeflag == tar.TypeReg {
			header.Size = int64(len(entry.body))
		}
		if err := archive.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
		if typeflag == tar.TypeReg {
			if _, err := io.WriteString(archive, entry.body); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	return compressed.Bytes()
}

func writeCodeFixture(t *testing.T, directory, version string) string {
	t.Helper()
	path := filepath.Join(directory, "code")
	if err := os.WriteFile(path, []byte(codeFixtureScript(version)), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func codeFixtureScript(version string) string {
	return "#!/bin/sh\n" +
		"if [ \"$1\" = \"--version\" ]; then printf '" + version + "\\ncommit\\nx64\\n'; exit 0; fi\n" +
		"if [ \"$1\" = \"serve-web\" ] && [ \"$2\" = \"--help\" ]; then\n" +
		"  printf '%s\\n' '--host --port --server-base-path --default-folder --server-data-dir --without-connection-token --accept-server-license-terms --disable-telemetry'\n" +
		"  exit 0\n" +
		"fi\n" +
		"exit 2\n"
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func codeZIPArchive(t *testing.T, entries map[string]tarEntry) []byte {
	t.Helper()
	var data bytes.Buffer
	writer := zip.NewWriter(&data)
	for name, entry := range entries {
		header := &zip.FileHeader{Name: name, Method: zip.Deflate}
		mode := os.FileMode(entry.mode)
		if entry.typeflag == tar.TypeDir {
			mode |= os.ModeDir
		}
		if entry.typeflag == tar.TypeSymlink {
			mode |= os.ModeSymlink
		}
		header.SetMode(mode)
		file, err := writer.CreateHeader(header)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(file, entry.body); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return data.Bytes()
}

func TestResolveRejectsUnsafeZIPAndUnexpectedDownloads(t *testing.T) {
	t.Setenv("PATH", "")
	for name, archive := range map[string][]byte{
		"traversal": codeZIPArchive(t, map[string]tarEntry{"../code": {body: "bad"}}),
		"absolute":  codeZIPArchive(t, map[string]tarEntry{"/code": {body: "bad"}}),
		"symlink":   codeZIPArchive(t, map[string]tarEntry{"code": {typeflag: tar.TypeSymlink, body: "/tmp/code"}}),
		"oversized": codeZIPArchive(t, map[string]tarEntry{"code": {body: strings.Repeat("x", 32)}}),
		"html":      []byte("<html>Download unavailable</html>"),
	} {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(archive) }))
			defer server.Close()
			dataDir := t.TempDir()
			_, err := Resolve(t.Context(), Options{DataDir: dataDir, GOOS: "darwin", GOARCH: "arm64", UpdateBaseURL: server.URL, MaxExpandedBytes: 8})
			if err == nil {
				t.Fatal("expected download rejection")
			}
			if name == "html" && !strings.Contains(err.Error(), "not a ZIP or gzip archive") {
				t.Fatal(err)
			}
			if _, err := os.Stat(managedExecutable(dataDir)); !os.IsNotExist(err) {
				t.Fatalf("failed download installed executable: %v", err)
			}
		})
	}
}
