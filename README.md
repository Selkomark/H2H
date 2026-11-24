# H2H: Harness-to-Harness Orchestration

**Status: stage 1 of the [roadmap](docs/roadmap.md) is built: the `h2h` console, supervised sessions, and configuration. The rest is design, validated by three spikes.**

H2H lets complete agent harnesses (Claude Code, Copilot CLI, Codex CLI and others) work as a team. Each harness owns a domain of responsibility, reports its status, and has its work checked and approved, instead of acting as a worker that answers one task and forgets.

A harness is a full agent product: a model plus its own loop, tools, permissions and ability to spawn sub-agents. H2H orchestrates whole harnesses that are opaque to each other. It does not define agents of its own.

The motivating case: one harness builds a UI, another tests it because it is better at UI testing, and a third owns customer service end to end. A human sees the status of each side by side and approves or rejects their work.

## Why

A team of harnesses that own domains should handle multi-part projects better than a central orchestrator directing every step:

- Each owner keeps the context, tools and access for its domain, so no single orchestrator has to hold the detail of everything.
- Work is checked by someone who did not produce it: another harness, a script or a human.
- Harnesses are interchangeable behind a common protocol, so a harness with a different strength or budget can join later without changing the rest.

The expected cost is coordination overhead. Similar tools exist and are known to be expensive to run, so cost is a design constraint from the start; see [docs/cost.md](docs/cost.md) and [docs/prior-art.md](docs/prior-art.md).

## Intended shape

