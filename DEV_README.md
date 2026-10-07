# Holark

Holark is a local workspace for working with coding agents on a Git repository.
Run Codex, Claude Code, or OpenCode in isolated Git worktrees, inspect their
changes, and manage GitHub issues and pull requests from one browser UI.

A **Holon** is a workspace for a task: it brings together a managed Git worktree,
agent sessions, shell terminals, and IDE tabs. Create multiple Holons to work on
different tasks in parallel.

## What you can do

- Start a Holon from a branch, an issue, or a pull request.
- Work with agents and shell terminals, inspect diffs, and open an embedded
  VS Code IDE.
- Browse repository files, branches, and commit history.
- Create and discuss GitHub issues, prepare and publish pull requests, review
  changes, address feedback, and rebase branches.
- Find assigned issues, assigned pull requests, and review requests in **My work**.

Holark runs a local server for one user and one repository. Its browser UI and
API listen only on loopback.

[Getting started](#getting-started) · [Local state](#local-state-and-configuration) ·
[CLI](#cli) · [Issue discussion](#issue-discussion) · [Development](#development)

## Getting started

All installation methods support Linux and macOS on x64 and ARM64, including
Apple Silicon. For the agent and GitHub workflow, you need:

- Git and a repository with at least one commit.
- An installed, authenticated Codex, Claude Code, or OpenCode CLI.
- GitHub CLI (`gh`), authenticated with `gh auth login`, and a GitHub `origin` remote.
- A browser.

The embedded IDE uses VS Code Server. Holark provisions it on demand using a
compatible `code` CLI or a managed download; no manual server installation is
needed. Initial provisioning requires internet access.

### npm

**Requirements:** Node.js 20.19+ with npm (Node.js 24 LTS recommended).

```sh
npm install -g holark
holark /path/to/your/repository
```

npm installs the `holark` command in its global bin directory. If your shell
reports `holark: command not found`, add that directory to PATH:

```sh
export PATH="$(npm prefix -g)/bin:$PATH"
```

### GitHub binaries

**Requirements:** Node.js 20.19+. Go, Make, and npm are not required.

Coming soon on [GitHub Releases](https://github.com/holark-ai/holark/releases).
Download the archive and extract it in a directory of your choice.
For `holark-0.1.0-1.tgz`:

```sh
tar -xzf holark-0.1.0-1.tgz
cd package
platform=$(node -p 'process.platform + "-" + process.arch')
export HOLARK_XTERM_WORKER_DIR="$PWD/terminal-worker"
export PATH="$PWD/native/$platform:$PATH"
holark /path/to/your/repository
```

### Build from scratch

**Requirements:** Go 1.24+, Make, and Node.js with npm (Node.js 24 LTS recommended).

```sh
git clone https://github.com/holark-ai/holark.git
cd holark
make build
export PATH="$PWD/bin:$PATH"
holark /path/to/your/repository
```

To persist PATH changes, save the exports in `~/.zshrc` or `~/.bashrc` after any
Node.js version-manager setup. Use absolute installation paths instead of
`$PWD`; for GitHub binaries, also save `HOLARK_XTERM_WORKER_DIR`.

You can then run `holark` inside your repository. Open the printed URL and keep
Holark running while using the UI.

## Local state and configuration

Holark binds to loopback and prints an authenticated launch URL. The link printed
in your terminal can be reused in new tabs or browsers until Holark stops or
restarts. Each restart generates a fresh link and invalidates previous links
and browser sessions.

Persistent state, managed worktrees, caches, agent runtimes, and IDE data are
stored beneath `HOLARK_HOME`, which defaults to `~/.holark`.

The default home layout is:

```text
~/.holark/
├── state/repositories/       # Per-repository SQLite state
├── worktrees/                # Managed Git worktrees
├── cache/repositories/       # Reproducible bare Git caches
├── runtimes/agents/          # Private harness integration files
└── ide/                      # Managed VS Code executable, profile, and logs
```

| Variable | Purpose | Default |
| --- | --- | --- |
| `HOLARK_HOME` | Persistent local state and managed worktrees | `~/.holark` |
| `HOLARK_LISTEN_ADDR` | Server address; requires an explicit loopback IP | `127.0.0.1:0` (an available port) |
| `HOLARK_RUNTIME_DIR` | Private connection files used by the server and CLI | `holark-<uid>` beneath the system temporary directory |
| `HOLARK_XTERM_WORKER_DIR` | Location of `terminal-worker/` and its dependencies | Discovered from the executable, working directory, or source tree |

The HTTP and WebSocket surfaces reject non-loopback listeners and require the
per-launch secret. Repository paths are supplied locally and are never exposed
through a hosted relay.

## Keyboard navigation

- Option/Alt + Shift + Left/Right switches tabs within the current Holon.
- Option/Alt + Shift + Up/Down switches current Holons in sidebar order.

Both wrap at either end and work while an agent or shell terminal has focus.
Switching Holons restores the selected tab and requests terminal focus. Dialogs
and ordinary text fields keep their own keyboard behavior. Shortcuts inside the
embedded VS Code frame are handled by VS Code.

## CLI

The `holark` CLI manages issues, labels, and issue comments through a running
Holark instance:

```sh
holark issue list
holark issue get ISSUE_ID
holark issue create --title 'Fix the login flow' --body-file issue.md
holark issue close ISSUE_ID
holark issue reopen ISSUE_ID
holark label list
holark issue label add ISSUE_ID bug
holark --help
```

Managed agent and shell terminals put the running server's `holark` executable
first on `PATH`. Outside those terminals, use the built executable
(`./bin/holark`) or add its directory to `PATH`.

The CLI discovers the local process from a private, repository-scoped connection
file. Run commands from the repository worktree or set `HOLARK_REPO_PATH`.
Managed terminals automatically use the server's runtime directory; set
`HOLARK_RUNTIME_DIR` if you use a custom directory outside those terminals.
Bearer tokens are read from the connection file and are not placed in child
environments.

`--server URL` or `HOLARK_SERVER_URL` can explicitly select another loopback
HTTP origin for development. If it does not match this repository's active
connection, `HOLARK_CLI_AUTH_TOKEN` is required.

## Issue discussion

Issue pages display a chronological GitHub discussion, with cached comments available
while refreshing. Create, edit, delete, and quote comments in the discussion panel.
Quote replies are ordinary Markdown comments. Markdown supports links, code, quotes,
and inline images, including GitHub's HTML image tags.

PR descriptions, issues, comments, and replies accept images through **Attach image**,
drag and drop, or clipboard paste. Uploads use the active `gh` login and require write
access to the connected GitHub repository. PNG, JPEG, GIF, WebP, and SVG images up to
10 MB are supported. Preview displays the images before you publish the text.

Images are uploaded immediately to GitHub, and their permanent GitHub URLs are saved
as ordinary Markdown references that synchronize with the description or comment.
Holark does not store image files or serve them locally. Private images are displayed
using temporary URLs obtained through GitHub authentication; the browser loads the
image directly from GitHub. Discarding a draft does not undo an upload to GitHub.

### Comment commands

```sh
holark issue comment list ISSUE_ID
holark issue comment list ISSUE_ID --refresh --json
holark issue comment sync ISSUE_ID
holark issue comment create ISSUE_ID --body 'Additional context'
holark issue comment create ISSUE_ID --body-file comment.md
printf 'Additional context\n' | holark issue comment create ISSUE_ID --body-file -
holark issue comment edit COMMENT_ID --body-file comment.md
holark issue comment delete COMMENT_ID
```

All comment commands accept `--json` and the repository-scoped connection and
loopback override configuration described above.
Bodies must be nonblank and contain at most 65,536 Unicode characters. GitHub
permissions apply, including on closed issues. Posting permission remains unknown
when GitHub’s read API cannot determine it; actual writes are authorized by GitHub.
Reads use the local cache; use
`--refresh` or `sync` to fetch changes made on GitHub.

### Recovering a comment write

If a mutation reports `issue_comment_projection_pending`, GitHub accepted it but
Holark still needs to refresh its local copy. `issue_comment_outcome_uncertain`
means the change may have reached GitHub. Both exit unsuccessfully in the CLI.
Refresh the discussion and check GitHub before retrying; creation is never retried
automatically. The web UI retains the draft and provides recovery controls.

### Discussion in agent prompts

Starting an issue agent attempts to sync comments before reading its local snapshot.
If sync fails, the prompt uses cached discussion with a freshness warning. The
prompt includes up to 50 recent comments within a 20,000-character discussion
budget, with omissions and truncation marked. Custom issue templates can use
`{{issue_comments}}`; discussion is appended when that variable is absent, without
changing the saved template. Posting a comment does not start or message an agent.
Running agents can read later comments through the CLI.

## Website

The public-facing website source lives in [`website/`](website/README.md).
Preview it with `make website-dev`, then open `http://127.0.0.1:8000`.
It is a static site with no build step, separate from the application UI in `web/`.

## Development

Use the Makefile to build and test so the Go packages have their generated
frontend assets:

```sh
make build
make test
make test-go PKG=./internal/localshell
make test-integration
make lint
make install-test-browser
make test-browser
```

`make test` runs the default Go and frontend suites. Integration and browser
tests run separately. `make install-test-browser` installs Chromium for the
browser suite.

### Build and publish the npm package

Maintainers need Go 1.24+, Make, and Node.js with npm (Node.js 24 LTS recommended).
Choose the version yourself; the script never updates the source package version.

Build and check a package locally, using an example version:

```sh
node packaging/npm/release.mjs 0.1.1
```

This builds all four platforms, writes the archive and SHA-256 checksum under
`.artifacts/npm/`, checks a temporary npm installation and terminal-worker startup,
and runs `npm publish --dry-run`. It prints the command to publish that exact
archive. Your global installation is unchanged.

To build, check, and publish in one command, sign in to npm with an account
allowed to publish `holark`, then use an unused version:

```sh
npm login --registry=https://registry.npmjs.org/
node packaging/npm/release.mjs 0.1.1 --publish
```

`--publish` checks your login and rejects already-published versions before
building. npm's account and two-factor authentication requirements still apply.
The default tag is `latest`; use `--tag next` for a preview release. The tag is
explicitly independent of whether the version has a prerelease suffix.

Run `make test` before releasing. The script checks packaging and startup;
it does not run the full application test suite. To only build an archive,
the existing `make package-npm NPM_VERSION=0.1.1` command remains available.

### Try with disposable state

To run with isolated, disposable local state while trying changes from a
development worktree, use ephemeral mode:

```sh
./bin/holark --ephemeral /path/to/repository
```

Use `-e` as shorthand for `--ephemeral`.

Multiple ephemeral instances can run against the same repository, each with its
own disposable state and managed worktrees. Ephemeral state is removed on a normal
exit. The next ephemeral launch for the same repository cleans up state left by
crashed launches, leaving running instances untouched.
External actions such as pushes and GitHub changes are not reverted.

### Tests with real agents

Run `make test-claude` for commit-fork coverage with the installed,
authenticated Claude Code CLI. It builds Holark for real hook ingestion and uses
temporary configuration with an unexpired access token from `.credentials.json`
or the default macOS Keychain entry. Run Claude Code normally first if the login
needs refreshing. `ANTHROPIC_API_KEY` and `CLAUDE_CODE_OAUTH_TOKEN` are also
supported; the test never copies refresh tokens.

Run `make test-opencode` for the OpenCode native-TUI smoke test and
commit-fork coverage. The fork test uses temporary XDG directories, copying
file-backed authentication and recent model selection when available. Both
commands make real model requests, exercise native permission prompts, and are
excluded from the default test suite.

### Resetting a database

Database schemas are defined in each store's table creation code. There are no
schema migrations. When a schema change makes an existing database incompatible,
stop Holark and run `holark clear-db` from that repository (or
`holark clear-db /path/to/repository`). The confirmation defaults to yes; pass
`--yes` to skip the prompt. It removes that repository's SQLite database and its
`-wal` and `-shm` sidecars from `HOLARK_HOME/state/repositories/`. Holark recreates
the database on the next launch; its previous local state is discarded.

### UI previews

Start the frontend dev server from the repository root:

```sh
npm --prefix web ci
npm --prefix web run dev
```

Open the [repository preview](http://127.0.0.1:5173/__dev/tree/web) or the
[holon preview](http://127.0.0.1:5173/__dev/holon) to iterate on the Shared tree
design using the real application components and sample Acme Dashboard data.
The [repository root](http://127.0.0.1:5173/__dev/) also supports file and branch
navigation. These previews work without the Go backend; terminal output is
illustrative and does not start an agent process. On macOS, open it
from another terminal with:

```sh
open http://127.0.0.1:5173/__dev/holon
```

The [PR list preview](http://127.0.0.1:5173/__dev/pulls) uses the real list with
sample pull requests, member filters, live fuzzy title search, sorting, and sync. Click the PR
icon in the left rail from any preview to open it, then click a row to open its
PR detail. Sample data resets on reload.

The [My work preview](http://127.0.0.1:5173/__dev/my-work) uses the real page with
sample assigned issues, assigned PRs, and review requests. Open it from the
My work rail item to try the filters, counts, pagination, detail links, and
simulated sync without the Go backend.

The [commit history preview](http://127.0.0.1:5173/__dev/?view=changes) loads a long history in pages of 50. Open **Commit history preview** at the bottom left to change the page delay, fail the next older-page request, or simulate a new commit on main. Scroll to check loading, retry, day grouping, and the end-of-history marker; select any commit to inspect its diff. Reload to reset the scenario.
