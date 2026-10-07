package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/holark-ai/holark/internal/holons"
	"github.com/holark-ai/holark/internal/localapp"
	"github.com/holark-ai/holark/internal/terminalhost"
	"github.com/holark-ai/holark/internal/terminals"
	"github.com/holark-ai/holark/internal/testfixture/pullrequestfixture"
)

type fixtureState struct {
	HolonID      string `json:"holon_id"`
	WorktreePath string `json:"worktree_path"`
}

type fixtureApplication struct {
	state                                 fixtureState
	dataDir, repositoryPath, trackingGate string
	scenario                              fixtureScenario
	pullRequestFlow                       pullRequestFlow
	frontend                              fs.FS
	mu                                    sync.RWMutex
	app                                   *localapp.Application
	attachmentMu                          sync.Mutex
	attachmentCounts                      map[string]int
	databaseMu                            sync.Mutex
	databaseGate                          *sql.Conn
	databaseGateRun                       string
	pullRequestScenario                   *pullrequestfixture.Scenario
	harnessScenario                       *harnessScenario
	handler                               http.Handler
}

func main() {
	listen := flag.String("listen", "127.0.0.1:18180", "HTTP listen address")
	static := flag.String("static-dir", "internal/localshell/frontend/dist", "built frontend directory")
	scenario := flag.String("scenario", "", "development scenario to seed")
	pullRequestFlowFlag := flag.String("pull-request-flow", "", "manual pull request flow to run")
	open := flag.Bool("open-browser", false, "open the fixture in the default browser")
	flag.Parse()
	if err := validateLoopback(*listen); err != nil {
		log.Fatal(err)
	}
	selectedScenario := fixtureScenario(*scenario)
	selectedFlow := pullRequestFlow(*pullRequestFlowFlag)
	if err := validatePullRequestFlow(selectedScenario, selectedFlow); err != nil {
		log.Fatal(err)
	}
	app, err := newFixtureApplicationForScenarioAndFlow(*static, selectedScenario, selectedFlow)
	if err != nil {
		log.Fatal(err)
	}
	defer app.close()
	listener, err := net.Listen("tcp", *listen)
	if err != nil {
		log.Fatal(err)
	}
	server := &http.Server{Handler: app.handler, ReadHeaderTimeout: 5 * time.Second}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() { <-ctx.Done(); _ = server.Shutdown(context.Background()) }()
	baseURL := "http://" + listener.Addr().String()
	launchURL := baseURL
	if app.scenario == fixtureScenarioHarnessVersions {
		launchURL += "/__e2e/harness-playground"
	}
	if app.scenario == fixtureScenarioPullRequestCreation {
		launchURL += "/holons/" + url.PathEscape(app.state.HolonID)
	}
	log.Printf("Holark browser E2E fixture listening on %s", baseURL)
	if app.scenario == fixtureScenarioHarnessVersions {
		log.Printf("Harness playground: %s", launchURL)
	}
	if app.pullRequestScenario != nil {
		log.Print(app.pullRequestScenario.Instructions)
	}
	if *open {
		go func() {
			if openBrowser(launchURL); err != nil {
				log.Printf("Open browser: %v", err)
			}
		}()
	}
	if err = server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}

func openBrowser(target string) error {
	var command *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		command = exec.Command("open", target)
	case "windows":
		command = exec.Command("rundll32", "url.dll,FileProtocolHandler", target)
	default:
		command = exec.Command("xdg-open", target)
	}
	if output, err := command.CombinedOutput(); err != nil {
		return fmt.Errorf("%s: %w: %s", command.Path, err, strings.TrimSpace(string(output)))
	}
	return nil
}

func validateLoopback(address string) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	if host == "localhost" {
		return nil
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return errors.New("listen address must be loopback")
	}
	return nil
}

func validatePullRequestFlow(scenario fixtureScenario, flow pullRequestFlow) error {
	if scenario == fixtureScenarioPullRequestCreation {
		if !flow.valid() {
			return fmt.Errorf("unknown pull request flow %q", flow)
		}
		return nil
	}
	if flow != "" {
		return errors.New("pull-request-flow requires the pull-request-creation scenario")
	}
	return nil
}

func newFixtureApplication(staticDirectory string) (*fixtureApplication, error) {
	return newFixtureApplicationForScenario(staticDirectory, fixtureScenarioDefault)
}

