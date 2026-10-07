# ACP Turn completion receipt

`Client.Prompt` returns a receipt only after a valid `session/prompt` response
has been parsed and no permission request was denied. The receipt contains the
bound session ID, positive JSON-RPC request ID, stop reason, lowercase SHA-256
of the prompt's direct UTF-8 bytes, and lowercase SHA-256 of the raw response
frame body. The latter excludes only the terminal NDJSON LF; JSON whitespace is
therefore part of the digest.

EOF, a truncated frame, JSON-RPC error, protocol error, permission denial, and
cancellation return the zero `Turn` together with their existing error. They do
not produce a completion receipt.

Response acceptance and cancellation compete for the same pending request. A
response that wins cannot be invalidated by a late context callback or poison a
following RPC. Permission denial is frozen at response acceptance. A later
permission request is unsolicited, closes the strict protocol, and does not
revoke the receipt that was already returned.
