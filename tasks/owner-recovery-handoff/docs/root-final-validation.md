# ROOT 文件层验收

冻结提交28ba02b，两个 Go 源码hash与worker manifest一致；独立FS审查PASS。

- macOS arm64 标准package定向race：30 top / 216 named PASS、0 FAIL、5 UID SKIP，6.374s。
- Docker Linux arm64离线、readonly、nonroot：同216 PASS/5 UID SKIP；独立仅CHOWN capability容器执行foreignUID五子项，6 named PASS、0 FAIL/SKIP，补足全部UID缺口。两个容器exit0、非OOM，精确rm并inspect确认不存在。
- Linux binary sha256与两个container receipt一致，已删除该owned临时binary及空输出目录。
- 保留worker真实RED、ROOT race/Linux日志、sourcehash和container终态。不是掉电/真实fsync内核故障证明，也不包含DB/RPC/native接线。

明细见data/root-final-receipt.json；ROOT vet exit0，结果另存root-vet.log。只读文件层不是新Owner整体恢复闭环。
