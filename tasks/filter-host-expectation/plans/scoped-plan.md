# Host peer expectation plan

Add only `HostPeerExpectation` and capture/verify helpers in the process
package. The value is an informational helper expectation, never a Handle or
LiveDescendant capability. Capture observes only the current host PID through
the existing private live double-read platform observer. Verify accepts a
kernel peer PID supplied by its caller, validates the frozen domain and raw
format, then obtains two private observations and requires both complete
records to match the expected PID, raw birth and effective UID.

The helper does not inspect sockets, use `ps`, public `Identity`, or a
serialized identity. Darwin/Linux reuse `observePeerRecord`; unsupported
platforms fail closed through that function. Tests use a private observation
seam for record drift and input rejection plus one short native self case.
