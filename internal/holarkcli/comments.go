package holarkcli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"time"

	"github.com/holark-ai/holark/internal/holarkclient"
	"github.com/holark-ai/holark/internal/issues/comments"
)

func (r *commandRunner) runIssueComment(ctx context.Context, args []string) int {
	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" {
		r.printCommentUsage()
		return 0
	}
	action := args[0]
	if action != "list" && action != "sync" && action != "create" && action != "edit" && action != "delete" {
		r.usageError("unknown issue comment command: " + action)
		return 2
	}
	var body, bodyFile string
	var refresh bool
	jsonOutput := r.json
	flags := flag.NewFlagSet("holark issue comment "+action, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	r.addClientOptions(flags, &jsonOutput)
	if action == "list" {
		flags.BoolVar(&refresh, "refresh", false, "refresh from GitHub")
	}
	if action == "create" || action == "edit" {
		flags.StringVar(&body, "body", "", "Markdown body")
		flags.StringVar(&bodyFile, "body-file", "", "body file or - for stdin")
	}
	if err := flags.Parse(interspersedFlagArgs(flags, args[1:])); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			r.printCommentUsage()
			return 0
		}
		r.usageError(err.Error())
		return 2
	}
	if flags.NArg() != 1 {
		r.usageError("issue comment " + action + " requires one ID")
		return 2
	}
	id := flags.Arg(0)
	switch action {
	case "list", "sync":
		client, ok := r.requestClient(ctx)
		if !ok {
			return 1
		}
		var d holarkclient.IssueDiscussion
		var err error
		if action == "sync" || refresh {
			d, err = client.SyncIssueComments(ctx, id)
		} else {
			d, err = client.ListIssueComments(ctx, id)
		}
		if err != nil {
			r.printError(err)
			return 1
		}
		if jsonOutput {
			return r.printJSON(d)
		}
		if d.SyncedAt == nil {
			fmt.Fprintln(r.stdout, "Discussion has not been synced.")
		} else {
			fmt.Fprintf(r.stdout, "Last synced: %s\n", d.SyncedAt.Format(time.RFC3339))
		}
		if len(d.Comments) == 0 {
			fmt.Fprintln(r.stdout, "No cached comments.")
		}
		for _, c := range d.Comments {
			r.printComment(c)
		}
		return 0
	case "create", "edit":
		provided := map[string]bool{}
		flags.Visit(func(f *flag.Flag) { provided[f.Name] = true })
		if provided["body"] == provided["body-file"] || (provided["body-file"] && bodyFile == "") {
			r.usageError("set exactly one of --body or --body-file")
			return 2
		}
		resolved, err := r.resolveBody(body, bodyFile)
		if err != nil {
			r.printError(err)
			return 1
		}
		if err = comments.ValidateBody(resolved); err != nil {
			r.usageError(err.Error())
			return 2
		}
		client, ok := r.requestClient(ctx)
		if !ok {
			return 1
		}
		var c holarkclient.IssueComment
		if action == "create" {
			c, err = client.CreateIssueComment(ctx, id, resolved)
		} else {
			c, err = client.UpdateIssueComment(ctx, id, resolved)
		}
		if err != nil {
			r.printError(err)
			return 1
		}
		if jsonOutput {
			return r.printJSON(c)
		}
		r.printComment(c)
		return 0
	case "delete":
		client, ok := r.requestClient(ctx)
		if !ok {
			return 1
		}
		if err := client.DeleteIssueComment(ctx, id); err != nil {
			r.printError(err)
			return 1
		}
		if jsonOutput {
			return r.printJSON(map[string]any{"id": id, "deleted": true})
		}
		fmt.Fprintf(r.stdout, "Deleted comment %s\n", id)
		return 0
	}
	return 2
}
func (r *commandRunner) printComment(c holarkclient.IssueComment) {
	author := c.Author.Login
	if author == "" {
		author = "Unknown author"
	}
	fmt.Fprintf(r.stdout, "\n%s · %s · %s\n%s\n%s\n", c.ID, author, c.CreatedAt.Format(time.RFC3339), c.URL, c.Body)
}
func (r *commandRunner) printCommentUsage() {
	fmt.Fprint(r.stderr, `Usage:
  holark issue comment list ISSUE_ID [--refresh] [--json]
  holark issue comment sync ISSUE_ID [--json]
  holark issue comment create ISSUE_ID --body TEXT | --body-file PATH
  holark issue comment edit COMMENT_ID --body TEXT | --body-file PATH
  holark issue comment delete COMMENT_ID [--json]

Use --body-file - to read Markdown from stdin. All commands accept --json.
If a mutation is pending or its outcome is uncertain, sync and check GitHub before retrying.
`)
}
