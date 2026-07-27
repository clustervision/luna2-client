# Go project engineering guide

This document defines the engineering principles, architecture defaults, quality gates, and migration practices that apply to Go projects in this style. It is intentionally generic: it does not reference any specific application domain, directory layout, or external tooling.

Apply these rules unless project-specific documentation explicitly overrides them. When a section does not apply (e.g. a project never shells out, so the executor rules are moot), say so explicitly rather than silently ignoring it.

---

## Language and ecosystem

The implementation language is Go.

- Do not use Python.
- Do not propose Python.
- Do not introduce Python helper scripts.

The future direction is Go binaries, not mixed-language tooling.

## Core principles

1. Prefer boring, safe, explicit code over clever abstractions.
2. Use the standard library first; reach for dependencies only when justified.
3. Make destructive or side-effectful actions visible, validated, and auditable.
4. Preserve re-entrant behaviour everywhere — every operation must be safe to re-run.
5. Support dry-run mode for any operation that mutates state.
6. Use native Go where sensible; shell out only when an external tool is the correct system interface.
7. Centralise all external command execution through one package.
8. Design for cross-platform and cross-architecture support from day one.
9. Keep packages small, interfaces focused, and dependencies shallow.
10. Library packages must not call `os.Exit`; only `main` may decide exit codes.

## Build targets

Default to pure Go. Avoid cgo unless there is an explicit, justified reason.

Default cross-compile pattern:

```
CGO_ENABLED=0 GOOS=<os> GOARCH=<arch> go build -o bin/<name>-<os>-<arch> ./cmd/<name>
```

Common targets to keep working:

- linux/amd64
- linux/arm64
- darwin/amd64 and darwin/arm64 (where applicable)

Avoid packages that break static cross-compilation.

## Architecture defaults

Prefer a small set of focused packages over a large flat one. A typical shape:

```
cmd/<binary>/main.go     # entrypoint per binary
internal/<domain>/...    # domain logic, not exported
pkg/<reusable>/...       # only if consumed externally; otherwise prefer internal/
```

Cross-cutting packages most projects benefit from:

- `cli` — flag/command parsing
- `config` — configuration loading and validation
- `log` — structured logging setup
- `executor` — central wrapper for any subprocess or external system call
- `state` — observed system or domain state
- `plan` — desired state and the steps to reach it
- `testutil` — shared test helpers and fakes

Use this as guidance, not rigid law. Adjust to fit the domain.

## State → plan → apply

For any project that mutates external state (filesystems, remote APIs, infrastructure, configuration), design around:

1. Read configuration.
2. Detect current state.
3. Build desired state.
4. Calculate an execution plan (the diff).
5. Validate the plan.
6. Render the plan (dry-run output).
7. Apply the plan step by step.
8. Run post-checks.
9. Log results.

Do not let arbitrary functions immediately mutate the world. Mutations belong inside plan steps that have:

- a description
- a destructive flag
- a check (is this already done?)
- an apply (do the work)
- optionally a rollback

Conceptual model:

```go
type Plan struct {
    Steps []Step
}

type Step struct {
    ID          string
    Description string
    Destructive bool
    Check       func(context.Context) (done bool, err error)
    Apply       func(context.Context) error
    Rollback    func(context.Context) error
}
```

The exact shape may vary. The thinking must not.

## Central executor

If the project shells out to any external command, all execution must go through one central executor package.

No package except the executor may call `os/exec` directly. This is a hard rule.

The executor must capture:

- command and arguments
- environment
- working directory
- stdin if used
- stdout, stderr
- exit code
- duration
- timeout/cancellation state
- dry-run behaviour
- structured error context

The executor must support:

- normal execution
- dry-run mode (logs intent, executes nothing destructive)
- fake/stub execution for tests
- command tracing
- timeout and cancellation through `context.Context`
- structured logging of every invocation
- error wrapping that preserves the command context

Use `context.Context` for any operation that blocks, waits, executes, or may be cancelled.

## Re-entrancy

Every operation must be safe to re-run on a previously-prepared target.

The general pattern: **before doing anything, check whether it is already done.**

Tolerate partial state:

- prior run completed successfully and is being re-invoked
- prior run failed halfway
- some pieces are present, others are missing
- state shape has drifted between runs

Specific patterns:

