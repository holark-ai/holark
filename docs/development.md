# Development and testing

## Build and test

Run builds and tests through the Makefile because Go packages embed generated
frontend assets.

```sh
make build
make test
make test-go PKG=./internal/localshell
make test-integration
make lint
make test-browser
```

The production frontend is embedded in the Go executable. Monaco, its workers,
and the rest of the UI are bundled and do not load from a CDN.

## Real agent regression tests

`make test-claude` provides commit-fork coverage using the installed and
authenticated Claude Code CLI. It builds Holark for real hook ingestion and
uses temporary configuration with an unexpired access token from
`.credentials.json` or the default macOS Keychain entry. Run Claude Code
normally first if its login needs refreshing. `ANTHROPIC_API_KEY` and
`CLAUDE_CODE_OAUTH_TOKEN` are also supported; the test never copies refresh
tokens.

`make test-opencode` runs the OpenCode native-TUI smoke test and
commit-fork coverage. It uses temporary XDG directories, copying file-backed
authentication and recent model selection when available.

These commands make real model requests and are excluded from the default test
suite.

## Frontend previews

Start the frontend development server from the repository root:

```sh
npm --prefix web run dev
```

The previews use real application components with sample Acme Dashboard data.
They do not require the Go backend, and terminal output is illustrative rather
than connected to an agent process.

- [Repository tree](http://127.0.0.1:5173/__dev/tree/web)
- [Repository root](http://127.0.0.1:5173/__dev/)
- [Holon](http://127.0.0.1:5173/__dev/holon)
- [Pull requests](http://127.0.0.1:5173/__dev/pulls)
- [My work](http://127.0.0.1:5173/__dev/my-work)
- [Commit history](http://127.0.0.1:5173/__dev/?view=changes)

The pull-request preview includes sample member filters, fuzzy title search,
sorting, and synchronization. Open a row to view its pull-request detail;
sample data resets on reload. The My work preview includes assigned issues,
assigned pull requests, review requests, filters, counts, and pagination. The
repository previews also support file and branch navigation.

The commit-history preview loads history in pages of 50. Use **Commit history
preview** at the bottom left to change the delay, fail the next older-page
request, or simulate a new commit on the main branch. Reload to reset any
preview scenario.
