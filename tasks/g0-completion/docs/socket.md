# Public socket ownership cleanup

The transaction freezes the launchd public listener only after successful
bootstrap and exact job identity verification. The frozen record contains the
socket `dev`, `ino`, `uid`, and mode, while the socket parent is already frozen
by the transaction's directory identity contract.

After `bootout`, cleanup is permitted only when the job is absent, the socket
parent still has the frozen identity, and the socket entry opened relative to
that parent directory is still the same real Unix socket. The final `stat` and
`unlink` use the parent directory FD, followed by `fsync` on that directory.
The opened parent FD itself is checked with `fstat` as well as the current path,
and is opened with `O_NOFOLLOW`; a path-only check is insufficient.

Any symlink, regular-file replacement, inode change, parent replacement,
missing freeze record, or unknown job state leaves the object untouched and
returns `UNKNOWN`. The transaction may remove its own plist/lease when their
identity checks remain valid, but it never removes a socket whose ownership
cannot be proven. Private backend cleanup has no access to this public socket
path.

The tests use real local AF_UNIX sockets plus a fake launchctl. They cover the
same-identity residual, socket symlink/replacement, and parent replacement
cases. No real launchctl registration or default user socket is used.
If the post-bootstrap socket freeze is missing or has the wrong owner/mode, the
transaction enters `UNKNOWN` and `managed()` rolls back the owned job and
plist/lease. It never converts that failure to `ROLLED_BACK` or deletes an
unfrozen socket.
