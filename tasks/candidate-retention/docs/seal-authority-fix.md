# Seal authority fix

Candidate source reopening now obtains held no-follow directory descriptors for
the spool, namespace and exact journal root. Events, lock and seal files are
opened relative to the held root; seal publication syncs that same directory.
The original producer journal is cross-checked against the held `events.jsonl`
inode before sealing.

Compose cannot establish an original worker-journal authority after the worker
has closed without a persisted worker journal directory/events/lock identity.
It must remain unregistered until that narrow worker/Store identity handoff is
available; this change does not mint authority from a later pathname reopen.
