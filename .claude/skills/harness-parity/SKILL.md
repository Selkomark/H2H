---
name: harness-parity
description: Rule and checklist for keeping every supported harness (Claude Code, Copilot CLI, Codex CLI, OpenCode) at the same level of support in H2H. Use whenever a change touches how H2H starts, configures, prompts, hooks or reads a harness, whenever a harness's flag, file or environment variable is added to config or code, and whenever something is tested or verified on one harness.
---

# Harness parity

H2H's promise is one behaviour for every harness (`docs/runtime.md`, "One behaviour for every harness"). A feature that works on one harness and is silently missing on another breaks that promise. This skill is the rule and the procedure that keep it.

## 1. The rule

**Whatever is built or tested for one harness is built and tested for all of them, in the same change.**

The supported harnesses are those in `internal/config/default.toml`: today Claude Code, Copilot CLI, Codex CLI and OpenCode. A new entry there joins this list.

- Do not finish a change with a harness left out. If one harness truly cannot do it, follow section 4 and record the gap. Never leave it unmentioned.
- Do not report "verified" for the feature when only one harness was checked. Report per harness.
- The same result counts, not the same mechanism. One harness may take a flag, another an environment variable or a file. What must match is what the harness ends up knowing or doing.

## 2. Where differences live

Differences between products are data, not code (`.claude/skills/go-architecture/`):

- **In config:** the harness's entry in `default.toml` (for example `context_args`, `context_env`, `ask_args`). Code reads the setting and never branches on a product name.
- **Placeholders, not special cases:** when products need the same value in different shapes, add a placeholder that config fills in (for example `{roles}` and `{roles_dir}`), and validate that it is used.
- **A shipped-defaults test:** for each capability, a test over `config.Template()` fails if any shipped harness lacks it (for example `TestShippedHarnessesPassRoles`). Add one for every new capability.

## 3. How to verify, for each harness

Install all three CLIs in a scratch directory, never globally and never into the repository:

```sh
cd <scratch> && npm init -y && npm install @anthropic-ai/claude-code @github/copilot @openai/codex @opencode/cli
```

Then check each harness at the deepest level that can be reached, and record the level reached for each one.

| Level | What it proves | How |
|---|---|---|
| 1. Documented | The option exists in this version | `<cli> --help`, `<cli> help <topic>`. Note the version (`--version`) |
| 2. Accepted | The CLI starts with the option and does not reject it | Start it in a pseudo-terminal for a few seconds (`script -qc "<cli> <args>" /dev/null`). Run the same with a misspelt option as a control: the control must fail, or the test proves nothing. Use the CLI's strict mode where it has one (Codex: `--strict-config`) |
| 3. Loaded | The CLI actually reads the value | A command that reports what was loaded (Copilot: `copilot instruction list --json`), or inspecting the running process (`/proc/<pid>/cmdline`, `/proc/<pid>/environ`) when started through `h2h start` |
| 4. Behaves | The model acts on it | Needs a signed-in harness. Run the same scenario on every harness and compare |

Levels 1 to 3 need no login, so they are always done for all harnesses. Level 4 is done for all harnesses or for none. If it is done for only some, say so explicitly.

Run the end-to-end check through H2H itself, not only the CLI: a scratch git repository, an `h2h.toml` that points each harness's `command` at the scratch install but keeps the shipped settings, `h2h start` for each, then inspect the processes and logs, then `stop` and `rm`.

Point `CODEX_HOME`, `COPILOT_HOME` and `HOME` at scratch directories when a check might write user configuration, and delete them afterwards.

## 4. When a harness cannot do it

1. Look for a different mechanism with the same result: another flag, an environment variable, a config override, or a file in a directory H2H owns. H2H never writes into the workspace or into a harness's own config files (`docs/runtime.md`).
2. Only if none exists, accept the difference. Declare it in that harness's config entry, make H2H tell the user at the point where it matters (as `h2h start` does when a harness cannot receive its role), and add it to the parity table in the relevant doc.

## 5. Recording the result

Every capability that differs per harness has a table in its doc, with one row per harness: the mechanism, the CLI version checked, and the verification level reached. Update it in the same change. `docs/responsibility-map.md`, "Passing roles to each harness", is the example.

## 6. Before finishing

- Every shipped harness has the capability, or a recorded and surfaced gap.
- A shipped-defaults test covers the capability.
- The per-harness table in the docs is current, with versions and levels.
- No product name appears in code outside `internal/harness/<product>` (config and docs are fine).
- The report to the user states the verification level per harness, including what was not reached.
