package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/holark-ai/holark/internal/harness/claudecode"
	"github.com/holark-ai/holark/internal/holarkcli"
	"github.com/holark-ai/holark/internal/localshell"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	args := os.Args[1:]
	repository, ephemeral, launch, parseErr := launchRequest(args)
	if launch {
		if parseErr != nil {
			fmt.Fprintf(os.Stderr, "holark: %v\n", parseErr)
			fmt.Fprintln(os.Stderr, "usage: holark [--ephemeral|-e] [repository]")
			os.Exit(2)
		}
		err := localshell.Run(ctx, localshell.Options{RepositoryPath: repository, ListenAddress: os.Getenv("HOLARK_LISTEN_ADDR"), HomeDirectory: os.Getenv("HOLARK_HOME"), RuntimeDirectory: os.Getenv("HOLARK_RUNTIME_DIR"), Ephemeral: ephemeral, Stdout: os.Stdout})
		if err != nil && !errors.Is(err, context.Canceled) {
			fmt.Fprintf(os.Stderr, "holark: %v\n", err)
			os.Exit(1)
		}
		return
	}
	os.Exit(runCLI(ctx, holarkcli.Options{
		Args:   args,
		Stdin:  os.Stdin,
		Stdout: os.Stdout,
		Stderr: os.Stderr,
		Env:    os.Getenv,
	}))
}

func launchRequest(args []string) (repository string, ephemeral, launch bool, err error) {
	switch {
	case len(args) == 0:
		return ".", false, true, nil
	case nonLaunchCommand(args[0]):
		return "", false, false, nil
	case args[0] == "--ephemeral" || args[0] == "-e":
		if len(args) > 2 {
			return "", false, true, errors.New("ephemeral mode accepts at most one repository")
		}
		if len(args) == 2 {
			return args[1], true, true, nil
		}
		return ".", true, true, nil
	case len(args) == 1 && (args[0] == "" || args[0][0] != '-'):
		return args[0], false, true, nil
	case len(args) == 2 && (args[1] == "--ephemeral" || args[1] == "-e"):
		return args[0], true, true, nil
	default:
		return "", false, false, nil
	}
}

func nonLaunchCommand(arg string) bool {
	switch arg {
	case "issue", "label", "clear-db", "help", "claude-hook", "-h", "--help":
		return true
	default:
		return false
	}
}

func runCLI(ctx context.Context, options holarkcli.Options) int {
	if claimed, exitCode := dispatchInternalCommand(options.Args, options.Stdin, options.Stderr); claimed {
		return exitCode
	}
	return holarkcli.Run(ctx, options)
}

func dispatchInternalCommand(args []string, stdin io.Reader, stderr io.Writer) (bool, int) {
	if len(args) == 0 || args[0] != "claude-hook" {
		return false, 0
	}
	if err := runClaudeHook(args[1:], stdin); err != nil {
		fmt.Fprintln(stderr, "holark: Claude hook ingestion failed")
		return true, 1
	}
	return true, 0
}

func runClaudeHook(args []string, stdin io.Reader) error {
	flags := flag.NewFlagSet("claude-hook", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	runtimeDir := flags.String("runtime-dir", "", "")
	launchToken := flags.String("launch-token", "", "")
	harnessSessionID := flags.String("harness-session-id", "", "")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("unexpected Claude hook arguments")
	}
	return claudecode.IngestHook(stdin, claudecode.IngestSpec{
		RuntimeDir:       *runtimeDir,
		LaunchToken:      *launchToken,
		HarnessSessionID: *harnessSessionID,
	})
}
