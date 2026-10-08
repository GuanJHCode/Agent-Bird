# F12 supervised installed-config public entry

## Scope

Exercise only a task-private installed package and controlled local provider
fixture. The public managed-owner sequence is `provider snapshot publish`, then
`task submit`. The acceptance requires the frozen generation to be consumed by
the actual Store submission and the persisted launch mode to be
`supervised-v1`.

## Frozen boundary

The fixture owns its temporary package, provider executable, state directory,
request files, process tree, and coordinator cleanup. It uses no real model,
account, user installation, or external provider. The provider is only a
protocol fixture; it is not evidence of model quality.

## Existing reusable coverage

`internal/install/config_generation_test.go` covers generation pinning,
pending-state rejection, and conflicting consumers. The new test covers the
missing public-entry composition. Pending/conflict are not reimplemented until
the public supervised admission is reachable.
