# F06 ACP transport foundation

Scope: `internal/grokacp` only. The package owns a bounded ACP v1 JSON-RPC NDJSON client over an injected duplex stream. It does not start Grok, access credentials, attach a TUI, or alter Store, Host, adapter, contract, or support reporting.

The client sends `initialize`, one `session/new`, then sequential `session/prompt` calls bound to that returned session ID. It keeps one reader and serialized writes; it rejects foreign session updates, duplicate JSON keys, invalid UTF-8/lone surrogates, malformed frames, and bounded-queue overflow. A write followed by cancellation, partial frame, or EOF yields terminal uncertainty and closes the stream. Reverse `session/request_permission` is replied to with cancelled; all other reverse requests get JSON-RPC method-not-found.

Native Grok ACP and StartDuplex coupling remain for the integration owner; a prompt stop reason only establishes ACP turn termination, never model-result success.
