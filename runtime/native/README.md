# Native return transport runtime

Production Python modules for optional native owner attachment and delivery.
The package builder copies these modules into its versioned `runtime/g0` layout
for compatibility with existing pinned manifests. That installed name is a wire
and packaging compatibility detail, not a source dependency on `tasks/g0-*`.

Historical experiment imports under `tasks/` are thin compatibility loaders.
Their tests, fixtures and acceptance scripts stay in the task directories.
This runtime does not own a second task database or delivery queue. The Go
coordinator remains authoritative for task decisions and durable ACK.

Owner-only directories and capability files retain `0700` / `0600`. Native
attachment verification, original credential context and explicit recovery
remain required for native transport. Generic `owner-bind` / `collect` does not
load this runtime.
