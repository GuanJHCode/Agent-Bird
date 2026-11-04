#!/bin/sh
set -eu

root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
binary="$root/bin/codex-orchestrator"
[ -f "$binary" ] && [ -x "$binary" ] || {
  printf '%s\n' '{"status":"blocked","reason":"orchestrator_binary_unavailable"}' >&2
  exit 2
}

case "${1-}" in
  controller|routing|provider|task|runtime-status|preflight|provider-probe|provider-lock|owner-bind|ensure-running|submit|status|summary|collect|wait-events|ack|accept|answer|retry|rework|request-changes|resume|stop|native-bridge|rebind-owner|recover-host|install|doctor|uninstall|pin|unpin) ;;
  *)
    printf '%s\n' '{"status":"blocked","reason":"unsupported_orchestrator_command"}' >&2
    exit 2
    ;;
esac

exec "$binary" "$@"
