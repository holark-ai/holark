# Browser scenarios

Run these commands from the repository root. Targets are defined in the
[Makefile](../../../Makefile); pull-request scenarios print their usage guidance
when they start. Each opens a browser by default. Append
`PR_SCENARIO_BROWSER_ARGS=` to print the URL without opening it.

| Command | Scenario |
| --- | --- |
| `make harness-scenario` | Interactive harness version discovery playground |
| `make pr-scenario-happy-path` | Pull-request creation |
| `make pr-scenario-outdated-description` | Outdated pull-request description |
| `make pr-scenario-outdated-review` | Outdated review |
| `make pr-scenario-rebase-click-conflict` | Conflict introduced when rebase is clicked |
| `make pr-scenario-regeneration-failure` | Description regeneration failure |
| `make pr-scenario-publication-failure` | Pull-request publication failure |
| `make pr-scenario-description-save-failure` | Description save failure |
| `make pr-scenario-lifecycle` | Rebase, review, and resolution action priority |

## Harness version playground

Run `make harness-scenario` to open a browser playground with an isolated
repository and application database. Use `make harness-scenario
PR_SCENARIO_BROWSER_ARGS=` to print the URL without opening a browser.

The controls above the application independently change each harness's discovery
inputs: supported, too old, too new, prerelease, unrecognized output, failed
version command, or missing executable. Custom raw output and the actual installed
version are also available through the preset menus and Custom popovers.
The compact toolbar wraps on narrow screens; opening a custom input overlays
the application without reducing its height. Changing a control reloads the
application pane at its current URL; the server and saved preferences remain running.
Browse Settings, open New Agent, or use the terminal's add-agent selector to
explore the resulting UI. Switch themes in Settings to compare rendering.

Only executable discovery/version-command results are substituted. The normal
version parser, support classifier, drivers, capability API, settings persistence,
and frontend components are used. No CLI is downloaded or emulated. Launches
require a real installed, authenticated CLI and may make model requests.
Playground controls and endpoints exist only in the browser fixture executable.

Official support initially covers exactly Codex 0.153.4, Claude Code 2.1.220,
and OpenCode 1.18.25, based on the validated versions approved for issue #157.
Each is also the latest officially supported recommendation for this Holark
release. Build metadata does not change support; prereleases are distinct.
Other installed versions remain available with a warning, including when their
version cannot be detected. Missing executables remain unavailable.

This playground exercises phase 1 of #157. The existing probing/cache lifecycle remains;
startup snapshots and the Settings refresh button belong to phase 2. Restart
Holark after installing or updating a harness for now. Retaining an executable
path does not preserve an old binary when that file is replaced. No verified
operation boundary was identified, so command/monitor implementations are unchanged.
