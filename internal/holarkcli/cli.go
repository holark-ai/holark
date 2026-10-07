package holarkcli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/holark-ai/holark/internal/holarkclient"
	"github.com/holark-ai/holark/internal/localapp"
	"github.com/holark-ai/holark/internal/localconnection"
	"github.com/holark-ai/holark/internal/localshell"
	"github.com/holark-ai/holark/internal/repository/gitadapter"
)

type EnvFunc func(string) string

type Options struct {
	Args   []string
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
	Env    EnvFunc
}

func Run(ctx context.Context, options Options) int {
	runner := commandRunner{
		args:   options.Args,
		stdin:  options.Stdin,
		stdout: options.Stdout,
		stderr: options.Stderr,
		env:    options.Env,
	}
	return runner.run(ctx)
}

type commandRunner struct {
	args       []string
	stdin      io.Reader
	stdout     io.Writer
	stderr     io.Writer
	env        EnvFunc
	serverURL  string
	json       bool
	endpoint   localConnection
	resolved   bool
	resolveErr error
}

type localConnection = localconnection.Connection

func (runner *commandRunner) run(ctx context.Context) int {
	runner.setDefaults()
	flags := flag.NewFlagSet("holark", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.StringVar(&runner.serverURL, "server", "", "loopback Holark server URL override")
	flags.BoolVar(&runner.json, "json", false, "print JSON output")
	if err := flags.Parse(runner.args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			runner.printUsage()
			return 0
		}
		runner.usageError(err.Error())
		return 2
	}
	args := flags.Args()
	if len(args) == 0 {
		runner.printUsage()
		return 2
	}
	switch args[0] {
	case "clear-db":
		return runner.runClearDB(ctx, args[1:])
	case "issue":
		return runner.runIssue(ctx, args[1:])
	case "label":
		return runner.runLabel(ctx, args[1:])
	case "-h", "--help", "help":
		runner.printUsage()
		return 0
	default:
		runner.usageError(fmt.Sprintf("unknown command %q", args[0]))
		return 2
	}
}

func (runner *commandRunner) runClearDB(ctx context.Context, args []string) int {
	var yes bool
	flags := flag.NewFlagSet("holark clear-db", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.BoolVar(&yes, "yes", false, "skip confirmation")
	flags.BoolVar(&yes, "y", false, "skip confirmation")
	if err := flags.Parse(interspersedFlagArgs(flags, args)); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fmt.Fprintln(runner.stderr, "Usage: holark clear-db [REPOSITORY] [--yes]")
			return 0
		}
		runner.usageError(err.Error())
		return 2
	}
	if flags.NArg() > 1 {
		runner.usageError("usage: holark clear-db [REPOSITORY] [--yes]")
		return 2
	}
	repositoryPath := "."
	if flags.NArg() == 1 {
		repositoryPath = flags.Arg(0)
	}
	homeDirectory := runner.env("HOLARK_HOME")
	if homeDirectory == "" {
		var err error
		homeDirectory, err = localshell.DefaultHomeDirectory()
		if err != nil {
			runner.printError(err)
			return 1
		}
	}
	prompted := false
	cleared, err := localapp.ClearRepositoryDatabase(ctx, repositoryPath, homeDirectory, func(path string) (bool, error) {
		prompted = true
		if yes {
			fmt.Fprintf(runner.stdout, "Clearing %s and its SQLite sidecars\n", path)
			return true, nil
		}
		fmt.Fprintf(runner.stdout, "Clear the database and SQLite sidecars for %s? [Y/n]\n%s\n> ", repositoryPath, path)
		answer, readErr := bufio.NewReader(runner.stdin).ReadString('\n')
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			return false, readErr
		}
		if errors.Is(readErr, io.EOF) && answer == "" {
			return false, nil
		}
		switch strings.ToLower(strings.TrimSpace(answer)) {
		case "", "y", "yes":
			return true, nil
		default:
			return false, nil
		}
	})
	if err != nil {
		if errors.Is(err, localapp.ErrRepositoryAlreadyRunning) {
			runner.printError(errors.New("Holark is running for this repository; stop it before clearing the database"))
		} else {
			runner.printError(err)
		}
		return 1
	}
	if cleared {
		fmt.Fprintln(runner.stdout, "Repository database cleared.")
	} else if !prompted {
		fmt.Fprintln(runner.stdout, "No repository database found.")
	} else {
		fmt.Fprintln(runner.stdout, "No database cleared.")
	}
	return 0
}

