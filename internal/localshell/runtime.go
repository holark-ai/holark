package localshell

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"

	"github.com/holark-ai/holark/internal/localapp"
	"github.com/holark-ai/holark/internal/localconnection"
	"github.com/holark-ai/holark/internal/terminalenv"
)

type Options struct {
	RepositoryPath   string
	ListenAddress    string
	HomeDirectory    string
	RuntimeDirectory string
	Ephemeral        bool
	ExecutablePath   string
	RepositoryID     string
	Stdout           io.Writer
	API              http.Handler
}
type Connection = localconnection.Connection

func DefaultHomeDirectory() (string, error) {
	d, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, ".holark"), nil
}

func DefaultRuntimeDirectory() string {
	return filepath.Join(os.TempDir(), fmt.Sprintf("holark-%d", os.Getuid()))
}

func Run(ctx context.Context, o Options) error {
	if o.Ephemeral {
		return runEphemeral(ctx, o)
	}
	return run(ctx, o)
}

func run(ctx context.Context, o Options) error {
	if o.Stdout == nil {
		o.Stdout = io.Discard
	}
	if o.ListenAddress == "" {
		o.ListenAddress = "127.0.0.1:0"
	}
	host, _, err := net.SplitHostPort(o.ListenAddress)
	if err != nil {
		return err
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("listen address must use an explicit loopback IP")
	}
	if o.HomeDirectory == "" {
		o.HomeDirectory, err = DefaultHomeDirectory()
		if err != nil {
			return err
		}
	}
	if o.RuntimeDirectory == "" {
		o.RuntimeDirectory = DefaultRuntimeDirectory()
	}
	o.RuntimeDirectory, err = filepath.Abs(o.RuntimeDirectory)
	if err != nil {
		return err
	}
	if o.ExecutablePath == "" {
		o.ExecutablePath, err = os.Executable()
		if err != nil {
			return err
		}
	}
	o.ExecutablePath, err = filepath.Abs(o.ExecutablePath)
	if err != nil {
		return err
	}
	if resolved, resolveErr := filepath.EvalSymlinks(o.ExecutablePath); resolveErr == nil {
		o.ExecutablePath = resolved
	}
	terminalContext := terminalenv.Context{ExecutablePath: o.ExecutablePath, RuntimeDirectory: o.RuntimeDirectory}
	if o.API == nil {
		application, openErr := localapp.New(ctx, localapp.Options{RepositoryPath: o.RepositoryPath, HomeDirectory: o.HomeDirectory, TerminalContext: terminalContext, ConfirmGitHubRepository: os.Getenv("HOLARK_CONFIRM_GITHUB_REPOSITORY") == "1"})
		if openErr != nil {
			return openErr
		}
		defer application.Close()
		o.API = application.Handler
		o.RepositoryID = application.RepositoryID
	}
	if err = os.MkdirAll(o.HomeDirectory, 0o700); err != nil {
		return err
	}
	if err = os.MkdirAll(o.RuntimeDirectory, 0o700); err != nil {
		return err
	}
	if err = os.Chmod(o.RuntimeDirectory, 0o700); err != nil {
		return err
	}
	listener, err := net.Listen("tcp", o.ListenAddress)
	if err != nil {
		return err
	}
	defer listener.Close()
	address := listener.Addr().String()
	baseURL := "http://" + address
	cliToken, err := token()
	if err != nil {
		return err
	}
	handler, launch, err := newHandler(address, cliToken, o.API)
	if err != nil {
		return err
	}
	cleanup, err := localconnection.Publish(o.RuntimeDirectory, o.RepositoryID, Connection{URL: baseURL, Token: cliToken})
	if err != nil {
		return err
	}
	defer cleanup()
	fmt.Fprintf(o.Stdout, "%s/?launch=%s\n", baseURL, url.QueryEscape(launch))
	server := &http.Server{Handler: handler}
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	select {
	case <-ctx.Done():
		_ = server.Shutdown(context.Background())
		<-done
		return ctx.Err()
	case err := <-done:
		return err
	}
}
