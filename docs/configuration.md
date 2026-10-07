# Configuration and local operation

## Ephemeral mode

Use ephemeral mode for isolated, disposable local state while trying changes
from a development worktree:

```sh
go run ./cmd/holark --ephemeral .
```

Use `-e` as shorthand for `--ephemeral`.

Multiple ephemeral instances can run against the same repository, each with
its own disposable state and managed worktrees. Ephemeral state is removed on
a normal exit. The next ephemeral launch for the same repository cleans up
state left by crashed launches while leaving running instances untouched.
External actions such as pushes and GitHub changes are not reverted.

## Local access and authentication

Holark binds to loopback and opens an authenticated launch URL. The link
printed in the terminal can be reused in new tabs or browsers until Holark
stops or restarts. Each restart creates a fresh link and invalidates previous
links and browser sessions.

The HTTP and WebSocket interfaces reject non-loopback listeners and require a
per-launch secret. Repository paths are supplied locally and are never exposed
through a hosted relay.

## State and environment variables

Persistent state, managed worktrees, caches, agent runtimes, and IDE data are
stored beneath `HOLARK_HOME`, which defaults to `~/.holark`.
`HOLARK_LISTEN_ADDR` can select another loopback address.

The default layout is:

```text
~/.holark/
├── state/repositories/       # Per-repository SQLite state
├── worktrees/                # Managed Git worktrees
├── cache/repositories/       # Reproducible bare Git caches
├── runtimes/agents/          # Private harness integration files
└── ide/                      # Managed VS Code executable, profile, and logs
```

The `holark` CLI discovers the local process from a private,
repository-scoped connection file. Managed agent and shell terminals use the
running server's runtime directory and put that server's `holark` executable
first on `PATH`. Bearer tokens are read from the connection file and are not
placed in child environments.

Outside a managed terminal, run commands from the repository worktree or set
`HOLARK_REPO_PATH`. `HOLARK_RUNTIME_DIR` selects a custom runtime directory for
both the server and CLI.

For development, `--server URL` or `HOLARK_SERVER_URL` can select another
loopback HTTP origin. If it does not match the repository's active connection,
`HOLARK_CLI_AUTH_TOKEN` is required.

## Keyboard navigation

- Option/Alt + Shift + Left/Right switches tabs within the current Holon.
- Option/Alt + Shift + Up/Down switches Holons in sidebar order.

Both shortcuts wrap at either end and work while an agent or shell terminal
has focus. Switching Holons restores the selected tab and requests terminal
focus. Dialogs and ordinary text fields keep their own keyboard behavior.
Shortcuts inside the embedded VS Code frame are handled by VS Code.

## Resetting incompatible local data

Database schemas are defined in each store's table creation code. There are no
schema migrations. When a schema change makes an existing database
incompatible, stop Holark and run:

```sh
holark clear-db
```

Run `holark clear-db /path/to/repository` to select another repository. The
confirmation defaults to yes; pass `--yes` to skip it. This removes that
repository's SQLite database and its `-wal` and `-shm` sidecars from
`HOLARK_HOME/state/repositories/`. Holark recreates the database on its next
launch, and the previous local state is discarded.
