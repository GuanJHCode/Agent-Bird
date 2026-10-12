# ACP Prompt capacity reservation

## Goal

Reserve bounded local receipt capacity before every remote ACP Prompt, keyed to
the exact binding, owner epoch, turn and attempt. Keep an indeterminate
reservation until the completion receipt is durably persisted.

## Plan

1. Extend the existing receipt quota directory and its locked accounting with
   private reservation records.
2. Reserve before `client.Prompt`; release only when no prompt started or after
   the immutable completion record exists.
3. Add focused regression coverage for capacity refusal and lifecycle release,
   then preserve the test transcript.
