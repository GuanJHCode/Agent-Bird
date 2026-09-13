# Controller death lifecycle guard

Base: `fba2cc7fb01ea47843d1585ea3403dcce45a7428`.

The first real OS regression starts the existing ActivationService in a separate process, opens an admitted frontend to its sandboxed Python dummy backend, records the backend's actual PID/birth, and sends SIGKILL to the service. The backend remains alive for the original three-second test observation window; the service cannot execute its `finally` cleanup. The test then stops only its known dummy resources.

RED command:

```sh
PYTHONDONTWRITEBYTECODE=1 python3 -B -m pytest -q -p no:cacheprovider tasks/g0-completion/tests/test_lifecycle_guard.py --tb=short
```

Result: **1 failed in 3.33s**, `lifecycle-red.log`: `service SIGKILL left its registered backend alive`.

The implementation covers the current ActivationService backend launch. `owned_child_guard.py` is the actual child's parent and sole reaper; the controller holds a private anonymous socketpair endpoint. The backend does not inherit that endpoint. Controller EOF, including SIGKILL, requests TERM, waits one second, then requests KILL and waits at most eight more seconds for that exact child. Signals operate on the guardian's unreaped `Popen` child, never a PID supplied in a control message. This prevents PID reuse from selecting an unrelated process. The service still exposes the actual backend PID/birth/executable and validates the native peer; `guardian_pid` is separate.

The guardian writes owner-only `guardian-start.json` and `guardian-final.json` receipts in the lease directory. Both processes hold the same opened directory FD; creation is O_EXCL, and reading requires a regular, owner-only, single-link file with stable stat and exact bounded length. Directory replacement retains the record in the original directory and returns UNKNOWN, without writing into the replacement. Missing birth refuses service readiness and produces UNKNOWN. Guardian loss before or after its launch ACK cannot prove child cleanup. Partial control-frame EOF stops the owned child. The launch plan remains the reviewed clean sandbox plan; the guardian receives it only through the private channel and writes no argv, environment, credentials or protocol bodies. The fixture builder now pins the guardian source alongside the existing interpreter and source pins.

The final combined command was:

```sh
PYTHONDONTWRITEBYTECODE=1 python3 -B -m pytest -q -p no:cacheprovider \
  tasks/g0-completion/tests/test_lifecycle_guard.py \
  tasks/g0-proxy-continuation/cold-start/tests \
  tasks/g0-auth-preserving-activation/tests/test_native_activation_fixture.py \
  --tb=short
```

Result: **67 passed in 16.71s**, `lifecycle-green.log`. The lifecycle file has 11 real-process tests, including service SIGKILL, ignored TERM escalation, guardian destruction before/after ACK, escaped grandchild survival, wrong-PID control input, missing birth, parent replacement, hardlinked receipt, partial frame EOF, and actual natural child exit between poll and terminate. Review regressions first produced **3 failed, 2 passed, 5 deselected** (`lifecycle-review-red.log`) for missing birth, directory replacement and hardlinked proof; the final command includes their fixes. Python's actual `Popen.terminate` suppresses the child-exit ProcessLookupError race; the regression exercises that real operation and retains exit status 0.

Scope remains direct backend execution cleanup. Every receipt states `tree_stop_unproven=true` and `restart_allowed=false`; an escaped grandchild demonstrably survives in the negative test and is cleaned by the test's own known handle. Destroying the guardian may leave the backend alive, and the result is UNKNOWN. No arbitrary process-tree guarantee, SIGKILL-proof guardian, automatic retry or historical NativeRuntime protection is claimed. The guardian never unlinks public or private sockets. Normal live-service cleanup still handles its private endpoint; after service SIGKILL, residual private endpoint cleanup is not proved by a reaped-child receipt. The service-SIGKILL test verifies public socket inode and an existing task marker remain unchanged. No real native, model, launchctl, real auth or global configuration was used. Tests release their own temporary directories and explicitly clean surviving dummy negative-test processes.
