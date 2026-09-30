# 本窗未随包携带的派生件（第 2 轮 F-B）

`n01probe.test` 是本窗 `go test -c -o <root>/n01probe.test ./internal/host` 的**编译产物**，
不属交付源/证据面，故不打包（13 MB 二进制会污染逐文件 SHA 闭包）。它的身份已由窗证据固定：

- SHA-256：`489b58bb41e73192f466080ae5b71204359a00f751429cd97507fc69cc393ea1`
  （见同目录 `n01-paired-summary.json.precompiled_binary_sha256`，由 `sha256` 在窗内实测写入）
- 编译命令与源码树：同 summary 的 `source_root` =
  `/private/tmp/bird-nseries-repair-20260930-3bcaf2/scratch/exp-n01`
  （= 联合树 + `snapshots/second/data/experiments-only/zz_n01_timeline_test.go` 的独立实验树），
  以及同目录 `compile.log`（空 = 无错误输出）。
- 20 次迭代全部使用这**同一个**二进制（`window.log` 的 `bound compile` 之后无第二次编译）。
- 本地仍保留一份于 `evidence/ROUND2/FB/paired-window/n01probe.test`（工作区证据面，非交付面包）。

复现：`BIRD_N01_WS=<exp-n01> python3 run_n01.py paired <全新绝对目录> <exp-n01>`。
