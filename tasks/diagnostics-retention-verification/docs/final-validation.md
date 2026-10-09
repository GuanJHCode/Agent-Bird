# F02 显式保留期集成验证

生产与测试源码固定 `61daae64482a6a8aca4e0d4a1374bba50c9e57bf`，worker交付 `597a46d`；ROOT验证快照 `082185da73ace2c4a6862d2d82dcd28e09b77526` 的完整 Go tree 同为 `636f74d9648dca20e0e483116676a933a52412b1`。固定生产增量独审 PASS；新 Linux runner `3a97dd8` 独审 PASS。

## 当前有效证据

- macOS arm64 Go1.26.8 定向 race：12 top / 64 named PASS，0 FAIL / SKIP。ROOT读取原JSONL计数并核SHA `62a72acd4398396cda20094ae911098a8b24c11fb333a65af19c391ce48a2ac0`；Store10/62、physical deletion crash1/1、public CLI1/1。8个新Retention顶层用例均在内，包含WirePresence。此前wire-apply-green02真实3/19，不是3/18。原命令/全部失败详见[worker报告](../../diagnostics-retention/docs/final-report.md)。
- 公开CLI子进程直接执行同一个带 race 的 Go test binary 中的真实CLI `run` 入口，并通过真实Unix socket访问Coordinator；不是另外build生产CLI。plan stdout810bytes原样0600保存→apply→exact replay、6种恶意Excluded外层wire拒绝均通过。Host身份fixture原Wait0，CLI原CombinedOutput完成Wait，crash原child Wait exit91；不是真实Provider执行。
- ROOT对固定源码独立 `go build ./...` 及 Store/Coordinator/IPC/CLI `go vet` exit0，stdout/stderr均空。原命令与环境在 `data/{build-02,vet-02}.command.json`，原输出/exit同名保存。旧build-01/vet-01仅对应前期源码，保留但不替代最终检查。
- Docker Linux arm64 Store：仅 `^TestDiagnosticsRetention`，8 top / 58 named PASS，0 FAIL / SKIP，original attach exit0、容器终态exit0且非OOM。expected集合由已通过Mac最终raw提取并封闭比较，未将缺少测试当PASS。Linux log SHA `5e08bdc2eaa0d79bccf047b05b5d330e94c2ae4cb29cf751c3e3a26385310956`。无race、无Linux公共CLI/真实Provider资格。
- Linux只用已缓存固定image，pull=never/network=none/nonroot/readonly/capdrop/限额。runner和ROOT分别核exact CID删除后No such object。原二进制SHA `2f2b337d5648d703723bc240296d0a062addc8dfdb696ed2e9835ab6c07b0cc1`；终态后核UID/nlink/inode/hash，仅删除本任务19,298,417bytes binary，所有raw/receipt保留。详见[data/root-verification.json](../data/root-verification.json)和[data/linux/final-01-receipt.json](../data/linux/final-01-receipt.json)。

显式保留期仅作用于最新已完成attempt的detailed diagnostics，以Store可信Exited最大接收时间为cutoff，非任务完成TTL。无自动timer/GC、其他artifact/host_progress/report_events保留期；没有全仓重测或物理断电/整盘/RSS资格。旧F13包不包含本次生产delta，最终稳定组合后再打包。未调用真实模型、替换用户安装或发布。