func (runner *commandRunner) runIssue(ctx context.Context, args []string) int {
	if len(args) == 0 {
		runner.printIssueUsage()
		return 2
	}
	switch args[0] {
	case "comment":
		return runner.runIssueComment(ctx, args[1:])
	case "create":
		return runner.runIssueCreate(ctx, args[1:])
	case "list":
		return runner.runIssueList(ctx, args[1:])
	case "get":
		return runner.runIssueGet(ctx, args[1:])
	case "close":
		return runner.runIssueState(ctx, args[1:], "close")
	case "reopen":
		return runner.runIssueState(ctx, args[1:], "reopen")
	case "label":
		return runner.runIssueLabel(ctx, args[1:])
	case "-h", "--help", "help":
		runner.printIssueUsage()
		return 0
	default:
		runner.usageError(fmt.Sprintf("unknown issue command %q", args[0]))
		return 2
	}
}

func (runner *commandRunner) runLabel(ctx context.Context, args []string) int {
	if len(args) == 0 {
		runner.printLabelUsage()
		return 2
	}
	switch args[0] {
	case "list":
		return runner.runLabelList(ctx, args[1:])
	case "create":
		return runner.runLabelCreate(ctx, args[1:])
	case "-h", "--help", "help":
		runner.printLabelUsage()
		return 0
	default:
		runner.usageError(fmt.Sprintf("unknown label command %q", args[0]))
		return 2
	}
}

func (runner *commandRunner) runLabelList(ctx context.Context, args []string) int {
	jsonOutput := runner.json
	flags := flag.NewFlagSet("holark label list", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	runner.addClientOptions(flags, &jsonOutput)
	if err := flags.Parse(interspersedFlagArgs(flags, args)); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			runner.printLabelUsage()
			return 0
		}
		runner.usageError(err.Error())
		return 2
	}
	if flags.NArg() != 0 {
		runner.usageError("label list does not accept positional arguments")
		return 2
	}
	client, ok := runner.requestClient(ctx)
	if !ok {
		return 1
	}
	labels, err := client.ListLabels(ctx)
	if err != nil {
		runner.printError(err)
		return 1
	}
	if jsonOutput {
		return runner.printJSON(labels)
	}
	for _, label := range labels {
		fmt.Fprintf(runner.stdout, "%s\t#%s\t%s\n", label.Name, label.Color, label.Description)
	}
	return 0
}

func (runner *commandRunner) runLabelCreate(ctx context.Context, args []string) int {
	var name, color, description string
	jsonOutput := runner.json
	flags := flag.NewFlagSet("holark label create", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.StringVar(&name, "name", "", "label name")
	flags.StringVar(&color, "color", "", "six-digit hexadecimal color")
	flags.StringVar(&description, "description", "", "label description")
	runner.addClientOptions(flags, &jsonOutput)
	if err := flags.Parse(interspersedFlagArgs(flags, args)); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			runner.printLabelUsage()
			return 0
		}
		runner.usageError(err.Error())
		return 2
	}
	if flags.NArg() != 0 || strings.TrimSpace(name) == "" {
		runner.usageError("usage: holark label create --name NAME [--color HEX] [--description TEXT]")
		return 2
	}
	client, ok := runner.requestClient(ctx)
	if !ok {
		return 1
	}
	label, err := client.CreateLabel(ctx, name, color, description)
	if err != nil {
		runner.printError(err)
		return 1
	}
	if jsonOutput {
		return runner.printJSON(label)
	}
	fmt.Fprintf(runner.stdout, "Created label %s (#%s)\n", label.Name, label.Color)
	return 0
}

