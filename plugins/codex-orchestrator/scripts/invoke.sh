#!/bin/sh
set -eu

root=${PLUGIN_ROOT:?PLUGIN_ROOT is required}
binary="$root/bin/codex-orchestrator"
[ -f "$binary" ] && [ -x "$binary" ] || {
  printf '%s\n' '{"status":"blocked","reason":"orchestrator_binary_unavailable"}' >&2
  exit 2
}

case "${1-}" in
  ensure-running|submit|status|collect|wait-events|ack|accept|answer|retry|resume|stop|native-bridge|rebind-owner|install|doctor|uninstall|pin|unpin) ;;
  *)
    printf '%s\n' '{"status":"blocked","reason":"unsupported_orchestrator_command"}' >&2
    exit 2
    ;;
esac

exec "$binary" "$@"