- **Tool:** a single `h2h` binary, written in Go. Run with no arguments it opens a full-screen console, an operations centre with the input at the bottom, where the user types `/commands` or plain questions and suggestions appear while typing. Every command also works from the shell, and `h2h serve --port <n>` adds a web UI.
- **Default harness:** one harness in the pool is the default. It runs new sessions when none is named and answers what the user types into the console. H2H ships with Claude Code as the default, and the user can change it, for example where a company allows only Copilot or Codex.
- **Sessions:** each harness runs in its own terminal session and workspace under a small keeper process, so sessions survive a crash of H2H and can be rejoined.
- **Visibility:** the user can list active harnesses, follow their logs, and jump into any session's terminal.
- **Reporting:** a session's state comes from the harness's own hooks, and harnesses report proof through a tool H2H provides.
- **Core:** a ticket system with approval rules and budgets, enforced in code.
- **Team:** a pool of harnesses, one owner per domain, optionally with one harness acting as coordinator.
- **Checking:** the builder always checks its own work, and an independent tester is the gate after it.
- **Permissions:** rules declared in config answer a harness's permission requests on the user's behalf. Anything not covered goes to the user as a dialog in the console, or is allowed in auto mode. Built for Claude Code; see [docs/permissions.md](docs/permissions.md#what-is-built).
- **Planning:** kanban by default. An optional project manager adds estimation, sprints bounded by budget, and a retro.
- **Cost:** coordination is done by code first, small agents second and a harness last.
- **Configuration:** declared as code, with the CLI and the UI as views onto it.

## Build and run

Requires Go 1.27 or later, git, and at least one harness installed and logged in.

```
go build -o bin/h2h ./cmd/h2h
cd <a git repository>
h2h doctor
h2h install
h2h
```

`h2h doctor` says what is missing, if anything, and how to fix it. `h2h install` installs the harnesses that are missing, after asking which.

| In the console | From the shell | Does |
|---|---|---|
| `/start [harness \| domain]` | `h2h start [harness \| domain]` | Starts a session in its own worktree under `.h2h/workspaces/`. Naming a domain starts its owner and passes its role as XML |
| `/ps` | `h2h ps` | Lists the harnesses that were started. In the console it opens the Harnesses tab, which updates live: arrows select, Enter attaches, `s` stops, `d` deletes |
| `/attach [id]` | `h2h attach [id]` | Opens a running harness's terminal. Ctrl-\ leaves it running |
| `/logs [-f] [id]` | `h2h logs [-f] [id]` | Prints or follows a harness's terminal log |
| `/stop [id]` | `h2h stop [id]` | Stops a running harness |
| `/rm [id]` | `h2h rm [id]` | Removes a stopped harness and its workspace |
| `/sessions [rm <session> \| rm all]` | `h2h sessions [rm <session> \| rm all]` | Lists the saved console sessions, or removes them. In the console it opens the Sessions tab: arrows select, Enter jumps into the session, `d` deletes it |
| `/resume <session>` | | Switches the console to a saved session, as Enter on the Sessions tab does |
| `/clear` | | Empties the console's page and carries on in the same session. The lines you entered stay in reach of the Up key |
| | `h2h --resume [session]`, or `-r` | Opens the console on an earlier session, with its page and the lines that were entered. Without a session it resumes the only one, or asks which |
| `/map` | `h2h map` | Draws the config as a graph: a box for each domain with its owner, and arrows for the flow of work between them. In the console it opens the Map tab, where the arrow keys or a click select a domain and Enter opens what it is for and who does what in it: the harness in charge and a table of each sub-agent's job. "Show more" answers the rest in plain words: is it running, which files, when it can start, where its work goes. From the shell it prints the drawing with each domain's setup under it; see [docs/responsibility-map.md](docs/responsibility-map.md) |
| `/install [--update] [harness ...]` | `h2h install [--update] [harness ...]` | Installs the harnesses that are missing, at their latest version. `--update` checks the installed ones for a newer version instead. Either way a dialog lists what was found, ticked, and only what stays ticked is installed; see [docs/runtime.md](docs/runtime.md#install) |
| `/doctor` | `h2h doctor` | Checks that everything H2H needs is in place: git, the project, the state directory, sessions, the config, and every harness: installed, running, logged in, and trusting the project. Proving a login asks each harness for one short answer. Changes nothing; see [docs/runtime.md](docs/runtime.md#doctor) |
| `/config` | `h2h config` | Sets the config up in a conversation: the default harness asks how the team should be arranged, and once you say you are done it writes the file, doctor checks it and the map shows the result |
| `/config --manual` | `h2h config --manual` | Opens the config in your editor, starting from the shipped one if the project has none |
| `/config show` | `h2h config show` | Prints the config |
| `/ask <id \| harness \| domain> <request>` | `h2h ask <id \| harness \| domain> <request>` | Hands a request to a running harness: it is typed into that harness's own session, where you can attach and watch, and its report is printed when it is done. If none is running for that name, the harness answers in a fresh read-only run instead |
| plain text | | Talks to the default harness, which sees the map, the domains and the running harnesses, and can run H2H's commands itself: list, start, stop, remove, read logs, ask a domain's owner, and more. It asks before anything that changes something, and cannot change the config. Its answer appears as it is written |

Harnesses keep running when the console exits, and the console's own session is saved so it can be resumed; see [docs/runtime.md](docs/runtime.md#console-sessions). The config is `h2h.toml` at the project root; see [docs/configuration.md](docs/configuration.md).

## What has been validated

Three short spikes tested the riskiest assumptions on Claude Code, Copilot CLI and Codex CLI, on a small sample. None invalidated the design.

- **Session state:** the state of a harness (working, waiting for input, idle, hung, exited) can be read from its own hooks, without reading its screen.
- **Permissions:** a permission request can be answered in code: allow, deny with instructions the model then follows, or hold for the user's answer. No prompt reached the screen.
- **Baseline:** on one small task, a builder and a tester in separate sessions cost about the same as one harness using sub-agents. Testers from a different product found two real defects that a same-product tester and 75 hidden tests missed.

The detail of each test is in [docs/findings.md](docs/findings.md). The spike code was throwaway and is not part of this repository.

## Principles

- **Clean-room build.** H2H may share ideas and architecture with existing tools, but none of their code is copied into it, and it is not an extension of any of them. See [docs/prior-art.md](docs/prior-art.md).
- **Permissive dependencies only,** so a company can adopt H2H without licence problems. See [docs/licensing.md](docs/licensing.md).
- **One behaviour for every harness.** Differences between harnesses are hidden behind small adapters or declared in config. See [docs/runtime.md](docs/runtime.md).

## Documentation

Read in this order:

- [docs/design.md](docs/design.md): architecture, roles and the communication model
- [docs/runtime.md](docs/runtime.md): sessions, keepers, workspaces and trust, hooks, monitoring, crash recovery and the choice of Go
- [docs/governance.md](docs/governance.md): tickets, proof of work, who checks the work, approvals, autonomy and intended use
- [docs/permissions.md](docs/permissions.md): how H2H answers a harness's permission requests on the user's behalf
- [docs/planning.md](docs/planning.md): kanban and managed modes, the project manager, estimation, sprints and the retro
- [docs/cost.md](docs/cost.md): where cost comes from and how the design limits it
- [docs/configuration.md](docs/configuration.md): config as code, the CLI and the UI
- [docs/responsibility-map.md](docs/responsibility-map.md): who owns what, and how it is drawn in the console and later the web UI
- [docs/authentication.md](docs/authentication.md): the four authentication problems and how to approach each
- [docs/licensing.md](docs/licensing.md): which dependency licences are allowed, and how that is checked
- [docs/prior-art.md](docs/prior-art.md): existing projects, where H2H intends to differ, and the clean-room rule
- [docs/risks.md](docs/risks.md): gaps found in the design review, with proposed directions
- [docs/findings.md](docs/findings.md): what the validation spikes tested, their results, and what they change in the design
- [docs/roadmap.md](docs/roadmap.md): build stages and open questions

Diagrams in these documents are Mermaid and follow the rules in `.claude/skills/mermaid-diagrams/`.

Anything built or tested for one harness is built and tested for every harness, following `.claude/skills/harness-parity/`.

Go code follows the package layout, dependency rule and principles (least privilege, KISS, DRY) in `.claude/skills/go-architecture/`.

## Licence

H2H is released under the Apache License 2.0. See [LICENSE](LICENSE).
