# ACP Turn completion receipt

`Client.Prompt` returns a receipt only after a valid `session/prompt` response
has been parsed and no permission request was denied. The receipt contains the
bound session ID, positive JSON-RPC request ID, stop reason, lowercase SHA-256
of the prompt's direct UTF-8 bytes, and lowercase SHA-256 of the raw response
frame body. The latter excludes only the terminal NDJSON LF; JSON whitespace is
therefore part of the digest.

EOF, a truncated frame, JSON-RPC error, protocol error, permission denial, and
cancellation that win before completion return the zero `Turn` with the error.

Response acceptance and cancellation compete for the same pending request. A
response that wins cannot be invalidated by a late context callback or poison a
following RPC. Permission denial is frozen at response acceptance. A later
permission request is unsolicited and closes the strict protocol. A completed
request retains its receipt even if its caller has not yet been scheduled to
return it. Future calls observe the terminal client error.

The original Prompt owns turnActive until it returns; pending closes only the
permission window. All request outcomes, cancellation and terminal state are
published under the same mutex. Each request channel receives exactly one
outcome before its pending slot is released. The client done channel cannot
override that result. Stream close runs after publication, outside the mutex.
