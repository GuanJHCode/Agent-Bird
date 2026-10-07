# Output observer tap

Scope: add a process-private output observer for Host detailed diagnostics.

1. Add the frozen stream labels and observer field without changing legacy capture.
2. Observe full normal stdout/stderr bytes after Handle locking; observer failures cannot affect I/O.
3. Observe Duplex stdout only from the caller's original Read path.
4. Validate with in-memory writers and pipes; native child coverage remains for integration owner.