func (runner *commandRunner) runIssueLabel(ctx context.Context, args []string) int {
	if len(args) == 0 || (args[0] != "add" && args[0] != "remove") {
		runner.usageError("usage: holark issue label add|remove ISSUE_ID LABEL_NAME...")
		return 2
	}
	action := args[0]
	jsonOutput := runner.json
	flags := flag.NewFlagSet("holark issue label "+action, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	runner.addClientOptions(flags, &jsonOutput)
	if err := flags.Parse(interspersedFlagArgs(flags, args[1:])); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			runner.printIssueUsage()
			return 0
		}
		runner.usageError(err.Error())
		return 2
	}
	if flags.NArg() < 2 {
		runner.usageError("usage: holark issue label " + action + " ISSUE_ID LABEL_NAME...")
		return 2
	}
	issueID := flags.Arg(0)
	labels := flags.Args()[1:]
	var (
		issue holarkclient.Issue
		err   error
	)
	client, ok := runner.requestClient(ctx)
	if !ok {
		return 1
	}
	if action == "add" {
		issue, err = client.AddIssueLabels(ctx, issueID, labels)
	} else {
		issue, err = client.RemoveIssueLabels(ctx, issueID, labels)
	}
	if err != nil {
		runner.printError(err)
		return 1
	}
	if jsonOutput {
		return runner.printJSON(issue)
	}
	names := labelNames(issue.Labels)
	if len(names) == 0 {
		names = []string{"(none)"}
	}
	fmt.Fprintf(runner.stdout, "Issue %s labels: %s\n", issue.ID, strings.Join(names, ", "))
	return 0
}

func (runner *commandRunner) runIssueCreate(ctx context.Context, args []string) int {
	var title, body, bodyFile string
	jsonOutput := runner.json
	flags := flag.NewFlagSet("holark issue create", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.StringVar(&title, "title", "", "issue title")
	flags.StringVar(&body, "body", "", "issue body")
	flags.StringVar(&bodyFile, "body-file", "", "issue body file, or - for stdin")
	runner.addClientOptions(flags, &jsonOutput)
	if err := flags.Parse(interspersedFlagArgs(flags, args)); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			runner.printUsage()
			return 0
		}
		runner.usageError(err.Error())
		return 2
	}
	if flags.NArg() != 0 {
		runner.usageError("issue create does not accept positional arguments")
		return 2
	}
	if strings.TrimSpace(title) == "" {
		runner.usageError("--title is required")
		return 2
	}
	resolvedBody, err := runner.resolveBody(body, bodyFile)
	if err != nil {
		runner.printError(err)
		return 1
	}
	client, ok := runner.requestClient(ctx)
	if !ok {
		return 1
	}
	issue, err := client.CreateIssue(ctx, title, resolvedBody)
	if err != nil {
		runner.printError(err)
		return 1
	}
	if jsonOutput {
		return runner.printJSON(issue)
	}
	fmt.Fprintf(runner.stdout, "Created issue %s: %s\nStatus: %s\n", issue.ID, issue.Title, issue.Status)
	return 0
}

