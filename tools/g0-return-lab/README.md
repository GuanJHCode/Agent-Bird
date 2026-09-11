# G0 synthetic return host

`g0-return-lab` is a bounded **macOS-only synthetic executor** for G0 return-channel experiments. It starts one detached process, writes one fixed result, and exposes read-only observation. It is not a scheduler, a database, a third-party CLI adapter, or evidence that the native Codex return channel or G0 works.

The optional owner/revision/ACK experiment is documented in [ACK.md](ACK.md). Build that variant as `data/g0-return-lab-ack`; keep a running baseline binary unchanged. Without `--controller-thread`, the original start metadata and four-field read/wait output remain unchanged.

The separate foreground `retained-start`, `retained-inspect`, and `retained-resume` lifecycle is documented in [RETAINED.md](RETAINED.md). It stops when its direct caller exits, preserves fixed step artifacts, and continues only after an explicit matching resume. The detached command behavior described below is unchanged.

Build with Go 1.26 or newer; the module uses only the standard library:

```sh
go build -o data/g0-return-lab .
```

The experiment coordinator must first create a unique absolute task directory owned by the current effective user with mode `0700`. `start` requires it to be empty. The tool does not select a directory, create its parent, read credentials, persist the environment, invoke a shell, or modify Codex configuration.

```sh
g0-return-lab start --dir /absolute/precreated/task-dir --nonce sample_01 --delay 5s
g0-return-lab wait --dir /absolute/precreated/task-dir --nonce sample_01 --timeout 30s
g0-return-lab read --dir /absolute/precreated/task-dir --nonce sample_01
```

`nonce` accepts 1–64 ASCII letters, digits, underscores, and hyphens. `delay` accepts `100ms`–`60s`; `timeout` accepts `100ms`–`120s`, inclusive. Each required flag must be supplied. No command takes task text or executes user-provided commands. `_worker` is an internal subprocess entry point, not a caller-facing command.

Successful `start` writes exactly one JSON line to stdout:

```json
{"version":1,"status":"started","nonce":"sample_01","worker_pid":123,"created_at":"2026-09-11T00:00:00Z","job_file":"job.json","result_file":"result.json"}
```

The worker calls `setsid`, receives `/dev/null` standard streams, and inherits the caller's normal environment and execution restrictions. `start` and `wait` do not own its continuing lifetime. This detaches it from the calling terminal; it does **not** bypass permissions or promise survival of an environment that explicitly terminates an entire process tree. The native runner must test the actual calling environment separately.

Before `start` returns success, it receives an internal pipe handshake and reads the atomically published, synced `job.json`. The on-disk nonce, PID, and `created_at` must match the child's handshake and actual spawned PID. `created_at` is a metadata creation timestamp, not an OS process birth identifier. A successful start proves this one launch and ownership record were established; it does not prove the process is still alive when a later reader observes the file.

After the configured delay, the worker checks its metadata, atomically publishes the fixed completed result, and exits. `read` is immediate. `wait` checks the files at most every 100 ms, with a bounded timeout. Successful output is a single JSON line:

```json
{"version":1,"status":"completed","nonce":"sample_01","count":1}
```

Before completion, `read` returns `{"version":1,"status":"pending","nonce":"sample_01","count":0}` with exit code `0`. `count=1` is a fixed synthetic payload; it is not a production execution count or a delivery acknowledgement. Re-reading never rewrites the result. A missing result can also mean a failed or interrupted worker; this minimal lab does not infer liveness or recover failed work.

| Exit | Meaning |
| --- | --- |
| `0` | A valid start, a valid completed result, or an immediate pending read |
| `2` | Invalid arguments, untrusted/missing metadata, nonce mismatch, duplicate launch, launch failure, or other I/O failure |
| `3` | `wait` expired without a result; the worker is not cancelled |

Errors go only to stderr as a fixed schema, for example `{"version":1,"status":"error","error":"timeout"}`. Error output never includes arbitrary file contents or OS error details. `start_failed` means no success was established. `start_uncertain` means a child was spawned but the bounded five-second readiness check could not establish success; that child may still complete within the configured delay. Do not retry `start` on that directory. Observe the existing record with the same nonce, or preserve the directory for inspection.

`claim.json`, `job.json`, and `result.json` use mode `0600`. A first-start exclusive claim prevents duplicate launches. Complete files are staged, synced, and published using an exclusive hard link, which cannot replace any existing destination. Reads reject symlinks, non-regular files, other owners, other permission modes, oversized/invalid JSON, unknown fields, and mismatched IDs. The directory is opened through `os.Root` and checked against its original identity. This assumes an isolated private task directory: it does not authenticate artifacts against another process running as the same user, and does not make crash-recovery or power-loss durability claims.

Run the meaningful real-process tests with:

```sh
go test -count=1 -timeout=60s ./...
G0_LAB_TEST_RACE=1 go test -race -count=1 -timeout=90s ./...
go vet ./...
go build -o data/g0-return-lab .
```

The race command also builds the CLI subprocesses with `-race`. Tests cover caller exit and independent process group, persisted metadata before return, bounded worker exit, pending/completed output, timeout and killed-waiter survival, duplicate starts, mismatched IDs, bounded inputs, private directories, rejected unsafe results, and refusal to overwrite existing artifacts. Test files and subprocess binaries are removed by the test harness. Experiment coordinators retain their own task data and remove it only after confirming the bounded workers have finished.
