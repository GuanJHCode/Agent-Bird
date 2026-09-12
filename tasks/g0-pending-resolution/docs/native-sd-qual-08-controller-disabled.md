# Historical controller disabled

`native_sd_control.run()` is retained for offline source and evidence review only.
The old sd-qual-08 controller hard-codes the historical case, job, socket,
service, and TUI path and does not provide a complete auth/profile-isolated
process boundary. Its process-starting entry therefore fails immediately with
`historical_controller_not_auth_isolated`; it must not be used to rerun the old
case or launch native/model work.

Offline helpers such as binding projection, evidence parsing, and identity
validation remain available to audit preserved records.
