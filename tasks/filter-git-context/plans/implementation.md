# F04 filter Git context

`OpenFilterGit` pins the native Git executable, permitted filter tools,
repository/common-directory identities, local configuration and explicitly
enumerated attributes. It exposes only a live in-memory context. Candidate Git
commands routed through that context retain the existing `process.Start` /
original `Wait` / tree-exit protocol and re-verify the pins before and after
each command. No materialize, prepare, freeze, DTO or Host behavior is changed.

The bounded regression set proves a valid context routes a command, config or
attribute drift invalidates it, and an unlisted malicious filter command is
rejected without execution.
