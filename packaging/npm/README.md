# Holark

A local browser workspace for coding agents and Git repositories.
Supports Linux and macOS on x64 and ARM64, including Apple Silicon.

## Getting started

Requirements:

- Node.js 20.19+ with npm (Node.js 24 LTS recommended), Git, and a browser.
- An installed, authenticated Codex, Claude Code, or OpenCode CLI for agent work.
- GitHub CLI (`gh`), authenticated with `gh auth login`, for GitHub features.

```sh
npm install -g holark
holark /path/to/your/repository
```

npm installs the `holark` command in its global bin directory. If it is not
found, add that directory to PATH:

```sh
export PATH="$(npm prefix -g)/bin:$PATH"
```

Save this export in `~/.zshrc` or `~/.bashrc` after any Node.js version-manager
setup. Use a Git checkout with at least one commit and a GitHub `origin` remote
for GitHub features. Open the printed URL and keep Holark running.

The embedded IDE uses VS Code Server, provisioned on demand through a compatible
`code` CLI or a managed download. No manual server installation is needed;
initial provisioning requires internet access.

For GitHub binaries, source builds, and full usage, see the
[repository README](https://github.com/holark-ai/holark#getting-started).
