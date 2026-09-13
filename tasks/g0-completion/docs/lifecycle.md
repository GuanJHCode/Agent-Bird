# Controller death lifecycle guard

Base: `fba2cc7fb01ea47843d1585ea3403dcce45a7428`.

The first real OS regression starts the existing ActivationService in a separate process, opens an admitted frontend to its sandboxed Python dummy backend, records the backend's actual PID/birth, and sends SIGKILL to the service. The backend remains alive for the original three-second test observation window; the service cannot execute its `finally` cleanup. The test then stops only its known dummy resources.

RED command:

```sh
PYTHONDONTWRITEBYTECODE=1 python3 -B -m pytest -q -p no:cacheprovider tasks/g0-completion/tests/test_lifecycle_guard.py --tb=short
```

Result: **1 failed in 3.33s**, `lifecycle-red.log`: `service SIGKILL left its registered backend alive`.

Implementation will first cover the current ActivationService backend launch. A small per-child guardian will be the actual child's parent and sole reaper, while the controller holds a private control FD. Controller EOF triggers TERM followed by bounded escalation for that exact child. Owning the unreaped child prevents PID recycling from turning a stop into a signal to an unrelated process. The service will continue exposing the actual backend PID/birth/executable, with guardian identity separate. Safe receipts retain task evidence and will never authorize automatic restart.

This is not a general process tree manager. Untracked or escaped grandchildren and destruction of the guardian itself must be reported as unproven/UNKNOWN; tests will include these negative boundaries. Public launchd sockets are never removed by the guardian. Historical NativeRuntime reuse follows this first stage after the service path is independently reviewable. No real native, model, launchctl, real auth or global configuration was used by the regression.
