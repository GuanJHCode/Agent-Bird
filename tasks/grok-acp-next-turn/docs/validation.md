# Grok ACP transport foundation

Integrated through `80097cc`. The bounded ACP v1 client owns one reader and serializes writes; it binds one initialized session, accepts consecutive prompts, rejects foreign session updates, denies permission requests explicitly, and closes an uncertain transport before another turn can write. The session is bound by the reader before the next notification is consumed. Unknown reverse methods receive method-not-found, including requests without params.

ROOT reran the complete package under race with the native actor enabled: 17 top-level / 21 named PASS, zero failures/skips. The controlled actor received two prompts in one ACP session using original PID 55265; original Wait returned exit0 and ConfirmTreeExited succeeded. Exact command, source hashes and raw JSON hash are in `../data/root-integrated-receipt.json`. Earlier effective RED/GREEN protocol and lifecycle logs remain in data; this record supersedes the earlier native-not-run entries for the controlled actor only.

This is an internal transport foundation. Host/public bridge wiring and automatic next-turn consumption remain open. No real Grok process, model/account call, arbitrary TUI attach, or actual model-quality acceptance was performed. Intel Mac/Windows native acceptance remains deferred for lack of environments.
