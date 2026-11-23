---
name: go-architecture
description: Architecture, file layout and engineering principles for H2H's Go code. Use before creating or moving a Go package, adding a dependency, defining an interface, wiring a new component into the binary, or reviewing Go code in this repo. Covers the package layout (official Go module layout plus the Google Go Style Guide), the dependency rule between packages, and the principles every change is checked against: least privilege, KISS and DRY.
---

# Go architecture

H2H is one Go binary, `h2h`. This skill fixes how its code is organised so that every change lands in the same shape. The design it implements is in `docs/`; start with `docs/design.md` and `docs/runtime.md`.

## 1. Sources

Go has no single mandated project layout, so H2H follows the two official sources and nothing else:

- **Layout:** [Organizing a Go module](https://go.dev/doc/modules/layout), from the Go team. H2H uses its "server project" shape: commands under `cmd/`, all other code under `internal/`.
- **Style:** the [Google Go Style Guide](https://google.github.io/styleguide/go/), including its Decisions and Best Practices pages. Where this skill is silent, that guide decides.

The `golang-standards/project-layout` repository is not an official standard and is not followed. In particular there is no `pkg/`, no `src/`, and no directory created only because a template has it.

## 2. Architecture

A small core with adapters at the edges, built from plain Go packages. There is no framework and no layer-named package.

- **Packages are named for what they provide,** one responsibility each: `ticket`, `policy`, `keeper`. Never for a layer or a kind of code: no `models`, `services`, `controllers`, `util`, `common`, `helpers`.
- **Core packages are pure.** `ticket` and `policy` hold the rules that must be exact. They do no I/O, start no processes and import no adapter, so they are tested with plain table-driven tests.
- **Edges are adapters.** Anything that touches the outside (a harness product, git, the disk, a socket, HTTP) sits in its own package behind a small interface.
- **Interfaces belong to the consumer.** The package that needs a behaviour declares the interface, with only the methods it calls. Implementations return concrete types. An interface is added when a second implementation or a test needs it, never in advance.
- **Wiring happens in one place.** `cmd/h2h/main.go` parses nothing and decides nothing: it calls `cli`, which builds the dependencies and passes them in explicitly. No global state, no `init()` side effects, no dependency-injection library.

### Dependency rule

Imports point inward, and never sideways between adapters:

```
cmd/h2h  ->  cli  ->  console, orchestrator, keeper, web
orchestrator  ->  ticket, policy, session, config (and interfaces it declares)
adapters (harness/*, store, workspace)  ->  core types only
ticket, policy  ->  standard library only
```

- A core package never imports an adapter.
- One harness adapter never imports another. Shared behaviour goes in `harness`.
- Product names (`claude`, `copilot`, `codex`) appear only in the shipped config (`internal/config/default.toml`) and inside `internal/harness/<product>`. Prefer config: a product gets a package only for behaviour that data cannot express. Code elsewhere that branches on a product is a bug: the difference belongs in the adapter or in config (see `docs/runtime.md`, "One behaviour for every harness").

## 3. Layout

```
H2H/
├── go.mod                  module github.com/Selkomark/H2H
├── LICENSE
├── README.md
├── cmd/
│   └── h2h/
│       └── main.go         entry point only; calls internal/cli
├── internal/
│   ├── cli/                the commands, shared by the shell and the console, and their styling
│   ├── console/            the interactive prompt: line editing, history, completion
│   ├── orchestrator/       applies the rules to ticket and session events
│   ├── ticket/             ticket type, states and transitions (pure)
│   ├── policy/             permission and approval decisions (pure)
│   ├── session/            session state model and the state directory
│   ├── keeper/             holds one harness's pseudo-terminal, writes state, serves attach
│   ├── workspace/          git worktrees per session
│   ├── harness/            starting a harness from its config entry; later the adapter contract
│   │   ├── claude/         added only when a product needs code, not just config
│   │   ├── copilot/
│   │   └── codex/
│   ├── report/             the reporting tool a harness calls
│   ├── store/              ticket store
│   ├── config/             schema, plan and apply
│   └── web/                h2h serve, with assets embedded
└── docs/
```

Rules:

- **Everything is `internal/`.** H2H ships a binary, not a library, so nothing is importable from outside until there is a deliberate reason to publish an API.
- **`cmd/h2h/` instead of a root `main.go`,** so the installed binary is named `h2h` and not after the module path.
- **One command, two views.** A command is one function in `cli`. `h2h ps` and `/ps` both call it; the console only reads lines and never implements behaviour.
- **This tree is a map, not a scaffold.** A package is created in the roadmap stage that first needs it (`docs/roadmap.md`). Do not add empty packages, placeholder files or `doc.go` stubs ahead of use.
- **Tests sit beside the code** as `*_test.go`. Fixtures go in a `testdata/` directory in the same package.
- **Split by file before splitting by package.** A new package needs its own responsibility and a clear import direction. Avoid packages with one small file, and avoid nesting deeper than `internal/<package>/<subpackage>`.
- **Package names** are short, lower-case, one word, no underscores, and read well at the call site: `ticket.State`, not `ticket.TicketState`.

## 4. Principles

Check every change against these three. If a change breaks one, say so and justify it in the change.

### Least privilege

Each process, package and session gets only what its job needs.

- **Processes.** The keeper holds a terminal and writes state, and nothing else: no ticket logic, no network, no credentials. The orchestrator never holds a harness's model-provider login (`docs/authentication.md`).
- **Files and sockets.** State directories are created `0700`, files `0600`, and local sockets are reachable only by the same OS user. The web UI binds to localhost and requires a token unless the user explicitly chooses otherwise.
- **Sessions.** A harness is started with an explicit, minimal environment. It receives only the credentials its domain is declared to use, by reference, and a reporting token valid for its own session only.
- **Secrets.** Never in the binary, the config, the ticket store or a log. Config names a secret; the value comes from the environment or a secret store.
- **Code.** Identifiers are unexported unless another package needs them. Interfaces carry only the methods the consumer calls. Pass a function the narrow value it needs, not the whole config or orchestrator.
- **Defaults deny.** When a rule is missing or a check fails, the answer is "ask the user" or "deny", never "allow" (`docs/permissions.md`).

### KISS

The simplest thing that satisfies the current roadmap stage.

- **Standard library first.** A dependency is added only for something costly or risky to write, such as a pseudo-terminal or a database driver, and only under an allowed licence (`docs/licensing.md`).
- **No speculative generality.** No interface with one implementation and no test double, no option nobody sets, no generic type where a concrete one works, no plugin mechanism until a stage calls for it.
- **Plain control flow.** Errors are returned and wrapped with `%w`, not hidden or panicked. `context.Context` is the first parameter of anything that blocks. A goroutine is started only with a known way to stop it.
- **Rules in code are boring on purpose.** Ticket transitions and policy decisions are tables and small functions that a reviewer can read in one pass.
- **Readable beats clever.** Follow the style guide's order of priorities: clarity, simplicity, concision, maintainability, consistency.

### DRY

One authoritative place for each piece of knowledge. This is about knowledge, not about lines that look alike.

- **One definition per fact.** Session states, ticket states, the config schema and the result contract are each defined once and imported. The CLI and the web UI are two views over the same functions, never two implementations.
- **One contract for all harnesses.** What every adapter must do is the `harness` interface and its conformance test. Adapters implement it; they do not restate it.
- **Docs and code do not duplicate each other.** A comment explains why, and links to the doc for the design. If code makes a doc wrong, update the doc in the same change.
- **Do not over-apply it.** Two harness adapters with similar-looking code stay separate when the products can drift apart. A little copying is better than a shared helper that couples them, or a new dependency. Extract only when the same rule has to change in both places at once.

## 5. Before finishing a Go change

- `gofmt` and `go vet` are clean, and `go test ./...` passes.
- New packages fit the layout and the dependency rule above.
- New exported names, interfaces and dependencies each have a present need.
- Any new file, socket, environment variable or credential path was checked against least privilege.
- No code, schema, prompt or test was copied from another orchestrator (`docs/prior-art.md`, clean-room rule).
