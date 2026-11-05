# T02 实现与验证（待独审）

基线：29691e9f08468b1de56cb0c42977084b58c91975。工作树：`/Users/guanjunhui/code/ai/agent-bird-hardening-20261105/tmp/t02`。未 commit/push，未使用真实模型，未修改 Store/schema/Host/Adapter。

## 冻结实现

- `taskHandle` v1 增加可选 `error`；rejected 决定与规范错误同一次原子 checkpoint 持久化。
- `main.go` 仅 `submitWithPreparedControl` callback 增加 reason 参数；保留 checkpoint 完成前不 cleanup 的已有顺序。
- `task dispatch` 重放 rejected 时 stdout 保留 rejected/error；首次与重放均退出 2；不会重新 Submit。
- 错误码限定 `[a-z][a-z0-9_]{0,127}`；缺失/不可公开旧错误返回 `submit_rejected_error_unavailable`，不猜原错误。
- 成功提交状态继续使用 `queued`、`queued_host_unavailable` 或 `reconciled`；不再把 `existing` 当业务状态，兼容性元数据 `existing:true` 仍保留。
- 未知状态 fail closed 为 `dispatch_checkpoint_invalid`。同 ID 不同规范请求仍为 `dispatch_request_conflict`。
- submitting/submit_uncertain 保留原 control 并走原 reconcile，成功后重读句柄返回 reconciled；未引入 retry-admission。

## 公开 CLI 红→绿

`task_dispatch_rejection_integration_test.go` 启动真实 controller/CLI 和协议 fake coordinator（无模型），模拟 owner/provider 与明确 Submit 拒绝。断言首/次均 rejected/invalid_submit、退出 2，Submit 总数 1；同一 fixture 继续验证冲突、旧 rejected 缺 error、未知 status。

初始 reproduction.txt 精确显示首拒绝无 error，第二次 status=existing 且 exit 0。初始测试误按 exit 1 断言，读取 main.go 产品约定后改为 exit 2；核心 red（第二次成功与错误缺失）不受影响。

## 命令环境

所有 Go 命令均使用：
`PATH=/private/tmp/go1.26.10-sdk/bin:$PATH GOENV=off GOTOOLCHAIN=local GOPROXY=off TMPDIR=/private/tmp GOCACHE=/Users/guanjunhui/code/ai/agent-bird-hardening-20261105/tmp/go-cache-t02 /private/tmp/go1.26.10-sdk/bin/go`

cwd 为工作树 `tools/orchestrator`。真实模型开关未启用。源码冻结 manifest 为 source-manifest.json；可集成稳定补丁 t02.patch（包含新测试文件）。

## 覆盖边界

- DIS-01/03：新增 public CLI fake coordinator fixture。
- DIS-02：复用真实已接受任务的 public dispatch/reconcile fixture；实际 Submit 回复丢失及原 control 保留由既有低层真实 IPC fixture 验证。未声称 public CLI fixture 本身丢弃 accepted Submit 回复。
- DIS-04：复用 checkpoint-before-cleanup 和真实拒绝 RPC crash/failure 边界 fixture；准备写失败、拒绝写失败、落盘后 cleanup 前 crash 均检查原恢复材料；没有新增每个系统调用级 crash 注入。
- 未执行安装包/真实 CLI 模型验收；未进行独立审查或主会话签收。

## 最终直接验证

- `go test -count=1 -run 'TestDispatch|TestManagedDispatchReuses|TestRejectedSubmit|TestSubmitRemoteRejection|Test.*Reconcile|TestLostSubmit' -json ./cmd/orchestrator`：exit 0，含子测试 {'pass': 47, 'fail': 0, 'skip': 0}，顶层 {'pass': 14, 'fail': 0, 'skip': 0}；输出 `focused-final.jsonl` SHA256 `945d4252770900a11c3cf44546a03e253ab9304cf38c912947e77775304e9139`。
- `go test -race -count=1 -run 'TestDispatchPublicCLI|TestManagedDispatchReuses|TestRejectedSubmit|TestSubmitRemoteRejection|Test.*Reconcile|TestLostSubmit' -json ./cmd/orchestrator`：exit 0，含子测试 {'pass': 23, 'fail': 0, 'skip': 0}，顶层 {'pass': 7, 'fail': 0, 'skip': 0}；输出 `race-focused.jsonl` SHA256 `393e01a3deebe2e6fc57e6fa5eefc77ec394f78b7d1cb375480f2f61e11da055`。

全 cmd suite 已另外启动，尚在运行，日志 package-tests.jsonl，不能当作 PASS。父会话可通过 exec session 96816 取得最终退出码。未因该额外检查延迟依赖任务独审。
