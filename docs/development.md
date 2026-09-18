# 开发与验证

Go 1.26+、Python 3、pytest、PyYAML；进程和 sandbox 验证针对 macOS。从仓库根目录：

```sh
(cd tools/orchestrator && go build ./... && go test -count=1 ./... && go vet ./...)
(cd tools/orchestrator && go test -race -count=1 ./...)
(cd tools/g0-probe && go test -count=1 ./...)
(cd tools/g0-return-lab && go test -count=1 ./...)
python3 -m venv .venv
.venv/bin/pip install pytest PyYAML
.venv/bin/python -m pytest tools/native-product-bridge tools/real-user-trial \
  tasks/brain-control-hardening/scripts/test_package_skills.py \
  tasks/provider-runtime-completion/scripts/test_runtime_layout.py \
  tasks/mvp-simple-install/scripts/test_package.py -q -rs
```

Go CLI 测试会重新构建源码并运行受控进程，运行时不要修改 Go 文件。测试使用临时目录和合成输入，不需要 Provider 登录。真实 Provider 测试默认跳过，需要单独授权和已登录环境。

`tasks/` 中保留的是运行时兼容入口、合成测试依赖和通用打包源码，不包含本地 data/tmp。旧 private native acceptance 运行器及其机器固定输入测试不在公开分发范围内，不能从公开测试通过推断原生 TUI 验收通过。

`./scripts/build-plugin.sh` 生成 collect-only portable 包。完整 Python native-runtime 包的开发工具仍位于 `tasks/g1-g4-delivery/scripts/package-plugin.sh`，参数需由本机解析后传入，其安全校验仍要求规范绝对路径；不要修改解释器权限来绕过失败。

## 通用主脑与编码验收

`TestCodingHostFreezesProviderCandidate` 使用合成 Grok/AGY 输出，但真正执行 macOS 沙箱、独立 Git worktree、候选冻结和行为断言；它不会调用模型。

`TestNativeCodingHost` 默认跳过。仅在明确授权新的会话目录写入及一次最多 120 秒的模型调用后执行：先核验实际二进制版本/SHA-256，再设置 `AGENT_BIRD_NATIVE_CODING_PROVIDER`（`grok-build` 或 `antigravity-cli`）、`AGENT_BIRD_NATIVE_CODING_BINARY`、`AGENT_BIRD_NATIVE_CODING_VERSION`、`AGENT_BIRD_NATIVE_CODING_SHA256` 和 `AGENT_BIRD_NATIVE_CODING_EVIDENCE`。最后一个参数必须指向不存在的绝对目录；测试将保留现场，不删除重试。运行：

```sh
cd tools/orchestrator
go test -count=1 -run '^TestNativeCodingHost$' ./internal/host
```

这个 opt-in 测试证明编码及候选冻结，不代表主脑自动派发、复审和集成的端到端验收；真实 CLI 的交互行为也需单独核验。不得把既往一次性验收授权或失败现场当作新预算。