- Detect existing target state before mutation.
- Treat "already in desired state" as a no-op, not an error.
- Treat "in conflicting state" as a loud, structured error — never silently overwrite.
- Make ownership/intent of mutated state explicit (this tool created this thing).
- Never silently force-destroy unless the destructive intent is explicit, validated, and documented.

## Dry-run

Dry-run is not an afterthought.

A dry-run must:

- log every action that would be taken
- execute no destructive command
- execute read-only probes only when they are safe and necessary to compute the plan
- produce output a human can review before approving

If the project has a notion of "approve and apply", the apply path must execute exactly the plan that the dry-run displayed.

## CLI conventions

The CLI should be predictable, scriptable, and consistent across binaries.

Suggested core flags (use those that apply):

```
--config
--dry-run
--yes
--verbose
--debug
--json
--json-log
--log-file
```

Do not invent CLI surface for hypothetical needs. Add flags as concrete requirements appear.

For multi-binary projects, prefer consistent flag names and behaviours across binaries; a flag with the same name should mean the same thing everywhere.

## Logging

Produce clear logs suitable for debugging failures in production.

Every major step should log:

- what is being checked
- what state was found
- what action is planned
- what command is executed (if applicable)
- stdout/stderr where useful
- exit code
- whether the step was skipped because it was already done
- whether the step was destructive
- what failed
- how to recover or what to inspect next

Prefer structured logging internally (`log/slog` or equivalent). A human-readable surface may sit on top.

Support at least:

```
--verbose
--debug
--json-log
--dry-run
--log-file
```

## Testing

Trust must be earned through tests. The following layers are required for any non-trivial project.

### 1. Unit tests

- Table-driven where possible.
- Cover config parsing, output parsing, plan generation, validation, error wrapping.
- Use `testutil`/fakes; no real subprocesses inside unit tests.

### 2. Golden tests

- Capture real-world inputs/outputs as fixtures under `testdata/`.
- Diff against committed golden files; update via an explicit `-update` flag, never automatically.

### 3. Fake executor tests

- With the central executor's fake mode, assert the exact command sequence a plan would issue.
- These tests prove *what would happen* without doing it.

### 4. Re-entrancy tests

For every major operation:

- clean state
- already-done state
- partial state
- conflicting state
- failure state
- second run after success
- second run after partial failure

### 5. Integration tests

- Where the domain requires real-system validation, support integration tests under a build tag.
- Gate destructive integration tests on an explicit env var **plus** a hostname/identity check.
- Do not fake everything forever; some logic must eventually meet reality.

## Coding style

Use idiomatic Go.

Prefer:

- small packages
- small functions
- explicit types
- clear, wrapped errors: `fmt.Errorf("context: %w", err)`
- table-driven tests
- standard library first
- minimal dependencies
- small behaviour-focused interfaces
- `context.Context` for any cancellable/blocking operation

Avoid:

- global mutable state
- hidden environment assumptions
- stringly-typed state
- silent fallbacks
- huge functions
- panic for normal errors
- `os.Exit` outside `main`
- over-abstracted plugin frameworks built before they have a second user
- premature dependency additions

Return errors instead of exiting from library code. Wrap errors with context as they cross package boundaries.

## Quality gates

All Go code must pass:

```
gofmt -w .
go vet ./...
go test ./...
golangci-lint run
govulncheck ./...
```

If a command cannot be run in the current environment, say so clearly and explain what remains unverified.

Do not suppress linter findings casually.

A `//nolint` comment is only allowed when:

1. the reason is technically valid
2. the reason is written next to the suppression
3. the suppression is as narrow as possible

### Recommended golangci-lint baseline

Enable at least:

- `govet`, `staticcheck`, `errcheck`, `ineffassign`, `unused`, `gosimple`, `typecheck`
- `misspell`, `revive`, `unconvert`, `unparam`, `nakedret`, `bodyclose`
- `gosec` (with documented exceptions only)

Most important:

- `errcheck` — do not silently drop errors from file ops, exec, parsing, network calls.
- `govet`, `staticcheck` — catch real bugs and dead code.
- `gosec` — catch risky file permissions, exec patterns, security mistakes.

## Makefile

Maintain a Makefile with at least:

