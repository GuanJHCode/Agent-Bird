# Host writer fixture migration

Baseline `0bbb512` full Host suite completed in 236.219 seconds and exposed
legacy synthetic launch fixtures that bypass the managed Host artifact grant.

The first migration batch uses scope-unique test reservation IDs and creates
launch metadata through `metadataForLaunch(grant)` when a private execution
path must write a Host artifact. Legacy `Host.Execute` fixtures require a
separate Store `SubmitPlan` / `RegisterHost` / `ClaimReady` rewrite because an
unregistered `Host` has no producer identity and cannot satisfy a live grant.
