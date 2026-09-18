# Native product bridge

The bridge joins G1 pending events to the already attached G0 owner transport.
It never discovers a native endpoint, guesses a thread or PID, or starts a
`turn/start` without the G0 owner-ready/helper-ready receipt chain.

The installed wrapper starts the helper through the Go launcher:

```text
invoke.sh native-bridge helper --request /absolute/0600-launch.json
```

The launch request pins the absolute Python interpreter, its SHA-256, the
installed plugin version root, and that version's package manifest. The Python
helper then accepts a G0 attachment config over its bounded stdin protocol. It
first emits `attached` with the immutable owner capability used to project the
initial G1 submit owner. Only after that may the controller provide the G1
task/control binding and request delivery.

Recovery uses the newly attached owner capability:

```text
invoke.sh rebind-owner --request /absolute/0600-rebind.json
```

`VerifyRebind` checks the capability, current owner PID/birth, thread,
origin-context, generation and proof. The coordinator reads the separate
control file to add the run ID/token and updates the owner binding before an
explicit `resume`.

The plugin package is not sufficient without the matching installed G0
runtime. The distribution must contain an owner-only `runtime/g0` directory,
`runtime-manifest.json`, the eleven reviewed G0 Python files, and one absolute
interpreter path/hash. The activation service must start from that directory
and pin those same files in its per-activation manifest. The bridge rejects a
module outside the installed runtime root or a module whose path/hash differs
from the active lease's service manifest. There is no development-repository
fallback.

The helper ledger is independent of G1 semantics. It stores immutable event
aliases and source mappings, one unresolved transport slot per thread, exact
history proof IDs, and the final G1 ACK request/receipt. A `sending` crash is
reconciled through history with sending disabled. An identical G1 ACK can be
replayed after a crash between the G1 commit and local `controller_acked`
transition.

For unattended callbacks, `native-bridge start --request` launches a detached,
owner-bound five-minute driver. It creates the helper grant, submits or rebinds
through the verified owner capability, and waits on G1 `wait-events`; only
actionable events enter the native transport. Exact history is written as an
owner-only driver receipt. `native-bridge decide --request` records the bound
owner decision, after which the driver submits the typed G1 ACK. The driver
stops when its original owner/service/backend/socket attachment is no longer
live and never resumes an interrupted task by itself.

Machine-bound private acceptance plans are not distributed in this repository.
Native acceptance requires a separate explicit plan and verified local inputs.