func (runner *commandRunner) runIssueList(ctx context.Context, args []string) int {
	var status string
	jsonOutput := runner.json
	flags := flag.NewFlagSet("holark issue list", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.StringVar(&status, "status", "", "filter by status: open or closed")
	runner.addClientOptions(flags, &jsonOutput)
	if err := flags.Parse(interspersedFlagArgs(flags, args)); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			runner.printUsage()
			return 0
		}
		runner.usageError(err.Error())
		return 2
	}
	if flags.NArg() != 0 {
		runner.usageError("issue list does not accept positional arguments")
		return 2
	}
	if status != "" && status != string(holarkclient.IssueStatusOpen) && status != string(holarkclient.IssueStatusClosed) {
		runner.usageError("--status must be open or closed")
		return 2
	}
	client, ok := runner.requestClient(ctx)
	if !ok {
		return 1
	}
	issues, err := client.ListIssues(ctx)
	if err != nil {
		runner.printError(err)
		return 1
	}
	if status != "" {
		filtered := issues[:0]
		for _, issue := range issues {
			if string(issue.Status) == status {
				filtered = append(filtered, issue)
			}
		}
		issues = filtered
	}
	sort.SliceStable(issues, func(i, j int) bool {
		leftNumber := issueGitHubNumber(issues[i])
		rightNumber := issueGitHubNumber(issues[j])
		if leftNumber != 0 && rightNumber != 0 && leftNumber != rightNumber {
			return leftNumber > rightNumber
		}
		if leftNumber != 0 && rightNumber == 0 {
			return true
		}
		if leftNumber == 0 && rightNumber != 0 {
			return false
		}
		return issues[i].ID > issues[j].ID
	})
	if jsonOutput {
		return runner.printJSON(issues)
	}
	if len(issues) == 0 {
		fmt.Fprintln(runner.stdout, "No issues.")
		return 0
	}
	for _, issue := range issues {
		if issue.SyncData.GitHub != nil && issue.SyncData.GitHub.Number != 0 {
			fmt.Fprintf(runner.stdout, "%s\t%s\t#%d\t%s\n", issue.ID, issue.Status, issue.SyncData.GitHub.Number, issue.Title)
		} else {
			fmt.Fprintf(runner.stdout, "%s\t%s\t%s\n", issue.ID, issue.Status, issue.Title)
		}
	}
	return 0
}

func (runner *commandRunner) runIssueGet(ctx context.Context, args []string) int {
	jsonOutput := runner.json
	flags := flag.NewFlagSet("holark issue get", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	runner.addClientOptions(flags, &jsonOutput)
	if err := flags.Parse(interspersedFlagArgs(flags, args)); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			runner.printUsage()
			return 0
		}
		runner.usageError(err.Error())
		return 2
	}
	if flags.NArg() != 1 {
		runner.usageError("usage: holark issue get ISSUE_ID")
		return 2
	}
	client, ok := runner.requestClient(ctx)
	if !ok {
		return 1
	}
	issue, err := client.GetIssue(ctx, flags.Arg(0))
	if err != nil {
		runner.printError(err)
		return 1
	}
	if jsonOutput {
		return runner.printJSON(issue)
	}
	runner.printIssue(issue)
	return 0
}

func (runner *commandRunner) runIssueState(ctx context.Context, args []string, action string) int {
	jsonOutput := runner.json
	flags := flag.NewFlagSet("holark issue "+action, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	runner.addClientOptions(flags, &jsonOutput)
	if err := flags.Parse(interspersedFlagArgs(flags, args)); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			runner.printUsage()
			return 0
		}
		runner.usageError(err.Error())
		return 2
	}
	if flags.NArg() != 1 {
		runner.usageError("usage: holark issue " + action + " ISSUE_ID")
		return 2
	}
	var (
		issue holarkclient.Issue
		err   error
	)
	client, ok := runner.requestClient(ctx)
	if !ok {
		return 1
	}
	if action == "close" {
		issue, err = client.CloseIssue(ctx, flags.Arg(0))
	} else {
		issue, err = client.ReopenIssue(ctx, flags.Arg(0))
	}
	if err != nil {
		runner.printError(err)
		return 1
	}
	if jsonOutput {
		return runner.printJSON(issue)
	}
	fmt.Fprintf(runner.stdout, "Issue %s is %s: %s\n", issue.ID, issue.Status, issue.Title)
	return 0
}

