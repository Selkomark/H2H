# H2H: onboarding for AI agents

Read this before changing anything. It says how to work in this repository and where to look. What H2H is and how it behaves is in [README.md](README.md) and `docs/`, and is not repeated here.

## Before you start

- **Check the branches.** Run `git fetch` and `git branch -a`. On 2026-11-25 the code was on `feature/development`, and `main` held only design documents, so a `main` checkout looked like an unbuilt project.
- **Built and designed are different.** Most of `docs/` is design. The status line at the top of the README, "What is built" in [docs/permissions.md](docs/permissions.md#what-is-built) and the command table in [docs/runtime.md](docs/runtime.md#interfaces) help tell them apart. Where a doc and the code disagree about what exists, check the code and tell the user. The stage table in [docs/roadmap.md](docs/roadmap.md) marks only stage 1 as built, although parts of later stages exist.
- **Two meanings of "harness".** Here it is a product H2H runs, such as Claude Code. You may be running in one while working on code that starts them. "Session" also has two meanings; see [Console sessions](docs/runtime.md#console-sessions).

## Setup, build and check

You need git and Go. [go.mod](go.mod) sets the minimum Go version, 1.27.4. On macOS, `brew install go` installed that version on 2026-11-25. Building needs no harness. Whether the default tests pass with no harness installed has not been checked.

| Task | Command, from the repository root |
|---|---|
| Build in the repository | `go build -o bin/h2h ./cmd/h2h` |
| Format check, which must print nothing | `gofmt -l .` |
| Static check | `go vet ./...` |
| Tests | `go test ./...` |

All three checks passed on macOS on 2026-11-25. There is no Makefile and no CI workflow in the repository, so nothing runs these for you. Linux was not checked.

To put `h2h` on your PATH, build with `-o` pointing at a directory that is on it. That changes the user's machine, as does installing Go, so do either only when asked.

## Where things are

| Path | What is there |
|---|---|
| `cmd/h2h` | Entry point only |
| `internal/cli` | Every command, shared by the shell and the console. `cli.go` lists them and handles the internal ones that sessions call: `keeper`, `state`, `permit`, `report` |
| `internal/console` | The full-screen interface |
| `internal/keeper` | The process that holds one harness's terminal |
| `internal/session` | Session records and state on disk |
| `internal/harness` | Starting and talking to harnesses, from their config entries |
| `internal/config` | Config schema and validation. `default.toml` is the shipped config, and its comments describe the settings. `upgrade.go` brings older configs up to date |
| `internal/policy` | Permission decisions, with no I/O |
| `internal/workspace` | Git worktrees, landing work, ignore rules |

The tree in the `go-architecture` skill is a map of where code should go. It includes packages that do not exist yet.

At run time, H2H keeps its state in `.h2h/` at the root of the project it runs in, and sockets, reports, permission requests and wait records in `/tmp/h2h-<uid>/`. The project's config is `h2h.toml`; without one the shipped defaults apply. See [State directory](docs/runtime.md#state-directory).

## What to read for a task

| If you are changing | Read |
|---|---|
| Sessions, workspaces, the console, `ask`, `land`, `doctor`, `install` | [docs/runtime.md](docs/runtime.md) |
| Permissions | [docs/permissions.md](docs/permissions.md) |
| Controlling a running harness: interrupt, answer, mode, model (planned) | [docs/control.md](docs/control.md) |
| A config setting | [docs/configuration.md](docs/configuration.md) |
| Domains or the map | [docs/responsibility-map.md](docs/responsibility-map.md) |
| Dependencies | [docs/licensing.md](docs/licensing.md) |
| Something not built yet | [docs/design.md](docs/design.md), then [docs/roadmap.md](docs/roadmap.md) and [docs/risks.md](docs/risks.md) |

## Rules

The rules live in these files. Claude Code can load them as skills; in any other agent, open the file.

| When | File |
|---|---|
| Writing or reviewing Go, adding a package or dependency | [go-architecture](.claude/skills/go-architecture/SKILL.md) |
| Touching how H2H starts, configures, prompts, hooks or reads a harness | [harness-parity](.claude/skills/harness-parity/SKILL.md) |
| Adding or editing a diagram | [mermaid-diagrams](.claude/skills/mermaid-diagrams/SKILL.md) |

Two rules apply to every change:

- **Clean room.** Copy no code, schema, prompt or test from another orchestrator ([docs/prior-art.md](docs/prior-art.md#clean-room-rule)).
- **Same change, same docs.** If code makes a doc wrong, update the doc with it.

## Common changes

These are starting points, not complete checklists.

**A command**

1. Add it to `commands()` in `internal/cli/cli.go`. The shell, the console and `help` use that list.
2. Decide whether the default harness may run it. `assistantCommands` in `internal/cli/assistant.go` lists those it may, and `true` means the user is asked first. `assistantRules` in the same file is the text the harness is given.
3. Update the command tables in the README and `docs/runtime.md`.

**A harness setting**

1. Add the field in `internal/config/config.go`, with validation if a value can be wrong.
2. Describe it in the comments of `default.toml`, and set it for every shipped harness or record the gap.
3. Add a test that fails if a shipped harness lacks it, like `TestShippedHarnessesPassRoles`.
4. If you replace a shipped value, add the old one to `retired` in `upgrade.go`.
5. Update the settings table in `docs/configuration.md`, and the per-harness table in the doc that covers the feature.

## Testing

- Tests sit beside the code. Many tests of `internal/cli` use shell scripts as stand-in harnesses.
- A test that makes real model calls must be skipped unless asked for. `internal/cli/conversation_real_test.go` runs only with `H2H_REAL_HARNESSES=1`, and its header gives the command. It uses the user's allowance, so ask first.
- The default tests do not show that a real harness accepts a flag or acts on it. The harness-parity skill gives the verification levels to report.

## Running H2H safely

H2H acts on the git repository you run it in, and on the user's real harness logins. To try something, use a scratch repository with at least one commit.

| Command | What to know |
|---|---|
| `h2h help`, `h2h --version` | No model calls |
| `h2h doctor` | Asks each working harness for one short answer, which uses the user's allowance. It may offer to update an older `h2h.toml` |
| `h2h start` | Starts a real harness in a new worktree, may add lines to `.gitignore`, and leaves the harness running until it exits or is stopped |
| `h2h ask`, text typed in the console, `h2h config` | Make model calls |
| `h2h install` | Installs software for the user, globally with the shipped commands |
| `h2h land` | Changes the checked-out branch, and may commit in the workspace first |
| `h2h` | Opens a full-screen console that needs a real terminal. With piped input it reads one line at a time instead |

## Writing docs and commits

- Docs say what is built and what is only designed. A claim about a harness gives the version checked and the level reached.
- Diagrams are Mermaid.
- Recent commits have a subject that says what the change does ("Tell the user when a harness waits for them") and a body that describes the behaviour.

## Not written down

The repository does not settle these. Ask the user instead of assuming.

- Which branch new work starts from, which branch a pull request targets, and when `main` is updated.
- How the Go version is chosen and when it is raised. `go.mod` says 1.27.4 and the README says "1.27 or later". Go has no LTS releases: each major release is [supported until there are two newer ones](https://go.dev/doc/devel/release).
- Which operating system versions are supported. macOS and Linux come first ([Implementation](docs/runtime.md#implementation)).
- How a release is made and how `Version` in `internal/cli/cli.go` changes.

## Keeping this file useful

Add what you had to discover that would have saved you time. Link to the doc that owns a fact rather than copying it, and remove what is no longer true.
