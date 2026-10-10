# Private filter-process codec

`RunFilterProcess` implements only one v2 helper lifetime and clean/smudge.
It waits for the request content flush before invoking a synchronous exchange,
never publishes partial callback output, and emits the success three-part
response only after a successful full producer drain. Every malformed packet,
callback error, ignored producer, short write, or cancellation drains to the
request flush when possible, writes `status=abort`, and terminates.

The codec does not own the Host sticky-failure latch. A caller must keep any
terminal error across Git helper restart; a new helper cannot restore authority.
Unknown metadata and delay are rejected. Optional ref/treeish/blob are checked
only as bounded observations, not authorization.
