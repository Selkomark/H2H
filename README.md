# H2H

**Run your coding agents as a team, from one console.**

H2H starts Claude Code, Copilot CLI, Codex CLI, OpenCode and Grok side by side in your project. Each one works in its own copy of the code. You see who is running and who needs you, hand them work, and decide what lands in your branch.

> **Status: early.** The console, sessions and configuration work today. Tickets, approval of work, budgets and the web UI are [planned](docs/roadmap.md) and not built. H2H is developed on macOS. Linux has not been checked, and Windows is not supported yet.

In H2H, a **harness** is one of these agent products, with its own model, tools and login.

## What you can do

- **Run several agents at once.** Each gets its own [git worktree](https://git-scm.com/docs/git-worktree), so they do not edit the same files.
- **Ask in plain words.** Type what you want in the console. Your default agent can start others, hand them work and report back. It asks you before it starts, stops or removes anything.
- **Give each agent a role.** One owns the frontend, another tests it. H2H tells each its role when it starts.
- **Step in at any time.** Attach to an agent's own screen, then leave it running.
- **Decide what you keep.** Work reaches your branch only when you land it. H2H does not push.
- **Keep them running.** Agents carry on after you close the console, and you can resume the console later.

## Before you start

You need:

- git 2.17 or later, and a project that is a git repository with at least one commit
- Go 1.27.4 or later, to build H2H
- at least one supported agent, installed and signed in

H2H uses your own logins and subscriptions. Whatever you ask an agent to do counts against that agent's plan.

## Install

There are no ready-made downloads yet, so build H2H from source:

```sh
git clone https://github.com/Selkomark/H2H.git
cd H2H
go build -o ~/.local/bin/h2h ./cmd/h2h
```

Any folder on your `PATH` works in place of `~/.local/bin`. To update, run `git pull` and build again.

## Quick start

Go to your project, then check your setup:

```sh
cd your-project
h2h doctor
```

`doctor` tells you what is missing and how to fix it. To check logins, it asks each installed agent for one short answer.

Missing an agent? `h2h install` shows what it can install and asks before it installs anything. It uses npm for most agents. For Grok, it downloads and runs the install script from `x.ai`.

Then open the console:

```sh
h2h
```

Claude Code is the default agent. To use another one, run `/config`.

A first session looks like this:

1. Type `/start`. The default agent starts in its own copy of your project.
2. Type `/attach`, give it a task, and press `Ctrl-\` to return. It keeps working.
3. When the work is ready, type `/land`. H2H shows what would change and asks before it merges.

Or skip the commands and type what you want in plain words.

## The console

Type `/` to see the commands. There are tabs for the **Console**, a **Map** of your team, your **Harnesses** (running out of listed) and saved **Sessions**. The frame glows while something is working.

| In the console | From the shell | What it does |
|---|---|---|
| `/start [agent \| domain]` | `h2h start` | Starts an agent, or the agent that owns a domain |
| `/ps` | `h2h ps` | Shows your agents and their state |
| `/ask <id> <request>` | `h2h ask` | Sends work to a running agent and shows its report |
| `/attach [id]` | `h2h attach` | Opens an agent's own screen. `Ctrl-\` leaves it running |
| `/logs [-f] [id]` | `h2h logs` | Shows or follows an agent's output |
| `/land [id]` | `h2h land` | Brings an agent's work into your branch |
| `/stop [id \| all]` | `h2h stop` | Stops agents |
| `/rm [id \| all]` | `h2h rm` | Removes agents and their copies of the project. It asks before discarding work |
| `/map` | `h2h map` | Shows who owns what, and the order of work |
| `/acp [agent \| off]`, `/model`, `/mode` | `h2h acp` | Demo, on the `demo/acp` branch only: the console talks to an agent over ACP, which can run H2H's commands as tools; see [docs/acp.md](docs/acp.md) |
| `/config` | `h2h config` | Sets up your team in a conversation |
| `/doctor` | `h2h doctor` | Checks your setup |
| `/install [--update]` | `h2h install` | Installs or updates agents |
| `/sessions` | `h2h sessions` | Lists or removes saved console sessions |
| `/resume <session>` | `h2h --resume` | Returns to a saved console session |
| `/clear` | | Starts a new conversation with the default agent |
| `/cls` | | Clears the screen and keeps the conversation |
| `/exit` | | Leaves the console. Agents keep running |

Run `h2h help` for the full list.

## Set up your team

H2H works without any setup. To give agents roles, run `/config` and describe your team, or write `h2h.toml` in your project's root:

```toml
default_harness = "claude"

# The agents this team uses. A name and its command are enough.
[harness.claude]
command = "claude"

[harness.codex]
command = "codex"

[domain.ui]
harness = "claude"
does = "The web frontend"
paths = ["web/**"]

[domain.e2e]
harness = "codex"
does = "End-to-end tests"
depends_on = ["ui"]

[permissions]
mode = "ask"
allow = ["git status*", "git diff*", "npm test*"]
deny = ["git push*"]
```

A **domain** is an area of responsibility with one agent in charge. `/start ui` starts its owner. `paths` and `depends_on` describe the work to your agents and the map; H2H does not enforce them.

List every agent your team uses. For a built-in agent, H2H fills in any setting you leave out. You can commit `h2h.toml` with your project. The [configuration guide](docs/configuration.md) lists every setting.

## Supported agents

| Agent | When it stops to ask you something | `[permissions]` rules apply |
|---|---|---|
| Claude Code | Permission requests appear as a dialog in the console. Answer other questions after `/attach` | Yes |
| Copilot CLI | The console says it is waiting. Answer after `/attach` | No |
| Codex CLI | The console says it is waiting. Answer after `/attach` | No |
| OpenCode | The console can only say it **may** be waiting, during an `/ask` | No |
| Grok | The console can only say it **may** be waiting, during an `/ask` | No |

For every agent except Claude Code, its own permission settings decide what it may do. How far each feature has been tested differs between agents; the [runtime guide](docs/runtime.md) records it for each.

## Stay safe

- **Check your agents' own permissions.** An agent set to approve everything, such as Grok with `permission_mode = "always-approve"`, still does so inside H2H. The `[permissions]` rules above do not stop it.
- **A separate copy is not a sandbox.** A git worktree keeps agents' edits apart. It does not stop an agent from reaching the rest of your computer.
- **You grant folder trust.** If H2H cannot confirm that an agent trusts your project, it will not send it a request, and asks you to attach and answer yourself.
- **Be careful with `mode = "auto"`.** It allows every permission request that reaches H2H unless a `deny` rule matches.

## If something goes wrong

Start with `h2h doctor`.

| You see | Try this |
|---|---|
| An agent is "installed but does not run" | Run `/install <agent>`. A terminal app can put a placeholder command on your `PATH` that only looks like the agent |
| An agent is installed but cannot answer | Open the agent yourself. Check that you are signed in and that its selected model works with your plan |
| H2H will not send a request because of trust | Use `/attach <id>`. Answer the trust question only if you trust the project, then give the request there |
| An agent is waiting | Use `/attach <id>`, answer, and press `Ctrl-\` |
| The glow or screen updates feel slow, for example over SSH | The glow redraws the whole screen while work runs. Setting `NO_COLOR=1` turns it off, along with other colour |

## Your files

H2H keeps its sessions, logs and project copies in `.h2h/` in your project, and adds it to your `.gitignore`. Logs and saved console sessions can contain anything shown on screen, so treat them as private. Some short-lived working files are kept in `/tmp/h2h-<your user id>/`.

To remove H2H from a project, land any work you want to keep, run `h2h rm all`, then delete `.h2h/` and `h2h.toml`. To uninstall it, delete the `h2h` program.

## Playground

[Selkomark/h2h-playground](https://github.com/Selkomark/h2h-playground) is a separate project for trying H2H on. Its `h2h.toml` shows a team with domains, and the work in it is under `projects/`.

## Where H2H is heading

The goal is a team in which each agent owns a domain, reports its progress, and has its work checked and approved. See the [design](docs/design.md), the [roadmap](docs/roadmap.md), and the plan for [controlling every agent from H2H](docs/control.md).

## For contributors

Read [AGENTS.md](AGENTS.md), which is written for AI agents and applies to people too. To check a change:

```sh
gofmt -l .
go vet ./...
go test ./...
```

H2H is written from scratch and copies no code from similar tools ([prior art](docs/prior-art.md)), and it uses only [permissively licensed dependencies](docs/licensing.md). Go code follows `.claude/skills/go-architecture/`, a change for one agent covers them all (`.claude/skills/harness-parity/`), and diagrams follow `.claude/skills/mermaid-diagrams/`.

| Document | About |
|---|---|
| [Design](docs/design.md) | Architecture and roles |
| [Runtime](docs/runtime.md) | Sessions, workspaces, the console and each command |
| [Configuration](docs/configuration.md) | Every setting |
| [Permissions](docs/permissions.md) | How permission requests are answered |
| [Responsibility map](docs/responsibility-map.md) | Domains and the map |
| [Control](docs/control.md) | Planned controls for running agents |
| [Governance](docs/governance.md) | Tickets, proof and approvals |
| [Planning](docs/planning.md) | Kanban and managed modes |
| [Cost](docs/cost.md) | Where cost comes from |
| [Authentication](docs/authentication.md) | Logins and access |
| [Risks](docs/risks.md) | Open gaps |
| [Findings](docs/findings.md) | What has been tested |
| [Roadmap](docs/roadmap.md) | Build stages and open questions |

## Licence

H2H is released under the Apache License 2.0. See [LICENSE](LICENSE).
