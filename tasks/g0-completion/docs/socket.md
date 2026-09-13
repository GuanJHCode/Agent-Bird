# Public socket ownership cleanup

The transaction freezes the launchd public listener only after successful
bootstrap and exact job identity verification. The frozen record contains the
socket `dev`, `ino`, `uid`, and mode, while the socket parent is already frozen
by the transaction's directory identity contract.

After `bootout`, cleanup is permitted only when the job is absent and the
socket parent still has the frozen identity. The parent FD is checked with
`fstat` as well as the current path and is opened with `O_NOFOLLOW`.
Because POSIX has no portable unlink-if-(dev,ino), the controller creates a
random mode-0700 quarantine directory under that frozen parent and atomically
renames the current socket entry into it. It then inspects the moved entry. A
matching frozen socket is unlinked from quarantine and the quarantine directory
is fsynced and removed.

If the moved entry is a replacement, the controller attempts to restore it to
the public name with a no-replace hard link. A concurrent new public entry makes
that restore fail with `EEXIST`; both the new public entry and the quarantined
replacement are preserved and the receipt is `UNKNOWN` with the quarantine path.
The public name is therefore briefly absent only after the launchd job is
already absent; normal replacement races restore the replacement, while an
ambiguous race never overwrites or deletes the newer object.

Any symlink, regular-file replacement, inode change, parent replacement,
missing freeze record, failed no-replace restore, or unknown job state leaves
the foreign object preserved and returns `UNKNOWN`. The transaction may remove
its own plist/lease when their identity checks remain valid, but it never
deletes a socket whose ownership cannot be proven. Private backend cleanup has
no access to this public socket path.

The tests use real local AF_UNIX sockets plus a fake launchctl. They cover the
same-identity residual, socket symlink/replacement, parent replacement, and a
replacement inserted between the identity check and quarantine rename. No real
launchctl registration or default user socket is used.
If the post-bootstrap socket freeze is missing or has the wrong owner/mode, the
transaction enters `UNKNOWN` and `managed()` rolls back the owned job and
plist/lease. It never converts that failure to `ROLLED_BACK` or deletes an
unfrozen socket.
