# Private filter.process v2 codec

`RunFilterProcess(ctx, input, output, ProcessExchange)` implements one private
helper lifetime; it does not activate a Host, IPC capability, or public merge.
The synchronous exchange callback matches the private exact Broker session.
Content streams once into its writer; there is no second 16 MiB input spool.

- A finite context deadline is required. Every read/write checks cancellation;
  the future carrier must close blocking transports on cancellation. Arbitrary
  `io.Reader`/`io.Writer` cannot be forcibly interrupted by this codec.
- Client greeting must come first and offer version 2. The version response is
  actually written before reading capabilities. Only offered clean/smudge are
  selected; at least one is required. Delay is never selected. Duplicate and
  malformed version/capability lines fail closed.
- Packet lengths use four lowercase hex digits, maximum total 65520 bytes.
  `0000` is flush; `0004` is an empty data packet, not a flush. Reserved lengths,
  truncated frames, invalid hex and oversize packets terminate the codec.
- Each metadata list has at most 16 lines / 8192 raw bytes and each line at most
  4096 raw bytes. One terminal LF is optional. Invalid UTF-8, NUL, CR or internal
  LF are rejected. Requests require exactly one negotiated command and pathname
  (1..512 UTF-8 bytes). Optional `ref` is noncontrol UTF-8, 1..1024 bytes;
  `treeish`/`blob` are 40 or 64 lowercase hex. Values may contain `=`. All other
  request headers, duplicate keys and delayed operations are rejected.
- Each body permits at most 16 MiB and 65536 packets, including empty packets;
  the helper permits at most 1024 requests. The producer is synchronous, at most
  once, revoked after callback return, and detects short writes. Ignored producer
  errors still reject the request. No request response precedes the content flush.
- Callback rejection/ignored producer/output over 16 MiB drains the current body
  using the original byte/packet budgets, emits `status=abort` only after its flush,
  and terminates without reading the next request. Framing/limit/cancellation
  failure that prevents a complete drain emits no response. A successful result
  is validated before status, then packetized and followed by content flush plus
  empty final status list. Transport write errors terminate immediately.
- EOF is normal only between complete requests. The carrier must permanently
  retain codec/helper failure across Git restarts. Routing data and this codec
  never grant transform authority; the original Broker/session permits do.

Protocol references: [gitattributes](https://git-scm.com/docs/gitattributes),
[long-running-process-protocol](https://git-scm.com/docs/long-running-process-protocol),
[Git convert.c v2.50.1](https://raw.githubusercontent.com/git/git/v2.50.1/convert.c).
This is a deliberately restricted protocol, not general Git filter support.