```make
.PHONY: fmt vet test test-race lint vuln check build build-linux

fmt:
	gofmt -w .

vet:
	go vet ./...

test:
	go test ./...

test-race:
	go test -race ./...

lint:
	golangci-lint run

vuln:
	govulncheck ./...

check: fmt vet test lint vuln

build:
	go build ./cmd/...

build-linux:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o bin/<name>-linux-amd64 ./cmd/<name>
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -o bin/<name>-linux-arm64 ./cmd/<name>
```

Before any meaningful handover, run `make check`. If it cannot complete, report exactly which targets ran, which failed, and what remains unverified.

## Dependencies

Prefer the Go standard library. Before adding any dependency, justify:

- why the standard library is insufficient
- what the dependency does
- whether it affects cross-compilation or requires cgo
- whether it is needed at runtime or only at build time
- whether it increases the attack surface

Avoid packages that pull in cgo or break static cross-arch builds.

## Security and safety

For any project that touches storage, networks, secrets, identities, or other shared systems:

- never hide destructive actions
- never act on a target identified by weak matching alone
- never mutate without explicit validation
- never overwrite unrelated state
- never suppress command failures unless explicitly expected and documented
- never ignore stderr from critical commands
- never continue past an ambiguous destructive target
- never treat dry-run as an afterthought

Identify destructive targets unambiguously (UUIDs, content-addressed IDs, fully-qualified paths). Fail safely on ambiguity.

---

## Bash → Go migration

When a Go project is replacing an existing Bash codebase, treat the Bash version as a **behavioural oracle**, not a translation source.

### Principles

1. The Bash code contains the operational truth — proven edge cases, command ordering, safety checks, recovery paths, re-entrant patterns.
2. The Go code contains the future architecture — idiomatic, testable, structured.
3. Preserve behaviour; do not preserve structure.
4. Do not translate line-by-line.
5. Do not wrap every Bash command in `exec.Command`.
6. Do not throw away hard-won lessons embedded in the Bash control flow, exit-code handling, or order of operations.

### Gotchas (read before you start)

These come up in nearly every Bash → Go port. Knowing them up front saves days.

1. **Bash is dynamically typed; Go decoders are strict.** A typed JSON/YAML decode in Go will fail *before* your validator can produce the expected error. Parse loosely (tolerate scalar where you expected array, etc.), then validate strictly — otherwise the validation-message tests inherited from the Bash suite will never get reached.

2. **`cmd || true` means "non-zero is a valid answer, not an error."** When Bash swallows a non-zero exit, it's expressing intent: "no signature found" or "not mounted yet" is a legitimate observable state. Translate as `return "", nil`, not as an error. This is the #1 source of false-positive failures in a fresh port.

3. **Exit codes are command-specific.** `xfs_repair` 4 means "retry with `-L`"; `fsck.ext4` 1 means "errors found and fixed"; `grep` 1 means "no match." Go's default "non-zero = error" hides this. Build a small `exitCode(result, err) int` helper and write per-command interpreters.

4. **`$(...)` substitutions silently bypass `run_cmd`.** Bash codebases typically gate *mutations* through a logged `run_cmd` wrapper, while read-only probes are invoked via `$(...)` which goes straight to the shell. When everything moves through a central Go executor, your dry-run mode must either allowlist common read-only probes or your tests will fail for reasons that never failed in Bash.

5. **Match every output format your Bash supported.** Because everything was a string under `jq`/`awk`, the Bash code probably accepted both legacy and modern shapes of `lsblk`/`blkid`/etc. silently. Capture fixtures from every supported environment and test the parsers against all of them — do not assume the modern format is enough.

6. **Bash glob semantics ≠ Go `filepath.Match`.** In Bash, `*` matches `/`. In Go's `filepath.Match`, it does not. If glob patterns are user-facing strings inherited from Bash configs, ship your own matcher. Do not silently change pattern semantics on operators.

7. **Bash iteration order is undefined; pick a stable one in Go.** `jq` and associative arrays iterate in whatever order the implementation chose. The first time your Go port emits the "same" error in a different order, tests break. Sort lexically (or by some other stable key) and document the divergence as deliberate.

8. **Bash error messages are not idiomatic Go.** Trailing periods, sentence-case openings, no `%q` quoting. Cleaning these up for Go style (lowercase first letter, no trailing punctuation, quoted identifiers) is normal — but check that no test depends on exact wording first.

