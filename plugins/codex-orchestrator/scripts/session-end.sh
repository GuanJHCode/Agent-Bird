#!/bin/sh
set -eu
umask 077
data=${PLUGIN_DATA:?PLUGIN_DATA is required}
mkdir -p "$data"
printf '{"version":1,"event":"session_end","pid":%s}\n' "$$" >> "$data/session-events.jsonl"
