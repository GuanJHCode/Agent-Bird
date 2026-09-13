# Native 0.154 pagination notice

This patch precisely accepts the fixed pagination `deprecationNotice` summary
with absent/null `details`. Other summaries, details, fields and server requests
remain rejected. No RPC permission, frame/message limit or deadline changes.

Official source commit `6b9826e3aa83b1a5947db50f4332cb9c65f1b340`:
`tasks/g0-native-0154-contract/data/source/codex-rs/app-server/src/request_processors/thread_processor.rs`
defines the summary at line 34, sends `details: None` at 818–828, and awaits
that notification before returning a paginated `thread/read(includeTurns=true)`
response at 866–885. Saved source SHA256:
`37a4451e58673b3577c432207a510a7322fd10af5e1e535bb2be8317d5fcb1d9`.

Native-tui-03 directly recorded the successful helper initialize and subsequent
`thread/read` request 1001, followed by `websocket_eof`. The rejected packet was
not captured. Attribution to this missing notice is a fixed-source inference,
supported by the dummy reproduction; it is not a recovered native wire packet.
The original case and its failure classification are unchanged.

The actual independent service/PTY/Go dummy chain now emits the official notice
before compatibility reads. Before the patch it failed with the same child EOF
hash (`native-deprecation-red.log`). The negative case injects an unknown canary
method: it still fails before Go prepare/claim, and the real service's immutable
`activation.json` stores bounded `wire_rejections` with frontend/connection/epoch,
fixed stage/reason, exception type/hash, opcode, declared frame length and method
hash/length. No rejected method text, body or history is stored. The diagnostic
RED had no rejection row (`native-wire-diagnostic-red.log`).

Final verification: **71 passed, 8 existing multi-threaded fork warnings,
18.92s, exit 0** (`native-deprecation-green.log`). The first combination had
70 passes and one existing `initial-history` case fail early at `tui-start`;
its assertion did not include the inner error. The unchanged startup path then
passed individually and in the final combination. That isolated cause remains
unresolved, not claimed fixed; all three logs are retained.

```sh
PYTHONDONTWRITEBYTECODE=1 PYTEST_DISABLE_PLUGIN_AUTOLOAD=1 \
/opt/homebrew/Cellar/python@3.13/3.13.5/Frameworks/Python.framework/Versions/3.13/bin/python3.13 -I -B -c '
import sys
sys.path[:0] = ["/Library/Frameworks/Python.framework/Versions/3.13/lib/python3.13/site-packages", "/Users/guanjunhui/Library/Python/3.13/lib/python/site-packages"]
import pytest
raise SystemExit(pytest.main(["-q", "-p", "no:cacheprovider", "tasks/g1-g4-delivery/tests/test_native_tui_case.py", "tasks/g1-g4-delivery/tests/test_native_deprecation_gate.py", "tasks/g1-g4-delivery/tests/test_native_resume_gate.py", "tasks/g0-completion/tests/test_owner_helper.py", "tasks/g0-completion/tests/test_owner_helper_service.py", "--tb=short"]))'
```

No Codex/native, external model, launchctl or real-auth operation was run.
Dummy temporary profiles/listeners were removed by their test contexts; evidence
logs and the pre-existing task-local build cache remain for review. Actual native
validation and source-pin preparation remain the root controller's next step.
