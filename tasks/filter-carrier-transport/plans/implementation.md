# B2 authenticated carrier transport

Status: implemented; targeted native/race evidence in ../docs/validation.md; pending independent review.

Scope: private Darwin B1 + original Gate adapter and current-binary helper;
no Store/schema/Host changes, public merge remains denied. Linux build-only.

Frozen contract: newCarrierTransport(finiteCtx, originalB1, originalGate),
activate(finiteCtx, originalHandle, frozenDigest), serve(), drain(finiteCtx),
removeAfterOriginalGit(finiteCtx, sameHandle). Activation caller must first
prove durable spawned/postguard for that original Start. Removal independently
requires drain, original Handle Wait and tree; helper EOF never releases Broker.
Constructor errors after taking a nonnil B1 owner release descriptors and retain
Unknown nodes. Copies, duplicate activation and second accepted socket reject.

One sequential Accept lifetime plus one original Gate Serve lifetime. Kernel
PeerPID from the accepted UnixConn enters Gate unchanged. Held/named B1 identity
checks bracket transport I/O. Cancellation closes listener/endpoint and bounded
drain waits for actual completions. Routing is only comparison data.

Helper routing: bounded JSON/base64 domain, HostPeerExpectation, socket named
parent/directory/socket IDs, caller-supplied current executable path/SHA256,
and absolute finite deadline. Capture gives no grant. Named socket and Host
kernel full birth/euid checked before any stdin protocol byte is read/forwarded.
Helper uses two fixed 32KiB buffers, cancellable pipe FD IO, original Gate codec;
no second codec or caller control token. Future Host admission must supply its
original owner-authenticated executable pin and complete signed digest/grant.

Validation: RED missing API; targeted Darwin native/race and current binary
build; short real Git filter-process clean/smudge against original live Broker,
original Start/Wait/tree and foreign Host rejection. No old long Host fixtures.