func (runner *commandRunner) resolveBody(body, bodyFile string) (string, error) {
	if body != "" && bodyFile != "" {
		return "", errors.New("--body and --body-file cannot both be set")
	}
	if bodyFile == "" {
		return body, nil
	}
	var data []byte
	var err error
	if bodyFile == "-" {
		data, err = io.ReadAll(runner.stdin)
	} else {
		data, err = os.ReadFile(bodyFile)
	}
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func interspersedFlagArgs(flags *flag.FlagSet, args []string) []string {
	var flagArgs []string
	var positionalArgs []string
	for index := 0; index < len(args); index++ {
		arg := args[index]
		if arg == "--" {
			positionalArgs = append(positionalArgs, args[index:]...)
			break
		}
		if !strings.HasPrefix(arg, "-") || arg == "-" {
			positionalArgs = append(positionalArgs, arg)
			continue
		}
		name, hasValue := flagName(arg)
		found := flags.Lookup(name)
		if found == nil && name != "h" && name != "help" {
			positionalArgs = append(positionalArgs, arg)
			continue
		}
		flagArgs = append(flagArgs, arg)
		if found != nil && !hasValue && !isBoolFlag(found) && index+1 < len(args) {
			index++
			flagArgs = append(flagArgs, args[index])
		}
	}
	return append(flagArgs, positionalArgs...)
}

func (runner *commandRunner) addClientOptions(flags *flag.FlagSet, jsonOutput *bool) {
	flags.StringVar(&runner.serverURL, "server", runner.serverURL, "Holark server URL")
	flags.BoolVar(jsonOutput, "json", runner.json, "print JSON output")
}

func flagName(arg string) (string, bool) {
	name := strings.TrimLeft(arg, "-")
	if before, _, ok := strings.Cut(name, "="); ok {
		return before, true
	}
	return name, false
}

func isBoolFlag(flagValue *flag.Flag) bool {
	boolFlag, ok := flagValue.Value.(interface{ IsBoolFlag() bool })
	return ok && boolFlag.IsBoolFlag()
}

func (runner *commandRunner) requestClient(ctx context.Context) (holarkclient.Client, bool) {
	client, err := runner.client(ctx)
	if err != nil {
		runner.printError(err)
		return holarkclient.Client{}, false
	}
	return client, true
}

func (runner *commandRunner) client(ctx context.Context) (holarkclient.Client, error) {
	if !runner.resolved {
		runner.endpoint, runner.resolveErr = runner.resolveEndpoint(ctx)
		runner.resolved = true
	}
	if runner.resolveErr != nil {
		return holarkclient.Client{}, runner.resolveErr
	}
	return holarkclient.Client{BaseURL: runner.endpoint.URL, BearerToken: runner.endpoint.Token}, nil
}

func (runner *commandRunner) printIssue(issue holarkclient.Issue) {
	fmt.Fprintf(runner.stdout, "ID: %s\n", issue.ID)
	fmt.Fprintf(runner.stdout, "Status: %s\n", issue.Status)
	fmt.Fprintf(runner.stdout, "Title: %s\n", issue.Title)
	labels := labelNames(issue.Labels)
	if len(labels) == 0 {
		fmt.Fprintln(runner.stdout, "Labels: (none)")
	} else {
		fmt.Fprintf(runner.stdout, "Labels: %s\n", strings.Join(labels, ", "))
	}
	if issue.SyncData.GitHub != nil && issue.SyncData.GitHub.URL != "" {
		fmt.Fprintf(runner.stdout, "GitHub: %s\n", issue.SyncData.GitHub.URL)
	}
	if issue.Body != "" {
		fmt.Fprintf(runner.stdout, "\n%s\n", issue.Body)
	}
}

func labelNames(labels []holarkclient.Label) []string {
	names := make([]string, len(labels))
	for index := range labels {
		names[index] = labels[index].Name
	}
	return names
}

func (runner *commandRunner) printJSON(value any) int {
	encoder := json.NewEncoder(runner.stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(value); err != nil {
		runner.printError(err)
		return 1
	}
	return 0
}

func (runner *commandRunner) printError(err error) {
	fmt.Fprintf(runner.stderr, "holark: %v\n", err)
}

func (runner *commandRunner) usageError(message string) {
	fmt.Fprintf(runner.stderr, "holark: %s\n\n", message)
	runner.printUsage()
}

func (runner *commandRunner) printUsage() {
	fmt.Fprint(runner.stderr, `Usage:

Launch Holark:
  holark [--ephemeral|-e] [REPOSITORY]

  REPOSITORY defaults to the current directory.
  -e, --ephemeral  Use isolated, disposable local state

Manage local repository state:
  holark clear-db [REPOSITORY] [--yes]

Run commands against a running Holark instance:
  holark COMMAND [OPTIONS]

Commands:
  issue create --title TITLE [--body BODY|--body-file PATH]
  issue list [--status open|closed]
  issue get ISSUE_ID
  issue close ISSUE_ID
  issue reopen ISSUE_ID
  issue comment list|sync|create|edit|delete ID [--body TEXT|--body-file PATH]
  issue label add ISSUE_ID LABEL_NAME...
  issue label remove ISSUE_ID LABEL_NAME...
  label list
  label create --name NAME [--color HEX] [--description TEXT]

Configuration:
  By default, Holark discovers the repository-scoped private connection using
  HOLARK_REPO_PATH, then the current Git worktree.
  --server or HOLARK_SERVER_URL may override it with a loopback HTTP origin.
  A non-matching override requires HOLARK_CLI_AUTH_TOKEN.
`)
}

func (runner *commandRunner) printIssueUsage() {
	fmt.Fprint(runner.stderr, `Usage:
  holark issue create --title TITLE [--body BODY|--body-file PATH]
  holark issue list [--status open|closed]
  holark issue get ISSUE_ID
  holark issue close ISSUE_ID
  holark issue reopen ISSUE_ID
  holark issue comment list|sync|create|edit|delete ID [--body TEXT|--body-file PATH]
  holark issue label add ISSUE_ID LABEL_NAME...
  holark issue label remove ISSUE_ID LABEL_NAME...
`)
}

func (runner *commandRunner) printLabelUsage() {
	fmt.Fprint(runner.stderr, `Usage:
  holark label list
  holark label create --name NAME [--color HEX] [--description TEXT]
`)
}

func (runner *commandRunner) setDefaults() {
	if runner.stdin == nil {
		runner.stdin = strings.NewReader("")
	}
	if runner.stdout == nil {
		runner.stdout = io.Discard
	}
	if runner.stderr == nil {
		runner.stderr = io.Discard
	}
	if runner.env == nil {
		runner.env = os.Getenv
	}
}

func (runner *commandRunner) resolveEndpoint(ctx context.Context) (localConnection, error) {
	connection, connected := runner.localConnection(ctx)
	override := strings.TrimSpace(runner.serverURL)
	if override == "" {
		override = strings.TrimSpace(runner.env("HOLARK_SERVER_URL"))
	}
	if override == "" {
		if !connected {
			return localConnection{}, errors.New("no active Holark connection for this repository; start or restart Holark for this repository")
		}
		return connection, nil
	}
	override, err := localconnection.NormalizeURL(override)
	if err != nil {
		return localConnection{}, err
	}
	if connected && override == connection.URL {
		return connection, nil
	}
	token := strings.TrimSpace(runner.env("HOLARK_CLI_AUTH_TOKEN"))
	if token == "" {
		return localConnection{}, errors.New("HOLARK_CLI_AUTH_TOKEN is required when the server override does not match this repository's active connection")
	}
	return localConnection{URL: override, Token: token}, nil
}

func (runner *commandRunner) localConnection(ctx context.Context) (localConnection, bool) {
	directory := strings.TrimSpace(runner.env("HOLARK_RUNTIME_DIR"))
	if directory == "" {
		directory = filepath.Join(os.TempDir(), fmt.Sprintf("holark-%d", os.Getuid()))
	}
	repositoryPath := strings.TrimSpace(runner.env("HOLARK_REPO_PATH"))
	if repositoryPath != "" {
		if repositoryID, err := gitadapter.RepositoryID(ctx, repositoryPath); err == nil {
			return localconnection.Read(directory, repositoryID)
		}
	}
	repositoryPath, err := os.Getwd()
	if err != nil {
		return localConnection{}, false
	}
	repositoryID, err := gitadapter.RepositoryID(ctx, repositoryPath)
	if err != nil {
		return localConnection{}, false
	}
	return localconnection.Read(directory, repositoryID)
}

func issueGitHubNumber(issue holarkclient.Issue) int {
	if issue.SyncData.GitHub == nil {
		return 0
	}
	return issue.SyncData.GitHub.Number
}