9. **Don't blindly preserve Bash bugs.** The behavioural-oracle stance has limits. If a Bash regex like `${var%%[0-9]*}` mangles NVMe device names, do *not* port the bug. Fix it in Go, add a regression test, and record the divergence in your decision log. The oracle is the *intended* behaviour, not the buggy implementation.

10. **Dead-code paths in Bash often stay dead.** A deprecation warning behind an earlier strict check is unreachable in practice. Don't faithfully port unreachable branches just because they exist — note them, skip them, reclaim the simplicity.

11. **Bash mutates global state; Go should make ownership explicit.** Constructs like `CURRENT_STAGE=...` or unconditional teardown of a shared resource often hide latent bugs the Bash tests never caught (because tests only checked outputs, not live state). When porting, make ownership a typed field (`OwnsX bool` on the struct that owns it) — and rebuild loggers from a base instead of accumulating `.With()` calls that stack attributes.

12. **Fire-and-forget needs an explicit split.** Bash uses `cmd || true` for best-effort cleanup. Go cannot idiomatically return-and-discard (`_, _ = ...` everywhere is ugly). Provide two executor entrypoints: one returns the result, one runs best-effort and logs failures. Don't try to make one function serve both callers.

13. **Slim runtimes need explicit `PATH`.** Bash scripts often `export PATH=/usr/sbin:/usr/bin:/sbin:/bin` at the top because their production runtime (initramfs, minimal container, cron, systemd unit) ships a thinner `PATH` than an interactive shell. Replicate this in each Go binary's `main()` when the same constraint applies — your dev machine's `PATH` will hide the problem until production.

### Behavioural map (do this before coding)

For each Bash script/function being migrated, document:

- script/function name
- purpose
- inputs (CLI args, env vars, config files, stdin)
- outputs (stdout, stderr, files written, exit codes consumed by callers)
- environment variables read
- config files read
- files created or modified
- external commands invoked
- destructive actions
- current re-entrant behaviour
- failure handling
- partial-state handling
- assumptions
- suggested Go package location
- suggested test strategy

Do not start implementation until this map exists.

### Per-module workflow

1. Do not delete the Bash version immediately.
2. Document what the Bash version does (the map above).
3. Add tests that capture the observable behaviour.
4. Implement the Go equivalent.
5. Compare outputs / plans / side effects.
6. Run both in parallel on representative inputs.
7. Only then replace or bypass the Bash path.

The Bash code stays authoritative until the Go implementation is proven by tests and real-environment validation.

### Migration order

Do not start with the most destructive module. A safe order for most Bash → Go migrations:

1. Repository analysis and migration map.
2. Central command executor (with dry-run and fake modes).
3. Logging.
4. Config model and validation.
5. Environment / target detection.
6. Output parsers for the external commands the project depends on.
7. Plan model and dry-run renderer.
8. Read-only / observational modules.
9. Mutating modules, lowest-risk first.
10. Highest-risk destructive modules last.
11. End-to-end orchestration binary.
12. Optional compatibility shim for old entrypoints if external callers depend on them.

Do not jump to destructive logic before the executor, logging, dry-run, and plan model exist.

### First task on a new migration

Start with analysis, not coding:

1. Inspect the repository.
2. Identify all Bash entrypoints and major helper scripts.
3. Identify all external commands invoked.
4. Identify destructive operations.
5. Identify re-entrant patterns already in place.
6. Identify config files, environment variables, and CLI arguments.
7. Build the migration map (per the workflow above).
8. Propose a Go architecture.
9. Propose a safe migration order.
10. Do not modify code yet.

Output a clear technical migration plan. Implementation begins only after that plan is reviewed.

---

## What not to do

- Rewrite everything in one commit.
- Translate Bash line-by-line.
- Use Python.
- Introduce cgo casually.
- Call subprocesses from random packages.
- Hide destructive actions.
- Skip dry-run support.
- Assume a clean target.
- Ignore re-entrant behaviour.
- Discard tested Bash lessons.
- Invent a new flow without mapping the current one.
- Replace proven behaviour without tests.
- Over-engineer a plugin framework before the core is stable.

## Final principles

The Go project should be:

- re-entrant
- testable
- safe
- boring
- portable
- clear
- binary-first
- platform-aware
- architecture-aware
- friendly to future refactoring (by humans or AI)

For any tool that touches shared state, boring is good. Exciting tools become post-mortems.
