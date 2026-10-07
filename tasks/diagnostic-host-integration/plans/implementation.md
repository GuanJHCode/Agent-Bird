# M2c actual execution integration

Base041d955. ROOT owns this worktree, implementation and Mac/Docker execution.
Two agents read only: lifecycle/event contract review and existing actor map.

Attach the already verified private driver after all provider preparation and
before Start, retaining the original observer/duplex consumer. Detailed scope
must match the launch Run/Task/Attempt/Host identity. Default summary has no raw
namespace. Ordinary failure wakes its local segment cancellation; managed Codex
failure wakes its lease cancellation. Neither callback signals or waits.

Unproved early return aborts the driver and joins bounded cleanup. Successful
original Wait/tree/drain permits Finish. Resolve summary metadata errors before
choosing Finish, and preserve those errors across the stop path. Diagnostic
storage failure is observational: the existing original Exited event carries
only an optional version1 {summary,detailed} failure receipt. It creates no
business action or retry authority and retains original identity, exit and
active-time settlement. Async detailed failure uses the actual diagnostics_unavailable
StopReason only when it canceled execution; late summary errors preserve output. An unproved
process must remain UNKNOWN. No raw paths/error messages enter failure artifacts.

TDD actual ordinary ExecuteLaunch opt-in/default and summary failure; then normal,
Start failure, canceled execution and controlled filesystem failure. Exercise
managed Codex through the original synthetic native actor and existing sandbox,
reference, turn, lease and original Wait checks. No real model/account calls.
Verify affected lifecycle regressions and independent review on a stable snapshot.
Public read/ACK and M3 are still subsequent required work, not waived by this batch.


## Native evidence correction and outstanding duplex dependency

Initial ordinary RED33240 exposed missing raw namespace and ignored summary
write error. Its first summary assertion also expected a business Failed outcome;
that expectation was rejected during contract review. Correct behavior preserves
Result/Question/Failed/Stopped and annotates only the original Exited. Store
checks detailed authority in the same new-event transaction after exact durable
replay lookup. Summary takes the earliest original Exited of the latest segment,
not later history that would erase the receipt.

The ordinary successful Stop includes original Handle.Wait and copy completion
(process.confirmStopped); the after_stop snapshot must truthfully expose that
proof. The earlier false WaitComplete expectation predates that strengthened
Stop contract. Terminal parsing/tail/privacy and interrupted outcome assertions
remain required.

Codex duplex is not wired in this batch: codexsession.Run defers Client.Close,
and codexrpc fail closes both stdin and stdout before necessarily observing EOF.
Host Wait proves child/stderr completion, not that stdout was read fully. Next
batch must retain the same RPC reader through bounded shutdown/EOF (or persist
an explicitly incomplete prefix); no second reader and no duplicate observer.
The existing process observer already sees original Read bytes, so adding another
transport tap would double-count them. Do not call Writer.Close based only on
RPC finish/Close or child Wait. All F01–F13 remain active.
