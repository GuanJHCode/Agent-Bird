# F07 subphase acceptance record

This subphase is limited to portable Linux `check` admission. Required evidence:

- policy tests: Linux accepts only `check`; Darwin's action set remains intact;
  non-Darwin/non-Linux rejects;
- strict portable tests: legacy, full-native, and invalid collect-only payloads
  reject before destination creation;
- valid strict collect-only package remains immutable on repeated install and
  passes Doctor;
- root-owned Docker execution exercises the compiled Linux binary under the
  offline readonly `nobody` container. It is intentionally not run in this
  worktree.
