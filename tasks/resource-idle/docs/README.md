# F12 idle30 fixture
The runner is limited to the fixed integration candidate path and requires a full
freeze manifest covering candidate, runner, controller, reused soak module and
threshold files. Each source is copied from one no-follow FD into a new 0700
output directory; copied executables are 0500. No candidate is started during
preparation.

It submits through public owner-bind/submit with one gated fake worker, enrolls
exact PID/birth/argv identities, requires durable Exited0 and reaping, then uses
public collect/accept. It records the equivalent of CompletelyIdle continuously.
Coordinator remains expected during the debounce: exit before 29 seconds after
the first idle observation, after 32 seconds, a nonzero original Wait, residual
identity, pin drift, or any cleanup signal is a failure. State and raw receipts
remain on failure. This is neither offline pending-delivery nor four-Host proof.