func newFixtureApplicationForScenario(staticDirectory string, scenario fixtureScenario) (*fixtureApplication, error) {
	flow := pullRequestFlow("")
	if scenario == fixtureScenarioPullRequestCreation {
		flow = pullRequestFlowHappyPath
	}
	return newFixtureApplicationForScenarioAndFlow(staticDirectory, scenario, flow)
}

func newFixtureApplicationForScenarioAndFlow(staticDirectory string, scenario fixtureScenario, flow pullRequestFlow) (*fixtureApplication, error) {
	if !scenario.valid() {
		return nil, fmt.Errorf("unknown browser fixture scenario %q", scenario)
	}
	if err := validatePullRequestFlow(scenario, flow); err != nil {
		return nil, err
	}
	dataDir, err := os.MkdirTemp("", "holark-browser-e2e-")
	if err != nil {
		return nil, err
	}
	fail := func(e error) (*fixtureApplication, error) { _ = os.RemoveAll(dataDir); return nil, e }
	repositoryPath := filepath.Join(dataDir, "repository")
	if scenario == fixtureScenarioDefault || scenario == fixtureScenarioHarnessVersions {
		if err = writeFixtureRepository(repositoryPath); err != nil {
			return fail(err)
		}
	}
	trackingGate := filepath.Join(dataDir, "tracking-gate")
	if err = os.MkdirAll(trackingGate, 0o700); err != nil {
		return fail(err)
	}
	if err = os.Setenv("HOLARK_XTERM_E2E_CONTROL_DIR", trackingGate); err != nil {
		return fail(err)
	}
	app := &fixtureApplication{dataDir: dataDir, repositoryPath: repositoryPath, trackingGate: trackingGate, scenario: scenario, pullRequestFlow: flow, frontend: os.DirFS(staticDirectory), attachmentCounts: map[string]int{}}
	if scenario == fixtureScenarioHarnessVersions {
		app.harnessScenario = newHarnessScenario()
	}
	if err = app.start(true); err != nil {
		app.close()
		return nil, err
	}
	mux := http.NewServeMux()
	if app.harnessScenario != nil {
		mux.HandleFunc("GET /__e2e/harness-playground", harnessPlaygroundHandler)
		mux.HandleFunc("GET /__e2e/harness-inputs", app.harnessScenario.inputsHandler)
		mux.HandleFunc("PUT /__e2e/harness-inputs", app.harnessScenario.inputsHandler)
	}
	mux.HandleFunc("GET /__e2e/state", app.stateHandler)
	mux.HandleFunc("POST /__e2e/restart-holark", app.restartHandler)
	mux.HandleFunc("POST /__e2e/forget-terminal/{holonID}/{recordID}", app.forgetTerminalHandler)
	mux.HandleFunc("GET /__e2e/terminal-attachments/{terminalID}", app.attachmentCountHandler)
	mux.HandleFunc("GET /__e2e/terminal-progress/{terminalID}", app.progressHandler)
	mux.HandleFunc("POST /__e2e/database/block/{runID}", app.blockDatabaseHandler)
	mux.HandleFunc("POST /__e2e/database/release/{runID}", app.releaseDatabaseHandler)
	mux.HandleFunc("POST /__e2e/tracking/pause", app.pauseTrackingHandler)
	mux.HandleFunc("POST /__e2e/tracking/release", app.releaseTrackingHandler)
	mux.Handle("/", http.HandlerFunc(app.serve))
	app.handler = mux
	return app, nil
}

