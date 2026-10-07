<p align="center">
  <img src="docs/assets/presentation/holark-logo.png" alt="Holark logo" width="120">
</p>

<h1 align="center">Holark</h1>

<p align="center">
  <strong>The workspace for building software with AI</strong>
</p>

<p align="center">
  Bring your coding agents, code, and reviews into one local workspace.
</p>

<p align="center">
  <a href="https://holark.ai"><img alt="Website" src="https://img.shields.io/badge/website-holark.ai-2b2b2b?style=flat-square&labelColor=555"></a>
  <a href="https://www.npmjs.com/package/holark"><img alt="npm" src="https://img.shields.io/npm/v/holark?style=flat-square&labelColor=555&color=2b2b2b&label=npm"></a>
  <a href="https://discord.gg/gh86FvHND"><img alt="Discord" src="https://img.shields.io/badge/discord-join-2b2b2b?style=flat-square&labelColor=555"></a>
</p>



https://github.com/user-attachments/assets/e2417858-6fab-4dd1-972d-4894f6de2dd6



## Why Holark

**01 / Context — Give agents the full picture.**<br>
Keep the task, discussion, and code changes together, so each agent’s work stays
connected to what you’re trying to achieve.

**02 / Attention — Know where you’re needed.**<br>
See which agents need an answer and which changes need review. Keep work moving
without opening every conversation.

**03 / Coordination — A shared workspace, from task to merge.**<br>
Bring your agents and their work together. With GitHub integration built in,
you can move between implementation, feedback, and review without managing the
plumbing.

## Get started

Holark supports macOS and Linux on x64 and ARM64.

You’ll need:

- Node.js 20.19 or newer and Git
- Codex, Claude Code, or OpenCode, installed and signed in
- GitHub CLI, signed in with `gh auth login`, for GitHub features

Install Holark:

```sh
npm install -g holark
```

Run it inside a Git repository:

```sh
cd /path/to/your/repository
holark
```

Open the printed URL.

<details>
<summary><code>holark</code> not found?</summary>

Add npm’s global bin directory to your `PATH`:

```sh
export PATH="$(npm prefix -g)/bin:$PATH"
```

Add this after any Node.js version-manager setup in your shell profile.

</details>

## Your first task

1. Open **New Holon**, choose an agent and starting branch, and describe the
   work.
2. Holark creates an isolated branch, worktree, agent session, and terminals.
3. Follow the agent, try the changes, run your checks, and open a pull request
   when it is ready.

A **Holon** is Holark’s workspace for one task. Its discussion, agent session,
code changes, terminals, and review stay together.

## Local by design

Holark runs as a single-user local process. Its HTTP and WebSocket interfaces
only accept loopback connections and require a fresh secret on every launch.
Your repository is not sent through a hosted relay.

Local state, worktrees, caches, agent runtime files, and IDE data are stored
under `~/.holark` by default.

## Build from source

Building requires Go 1.24 or newer, Node.js, npm, Git, and Make.

```sh
git clone https://github.com/holark-ai/holark.git
cd holark
make build
./bin/holark /path/to/your/repository
```

The frontend and terminal worker are embedded in the resulting executable.

## Contributing

Bug reports and product feedback are welcome.

Holark is not currently accepting external code contributions. See
[CONTRIBUTING.md](CONTRIBUTING.md) for details and
[SECURITY.md](SECURITY.md) for reporting security vulnerabilities.

## License

Holark is Fair Source software licensed under the
[Functional Source License 1.1 with an Apache 2.0 Future License](LICENSE.md)
(`FSL-1.1-ALv2`).
