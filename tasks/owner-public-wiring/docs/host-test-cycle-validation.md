# Host测试编译环修复

新增Coordinator→Host recovery lease生产依赖使旧Host内部native测试的反向Coordinator import构成cycle。真实编译RED保留root-host-cycle-red.log。只拆测试：父流程到external host_test，两个私有故障child留internal host，四个test-only alias/wrapper桥接。生产API未改，无测试排除/file-list替代。ROOT及独审逐字比对父369行（除公开符号host前缀）、私有receipt/child/helpers187行均保持原文。

- 标准go test -run '^$' ./internal/host 编译成功；整模块同一compile-only命令24包通过、0执行测试，不能当行为验收。
- 三个原真实永久故障parent race：3top3named PASS0FAIL/skip，53.569s。admission、publisher、durable-return窗口分别实际原owner Wait=exit1 non-signaled，11.021s/12.019s/11.542s，均在13s窗口内。后两case actualleader/descendant TERM后存活、最终产品KILL/ESRCH原断言保持；无forced cleanup或retained日志。
- host最终vet exit0；独立实际diff及依赖图审查PASS。证据和两个测试源码hash见root-host-cycle-receipt.json。

本修复恢复标准host package测试，是已授权恢复链集成的必要验收修复，不代表scope handoff已实现。