func (f *fixtureApplication) start(seed bool) error {
	if f.scenario == fixtureScenarioPullRequestCreation {
		open := pullrequestfixture.OpenBrowserScenario
		if seed {
			open = pullrequestfixture.NewBrowserScenario
		}
		delay := 10 * time.Second
		if f.pullRequestFlow == pullRequestFlowActionPriority {
			delay = 5 * time.Second
		}
		scenario, err := open(context.Background(), f.dataDir, pullrequestfixture.BrowserOptions{
			CompletionDelay: delay,
			Flow:            f.pullRequestFlow.fixtureFlow(),
			State:           pullrequestfixture.State{HolonID: f.state.HolonID, WorktreePath: f.state.WorktreePath},
		})
		if err != nil {
			return err
		}
		f.mu.Lock()
		f.app, f.pullRequestScenario = scenario.Application, scenario
		if seed {
			f.state = fixtureState{HolonID: scenario.State.HolonID, WorktreePath: scenario.State.WorktreePath}
		}
		f.mu.Unlock()
		return nil
	}
	options := localapp.Options{RepositoryPath: f.repositoryPath, HomeDirectory: filepath.Join(f.dataDir, "app"), Terminal: terminalhost.Options{MaximumTerminals: 1026, DetachedTailBytes: 1024}}
	if f.harnessScenario != nil {
		options.HarnessDiscovery = f.harnessScenario
	}
	application, err := localapp.New(context.Background(), options)
	if err != nil {
		return err
	}
	f.mu.Lock()
	f.app = application
	f.mu.Unlock()
	if !seed {
		return nil
	}
	head, err := gitOutput(f.repositoryPath, "rev-parse", "HEAD")
	if err != nil {
		return err
	}
	holon, err := application.Holons.Create(context.Background(), holons.Create{Title: "Browser terminal smoke", Kind: holons.KindNormal, BaseBranch: "main", BaseCommit: strings.TrimSpace(head)})
	if err != nil {
		return err
	}
	f.state = fixtureState{HolonID: holon.ID, WorktreePath: holon.WorktreePath}
	return nil
}

func (f *fixtureApplication) serve(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/launch.js" {
		w.Header().Set("Content-Type", "application/javascript")
		launchScript := "window.__HOLARK_LAUNCH_READY__ = Promise.resolve();\n"
		if f.pullRequestFlow == pullRequestFlowRebaseClickConflict {
			launchScript += rebaseClickConflictLaunchScript
		}
		_, _ = w.Write([]byte(launchScript))
		return
	}
	if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/attach") {
		parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
		if len(parts) >= 7 {
			f.attachmentMu.Lock()
			f.attachmentCounts[parts[len(parts)-2]]++
			f.attachmentMu.Unlock()
		}
	}
	f.mu.RLock()
	application := f.app
	pullRequestScenario := f.pullRequestScenario
	f.mu.RUnlock()
	if strings.HasPrefix(r.URL.Path, "/api/") {
		if pullRequestScenario != nil && r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/rebase") {
			if err := pullRequestScenario.PrepareNextRebase(); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
		}
		application.Handler.ServeHTTP(w, r)
		return
	}
	path := strings.TrimPrefix(filepath.Clean(r.URL.Path), "/")
	if path != "." {
		if _, err := fs.Stat(f.frontend, path); err == nil {
			http.FileServer(http.FS(f.frontend)).ServeHTTP(w, r)
			return
		}
	}
	r.URL.Path = "/"
	http.FileServer(http.FS(f.frontend)).ServeHTTP(w, r)
}

