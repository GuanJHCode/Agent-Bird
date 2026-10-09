# FD timing evidence limits

The framed SCM_RIGHTS probe and two ordinary native results have retained raw outputs. The outer-copy framed-rights stdout differed from the worker commit, so both versions were retained; no raw was overwritten. No production FD protocol changed. Two earlier native invalid_ipc_message failures remain unexplained; successful repetitions do not erase them.

The worker additionally reported a narrow race pass and an accidental whole-IPC failure (TestOwnerInheritedControlRejectsOtherDescriptorKinds/listener: Unix bind invalid argument), but did not save either raw output or an exit file. They are not accepted as verification evidence. The original environment cannot be fully reconstructed. ROOT will retain actual focused final evidence, without reclassifying the earlier failure.
