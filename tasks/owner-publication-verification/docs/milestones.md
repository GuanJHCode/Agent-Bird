# ROOT F10验证里程碑

verify WT固定组合acad8ce=62c9主代码+stage568，后续只加runner到a4246de。第二轮gate独审BLOCK仍在另WT修，当前不能称完整F10验收。

Linuxrunner复制已审F02 finally-CID闭集harness；43e29初版stage selector误含CrashChild被独审指出（尚未执行），d191一行修成6业务top闭集，复审PASS。未为失效selector制造运行证据。

linux-stage-build01固定a4246de，离线Go1.26.8 CGO0/linux-arm64编译成功、stdout/stderr空；hostbootstrap subtree与568完全相同。stage01在cachedexactimage/nonroot/readonly/no-network运行，6top31namedPASS0F/S、original attach/容器终态0、非OOM；8次真实SIGKILL原Wait/tree。raw SHA dc8c5ac34b38ba2894501584629c97b7b933a598e031da677cfeadb01b40554e。ROOT核actualPASS集合与Mac31named相等、8条原Waittree，runner与ROOT二次inspect exactCID 27799cffa1c1ea9f7ca1fd250bd808b9606ae3b5ceeca393a27f0ede1a45af11不存在；只清本任务5,130,220bytes testbinary，原inode/hash/UID/mode/size和清理收据保留。

ROOT另外读取stage Mac final原race6top31named/exit0/SHA及Win leafbuild0，并核10个native私有fixture根确已不存在，见data/stage-root-absence.json。这不是Windows native/物理断电/资源资格，不调用真实模型/改用户安装/发布。

完整build/vet、其余Linux(Store/IPC/public)与gate delta最终证据等待稳定修复后补；不重复此前有效native/race矩阵。