func (f *fixtureApplication) restartHandler(w http.ResponseWriter, _ *http.Request) {
	f.mu.Lock()
	old := f.app
	oldScenario := f.pullRequestScenario
	f.mu.Unlock()
	closeApplication := old.Close
	if oldScenario != nil {
		closeApplication = oldScenario.Close
	}
	if err := closeApplication(); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	if err := f.start(false); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
func (f *fixtureApplication) stateHandler(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(f.state)
}
func (f *fixtureApplication) forgetTerminalHandler(w http.ResponseWriter, r *http.Request) {
	f.mu.RLock()
	application := f.app
	f.mu.RUnlock()
	if _, err := application.Holons.SetManualTerminalBinding(r.Context(), r.PathValue("holonID"), r.PathValue("recordID"), ""); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
func (f *fixtureApplication) attachmentCountHandler(w http.ResponseWriter, r *http.Request) {
	f.attachmentMu.Lock()
	count := f.attachmentCounts[r.PathValue("terminalID")]
	f.attachmentMu.Unlock()
	_, _ = fmt.Fprint(w, count)
}
func (f *fixtureApplication) progressHandler(w http.ResponseWriter, r *http.Request) {
	f.mu.RLock()
	application := f.app
	f.mu.RUnlock()
	progress, ok := application.Manager.Progress(terminals.TerminalID(r.PathValue("terminalID")))
	if !ok {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(progress)
}
func (f *fixtureApplication) blockDatabaseHandler(w http.ResponseWriter, r *http.Request) {
	runID := r.PathValue("runID")
	if !validRunID(runID) {
		http.Error(w, "invalid run ID", 400)
		return
	}
	f.databaseMu.Lock()
	defer f.databaseMu.Unlock()
	if f.databaseGate != nil {
		http.Error(w, "database already blocked", 409)
		return
	}
	f.mu.RLock()
	database := f.app.Database
	f.mu.RUnlock()
	connection, err := database.Conn(r.Context())
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	if _, err = connection.ExecContext(r.Context(), "begin exclusive"); err != nil {
		connection.Close()
		http.Error(w, err.Error(), 500)
		return
	}
	f.databaseGate, f.databaseGateRun = connection, runID
	_ = os.WriteFile(filepath.Join(f.state.WorktreePath, ".terminal-database-"+runID+"-blocked"), nil, 0o600)
	w.WriteHeader(http.StatusNoContent)
}
func (f *fixtureApplication) releaseDatabaseHandler(w http.ResponseWriter, r *http.Request) {
	f.databaseMu.Lock()
	defer f.databaseMu.Unlock()
	if f.databaseGate == nil || f.databaseGateRun != r.PathValue("runID") {
		http.Error(w, "database not blocked by run", 409)
		return
	}
	_, _ = f.databaseGate.ExecContext(r.Context(), "rollback")
	_ = f.databaseGate.Close()
	f.databaseGate = nil
	f.databaseGateRun = ""
	w.WriteHeader(http.StatusNoContent)
}
func (f *fixtureApplication) pauseTrackingHandler(w http.ResponseWriter, _ *http.Request) {
	if err := os.WriteFile(filepath.Join(f.trackingGate, "paused"), nil, 0o600); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	w.WriteHeader(204)
}
func (f *fixtureApplication) releaseTrackingHandler(w http.ResponseWriter, _ *http.Request) {
	if err := os.Remove(filepath.Join(f.trackingGate, "paused")); err != nil && !errors.Is(err, os.ErrNotExist) {
		http.Error(w, err.Error(), 500)
		return
	}
	w.WriteHeader(204)
}

func (f *fixtureApplication) close() {
	f.databaseMu.Lock()
	if f.databaseGate != nil {
		_ = f.databaseGate.Close()
		f.databaseGate = nil
	}
	f.databaseMu.Unlock()
	f.mu.Lock()
	application := f.app
	pullRequestScenario := f.pullRequestScenario
	f.app = nil
	f.pullRequestScenario = nil
	f.mu.Unlock()
	if pullRequestScenario != nil {
		_ = pullRequestScenario.Close()
	} else if application != nil {
		_ = application.Close()
	}
	_ = os.Unsetenv("HOLARK_XTERM_E2E_CONTROL_DIR")
	_ = os.RemoveAll(f.dataDir)
}

func writeFixtureRepository(path string) error {
	if err := os.MkdirAll(path, 0o700); err != nil {
		return err
	}
	files := map[string]string{"browser-terminal-smoke-marker": "ok\n", "terminal-slow-browser-scenario.sh": slowBrowserScenario, "terminal-tracking-failure-scenario.sh": trackingFailureScenario, "terminal-tracking-quality-scenario.sh": trackingQualityScenario, "terminal-resize-delivery-scenario.sh": resizeDeliveryScenario, "terminal-navigation-scenario.sh": navigationScenario, "terminal-render-stall-scenario.sh": renderStallScenario, "terminal-synchronized-output-race-scenario.sh": synchronizedOutputRaceScenario, "terminal-unicode-width-scenario.sh": unicodeWidthScenario, "terminal-semantic-restore-scenario.sh": semanticRestoreScenario, "terminal-ownership-scenario.sh": ownershipScenario}
	for name, contents := range files {
		mode := fs.FileMode(0o700)
		if name == "browser-terminal-smoke-marker" {
			mode = 0o600
		}
		if err := os.WriteFile(filepath.Join(path, name), []byte(contents), mode); err != nil {
			return err
		}
	}
	commands := [][]string{{"init", "-b", "main"}, {"add", "."}, {"-c", "user.name=Holark Browser E2E", "-c", "user.email=browser-e2e@invalid", "commit", "-m", "fixture"}}
	for _, args := range commands {
		if _, err := gitOutput(path, args...); err != nil {
			return err
		}
	}
	return nil
}
func gitOutput(directory string, args ...string) (string, error) {
	command := exec.Command("git", args...)
	command.Dir = directory
	output, err := command.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %v: %w: %s", args, err, output)
	}
	return string(output), nil
}
func validRunID(value string) bool {
	if value == "" || len(value) > 64 {
		return false
	}
	for _, r := range value {
		if !(r >= 'a' && r <= 'z') && !(r >= 'A' && r <= 'Z') && !(r >= '0' && r <= '9') {
			return false
		}
	}
	return true
}
