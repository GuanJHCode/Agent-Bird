# F13 filtered Host current candidate package

`filter-host-current-20261010` accepted the clean source `61431425530ab0befb7486376029f644957c3ae8` (repository tree `3676062fd02fe56f050672c5887a4fea77f6a759`, orchestrator tree `d43e8ac891e20e77c2e969a6152f476ef5ebe6af`) on macOS arm64. It reuses the reviewed F13 runner without changing production code, tests, packaging code, or the runner.

The runner recorded 12 completed commands, all exit 0 with empty stderr: pinned Go version and module verification; two independent `-trimpath -buildvcs=false` builds; build metadata; two `full-native-hooks` packages; task-private public install, exact install replay, Doctor, owned uninstall, and post-uninstall Doctor. The runner required Go 1.26.8 with offline dependencies and the pinned Python 3.14.3 path/hash.

The accepted version is `0.1.0-closure.61431425530a`. The two builds have binary SHA-256 `2a911b89919a071e883991171cc6dfd24ade54b5b8212d2d2de86c32b56013d0`; the retained package manifest SHA-256 is `afdec3c28dedbfdc031e7215c8b13792bc2512b050cf90166b8457370fd03f21`. Both packages contain 28 files with equal bytes and modes. An independent fixed closure verified 24 source files; the installed root had 27 payload files plus its manifest, and replay retained the binary identity. The test installation was removed and the task-owned runtime-data sentinel remained.

Raw command evidence and the full receipt are in `data/filter-host-current-20261010/`. The retained package is `tmp/filter-host-current-20261010/package-a`. The duplicate two builds and `package-b` remain untouched pending the separate authorized cleanup decision.

This is a macOS arm64 package and task-private installation-management check for this exact source. It does not register hooks, replace a user installation, run a real Provider/model/account, or establish F12 resources, cross-platform support, real CLI/TUI consumption, or any broader F01–F13 completion.
