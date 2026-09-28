# Repository Guidelines

## Project Structure & Module Organization

- `tools/orchestrator/`: Go control plane, durable scheduler, and CLI. Entry points live in `cmd/orchestrator/`; implementation packages and colocated tests live in `internal/`.
- `tools/g0-probe/` and `tools/g0-return-lab/`: independent Go modules for metadata probes and lifecycle experiments.
- `plugins/codex-orchestrator/`: plugin manifest, shell entry points, and Python libraries under `lib/`.
- `tools/native-product-bridge/` and `tools/real-user-trial/`: Python bridge and authentication tests.
- `tasks/<task-name>/`: scoped scripts, tests, plans, data, and evidence. Some scripts are packaged runtime dependencies; inspect packaging references before moving them.
- `docs/` and `plans/`: architecture, runtime protocols, validation plans, and implementation records.

## Build, Test, and Development Commands

Use Go 1.26 or newer, Python 3, and `pytest`. Native orchestration targets macOS. Run Go commands inside the relevant module, such as `tools/orchestrator/`:

```sh
go build ./...                       # Compile the module
go test -count=1 ./...                # Run uncached tests
go test -race -count=1 ./...          # Check concurrent behavior
go vet ./...                         # Run Go static analysis
```

From the repository root, run focused Python suites:

```sh
python3 -m pytest tools/native-product-bridge -q
python3 -m pytest tools/real-user-trial -q
```

Consult each tool's README for local invocation and required request files. Packaging uses `tasks/g1-g4-delivery/scripts/package-plugin.sh` with explicit binary, interpreter, version, and output arguments.

## Coding Style & Naming Conventions

Format Go changes with `gofmt`; use tabs and standard Go naming. Follow existing Python style: four-space indentation, `snake_case` functions, and `PascalCase` classes. Keep shell scripts compatible with their declared interpreter. No shared Python formatter configuration is present. Prefer small changes within existing modules.

## Testing Guidelines

Use Go's `testing` package with `*_test.go` files and Python `pytest` with `test_*.py` files. Add regression tests for observable behavior changes. No repository-wide numerical coverage threshold is configured. Follow task-specific acceptance plans; synthetic tests do not establish native TUI acceptance. Report skipped native checks explicitly.

## Commit & Pull Request Guidelines

Recent commits predominantly use `feat:`, `fix:`, `test:`, and `docs:` followed by concise imperative descriptions. Keep commits scoped. PR descriptions should explain the behavior change, reference relevant issues or task plans, and record validation commands, results, and remaining limitations.

## Task Artifacts & Configuration

Create artifacts under `tasks/<task-name>/{scripts,plans,docs,data,tmp}/`. Preserve existing modifications and evidence; remove only temporary files owned by your task. Follow `docs/runtime-protocols.md` and `docs/git-cleanup-protocol.md` for runtime and cleanup changes. Preserve owner-only runtime directories (`0700`) and capability/request files (`0600`).
