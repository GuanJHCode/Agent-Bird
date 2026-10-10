# Private filter-process codec

Implement only pkt-line v2 clean/smudge framing in `internal/gitfilter`.
The codec consumes full request content before calling its synchronous exchange,
emits no partial response, and terminates on every protocol or exchange error.
It does not own a Host/session latch; callers must retain failure across Git
helper restarts.

Limits: 65,520-byte pkt total, 16 headers/8,192 header bytes, 4,096 bytes per
metadata line, 16 MiB and 65,536 packets per body, 1,024 requests. Unknown
headers and delay are rejected; optional ref/treeish/blob are validated only.
