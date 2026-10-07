---
name: holark-integration
description: Holark CLI integration for agents. Use when the user asks an agent to interact with Holark through the local `holark` command, especially to list, create, inspect, close, reopen, or label issues from a managed agent session.
---

# Holark Integration

## Principle

Use the `holark` CLI as the integration boundary. It discovers the running local application through Holark's private repository-scoped connection file. Do not infer application state from repository files, Git config, worktree paths, or Holark internals unless the user explicitly asks for implementation work on Holark itself.

Prefer JSON output when the result will be parsed, transformed, or used for follow-up commands.

## Configuration

The CLI selects the connection for `HOLARK_REPO_PATH`, falling back to the
current Git worktree. The private connection file supplies the server URL and
bearer token together; managed agent environments do not contain the token.
If discovery fails, ask the user to start or restart Holark for this repository.

`--server URL` or `HOLARK_SERVER_URL` may explicitly override discovery with
a loopback HTTP origin. A matching repository connection supplies its token;
any other override also requires `HOLARK_CLI_AUTH_TOKEN`. Do not use Git
configuration or port 8080 as a fallback.

Command-local flags may appear before or after positional arguments.

## Issues

List issues:

```bash
holark issue list
holark issue list --status open
holark issue list --json --status open
```

Create an issue:

```bash
holark issue create --title "Title" --body "Body"
holark issue create --title "Title" --body-file issue.md
holark issue create --title "Title" --body-file -
```

Inspect an issue:

```bash
holark issue get ISSUE_ID
holark issue get ISSUE_ID --json
```

Close or reopen an issue:

```bash
holark issue close ISSUE_ID
holark issue reopen ISSUE_ID
```

## Labels

List and create repository labels:

```bash
holark label list
holark label list --json
holark label create --name "bug" --color d73a4a --description "Something is broken"
holark label create --name "needs review"
```

Assign or remove one or more labels by name:

```bash
holark issue label add ISSUE_ID "bug" "priority: high"
holark issue label remove ISSUE_ID "needs review"
holark issue label add ISSUE_ID "bug" --json
```

List labels before assigning them. Assignment accepts only names currently
present on GitHub; create a missing definition explicitly with `label create`.
Add and remove are idempotent.

## Output Handling

For user-facing summaries, report the issue ID, status, title, assigned label names, and GitHub URL if present. When a command fails, preserve the CLI error code and message instead of guessing the server state.
