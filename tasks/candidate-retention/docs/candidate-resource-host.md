# Host candidate-resource registration

Only ordinary auto-directory producer workspaces and ordinary v2 compose
actions opt in. After the original provider/worker has exited successfully and
both `Result` and `Exited` are durable, Host seals the exact deterministic
source journal and emits the bounded internal registration event. Registration
is best-effort: a failure retains the completed business result and sealed
evidence for an exact command replay.

The registration event contains only canonical header JSON and increments the
returned event count when it is durably appended.
