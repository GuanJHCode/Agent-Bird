#!/bin/sh
set -eu
umask 077
data=${PLUGIN_DATA:?PLUGIN_DATA is required}
mkdir -p "$data"
marker="$data/pending.json"
pending=false
if [ -f "$marker" ]; then
  pending=true
fi
printf '{"version":1,"event":"session_start","pending":%s,"pid":%s}\n' "$pending" "$$" >> "$data/session-events.jsonl"
if [ "$pending" = true ]; then
  printf 'codex_orchestrator_pending=true\n'
fi
