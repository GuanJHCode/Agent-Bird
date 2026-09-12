# fixed 0.154 source receipt

All source references use official commit `6b9826e3aa83b1a5947db50f4332cb9c65f1b340` (`rust-v0.154.0`). No credential body or local auth file is retained here.

| fixed path | official URL | SHA-256 | audit use |
| --- | --- | --- | --- |
| `codex-rs/login/src/auth/manager.rs` | https://raw.githubusercontent.com/openai/codex/6b9826e3aa83b1a5947db50f4332cb9c65f1b340/codex-rs/login/src/auth/manager.rs | `87926092db3a115b8269c6fdbc6958e2268d9750ddcb8baa1799f1ec7e3ba4d9` | API-key load, refresh guard, persistence |
| `codex-rs/login/src/auth/storage.rs` | https://raw.githubusercontent.com/openai/codex/6b9826e3aa83b1a5947db50f4332cb9c65f1b340/codex-rs/login/src/auth/storage.rs | `2e95a8998687e926675d41b5d941c3c3546a652c03d9680e6c7385e59bf459af` | `auth.json`, File/Ephemeral/Keyring stores |
| `codex-rs/protocol/src/auth.rs` | https://raw.githubusercontent.com/openai/codex/6b9826e3aa83b1a5947db50f4332cb9c65f1b340/codex-rs/protocol/src/auth.rs | `c7293c4b592e7429b6aeb969b15ba0cfe056729f33b09583912fec250b00c756` | lowercase `apikey` serialization |
| `codex-rs/app-server/src/request_processors/account_processor.rs` | https://raw.githubusercontent.com/openai/codex/6b9826e3aa83b1a5947db50f4332cb9c65f1b340/codex-rs/app-server/src/request_processors/account_processor.rs | `fb166a19016869376162a93e6b9a646e5537553aa3834f6c5c6521918f4a79e0` | account/rate-limit auth calls |
| `codex-rs/utils/home-dir/src/lib.rs` | https://raw.githubusercontent.com/openai/codex/6b9826e3aa83b1a5947db50f4332cb9c65f1b340/codex-rs/utils/home-dir/src/lib.rs | `1338259489ade54b01caa1468b6d25fc5a183657fa94cca0add8b91865635ef5` | `CODEX_HOME` resolution |
| `codex-rs/core/src/config/mod.rs` | https://raw.githubusercontent.com/openai/codex/6b9826e3aa83b1a5947db50f4332cb9c65f1b340/codex-rs/core/src/config/mod.rs | `4967b9f719ac886d5c6dd923f4d07263fe4eef2a7070078c5a291f57973c76a1` | `cli_auth_credentials_store`, no-auth provider |
| `codex-rs/tui/src/lib.rs` | https://raw.githubusercontent.com/openai/codex/6b9826e3aa83b1a5947db50f4332cb9c65f1b340/codex-rs/tui/src/lib.rs | `fd85e541df5affb811f7133d967dcb294292c77bbce1e2cffc16eb670d3c34ec` | default socket probe and login screen |
| `codex-rs/app-server-client/src/remote.rs` | https://raw.githubusercontent.com/openai/codex/6b9826e3aa83b1a5947db50f4332cb9c65f1b340/codex-rs/app-server-client/src/remote.rs | `6d61d61ba5132e25ccd16a413bad5ab2e7059c4798d2ee3b08e93a9c33bb0986` | Unix connect and WebSocket handshake |
| `codex-rs/app-server-transport/src/transport/mod.rs` | https://raw.githubusercontent.com/openai/codex/6b9826e3aa83b1a5947db50f4332cb9c65f1b340/codex-rs/app-server-transport/src/transport/mod.rs | `537d15b8d6927e36922351137ce52fcc687564d8228ea02dce16e5ee4de37ab5` | default control socket path |
| `codex-rs/app-server-transport/src/transport/unix_socket.rs` | https://raw.githubusercontent.com/openai/codex/6b9826e3aa83b1a5947db50f4332cb9c65f1b340/codex-rs/app-server-transport/src/transport/unix_socket.rs | `08b3f6d88895567645e152d122ff70e96eaf80e82dbcf00fd9c8e7b11bb19c88` | UDS mode/cleanup behavior |

Relevant fixed line anchors:

- `manager.rs:300-314` loads API-key auth without ChatGPT token state; `:980-1002` defines the API-key file shape; `:2364-2371` and `:2945-2967` gate proactive refresh; `:3033-3045` persists only refreshed ChatGPT tokens.
- `storage.rs:39-59` defines the auth payload, `:154-207` maps File storage to `CODEX_HOME/auth.json`, and `:459-528` implements Ephemeral/File/Keyring selection.
- `account_processor.rs:1106-1137` shows `account/read`'s explicit refresh flag versus `account/rateLimits/read` calling `auth()`.
- `tui/src/lib.rs:459-489` probes the default Unix path, `:395-404` builds the endpoint, `:1913-1923` derives login status, and `:2071-2079` skips login when the provider does not require OpenAI auth.
- `remote.rs:785-806` connects to the generated Unix path before the WebSocket handshake.

The local source snapshot used for line verification was kept outside the main worktree at `/tmp/codex-fixedline.gQu41F`; it contains only these fixed public source files and no credential material.
