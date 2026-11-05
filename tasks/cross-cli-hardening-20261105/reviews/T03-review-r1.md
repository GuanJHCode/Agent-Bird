# T03 初审：CHANGES_REQUESTED

实现/root，独审/root/review_t03；冻结t03 HEADbde4bacf+31文件，patchSHA e509cfda9817b58e7177b103062ff8a0fabdd0d497a790a40321c76d39c22dc6。独审核31项manifest、独立focused18PASS，发现P2：64800-byte budget_group_id的真实Submit envelope65455 bytes可发送，Store已创建task/run/policy后ClaimReady才launch_message_too_large。

证据保留外部tmp/review-t03/{boundary_test.go,overlay.json,boundary.log,focused.json}；独审用overlay，未修改冻结源码。建议共同contract约束新控制ID的UTF-8字节，贯通CLI/Store，保留旧row/receipt和最终Launch门禁，覆盖AcceptedInput重复ID与adapter余量。

其它预算跨run、legacy组隔离、unknown、retry/rework/rebind、能力门禁及迁移oracle未发现新缺陷。修订前普通4包673PASS/4skip、race134PASS均不使此缺口失效。root在上述进程结束后才修改源码；新增共同ID<=128字节准入与分层回归，修订目标待最终验证/复审。当前不能标done或提交。
