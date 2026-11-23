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

- **Tool:** a single `h2h` binary, written in Go. Run with no arguments it opens a console, an operations centre where the user types `/commands` or plain questions. Every command also works from the shell, and `h2h serve --port <n>` adds a web UI.
- **Default harness:** one harness in the pool is the default. It runs new sessions when none is named and answers what the user types into the console. H2H ships with Claude Code as the default, and the user can change it, for example where a company allows only Copilot or Codex.
- **Sessions:** each harness runs in its own terminal session and workspace under a small keeper process, so sessions survive a crash of H2H and can be rejoined.
- **Visibility:** the user can list active harnesses, follow their logs, and jump into any session's terminal.
- **Reporting:** a session's state comes from the harness's own hooks, and harnesses report proof through a tool H2H provides.
- **Core:** a ticket system with approval rules and budgets, enforced in code.
- **Team:** a pool of harnesses, one owner per domain, optionally with one harness acting as coordinator.
- **Checking:** the builder always checks its own work, and an independent tester is the gate after it.
- **Permissions:** rules declared in config answer a harness's permission requests on the user's behalf. Anything not covered goes to the user.
- **Planning:** kanban by default. An optional project manager adds estimation, sprints bounded by budget, and a retro.
- **Cost:** coordination is done by code first, small agents second and a harness last.
- **Configuration:** declared as code, with the CLI and the UI as views onto it.

## Build and run

Requires Go 1.27 or later, git, and at least one harness installed and logged in.

```
go build -o bin/h2h ./cmd/h2h
cd <a git repository>
h2h
```

| In the console | From the shell | Does |
|---|---|---|
| `/start [harness]` | `h2h start [harness]` | Starts a session in its own worktree under `.h2h/workspaces/` |
| `/ps` | `h2h ps` | Lists sessions |
| `/attach [session]` | `h2h attach [session]` | Opens the session's terminal. Ctrl-\ leaves it running |
| `/logs [-f] [session]` | `h2h logs [-f] [session]` | Prints or follows the session's terminal log |
| `/stop [session]` | `h2h stop [session]` | Stops a session |
| `/rm [session]` | `h2h rm [session]` | Removes a stopped session and its workspace |
| `/config` | `h2h config` | Shows the config. `/config edit` opens it in your editor, and `/config <description>` has the default harness propose the change |
| plain text | | Asks the default harness, which sees the session list and the commands |

Sessions keep running when the console exits. The config is `h2h.toml` at the project root; see [docs/configuration.md](docs/configuration.md).

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
- [docs/authentication.md](docs/authentication.md): the four authentication problems and how to approach each
- [docs/licensing.md](docs/licensing.md): which dependency licences are allowed, and how that is checked
- [docs/prior-art.md](docs/prior-art.md): existing projects, where H2H intends to differ, and the clean-room rule
- [docs/risks.md](docs/risks.md): gaps found in the design review, with proposed directions
- [docs/findings.md](docs/findings.md): what the validation spikes tested, their results, and what they change in the design
- [docs/roadmap.md](docs/roadmap.md): build stages and open questions

Diagrams in these documents are Mermaid and follow the rules in `.claude/skills/mermaid-diagrams/`.

Go code follows the package layout, dependency rule and principles (least privilege, KISS, DRY) in `.claude/skills/go-architecture/`.

## Licence

H2H is released under the Apache License 2.0. See [LICENSE](LICENSE).
