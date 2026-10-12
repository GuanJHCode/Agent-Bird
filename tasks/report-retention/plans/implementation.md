# Managed report retention (user-authorized policy)

User chose explicit owner resolution followed by TTL collection with recovery indexes retained. First batch scope: only immutable managed report attachments (`class=report`, result/failure). Never delete Provider source, Host terminal/progress slots, report/runtime event rows, Git objects or worktrees in this batch. Subsequent worktree/ref pin work remains separate.

Success requires exact handled ACK + latest completed/result-ready terminal attempt, no pending/question/unknown, 30 days after the later confirmed terminal/consumption time. Failure requires exact owner resolution (`resolved`/`abandoned`), all original process segments proved terminal and delivery consumed, then 7 days after resolution/terminal/consumption. Unresolved/unknown resources retain pins; user code is not TTL data.

New schema39: immutable resolution and deletion records, persistent resource pins, owner-bound opt-in run GC policy. Preserve original intake/proof/events as recovery indexes. Physical object data.bin alone can be retired; permanent intent/lock/deletion marker remain charged. Fresh durable delete intent precedes Remove, finalize only after original-object absence verification, exact saved-plan recovery/idempotence. No historical unknown becomes delete authority.

Pin creation must reject deleting/released objects; default-off ACK has no extra writes. Store checks active pins and all eligibility again at apply. New report producers cannot resurrect retired physical data; exact old event replay returns original durable metadata ACK, different payload rejects.

Ownership update: root owns this WT IPC/CLI/coordinator and final integration. Artifact-delete worker owns isolated WT internal/artifacts and internal/store (schema39, retention, replay, GC), with frozen OpenDeletion + Plan/MarkerBytes/RemovedBytes/Remove/VerifyRemoved/Close API. Root integrates stable commits before final verification. No recursive agents or shared writer.

Design review requirements: exact consumption must join runtime_decisions with handled plus event/action slot/revision/hash, because acked alone also represents rejected/stale/waiting_user. Deleted exact report replay uses preserved canonical metadata and never restores bytes. Prepared deletions fence every task attempt/segment/intake creation path, including retry/resume, and reject new pins. Resolution is per intake and includes an immutable reason and current owner/rebind binding. Marker metadata is prepaid using planned bytes before Remove; a fresh lease's MarkerBytes is zero. Public plans are bounded to 8191 bytes and carry durable references rather than full private proofs.

Validation: bounded focused Store/FS race and one public CLI/coordinator path, no full suites or real models. Required rejection/cutoff/owner drift/replay/quota/crash evidence retained. Default off; do not enable/delete existing user runtime data.
