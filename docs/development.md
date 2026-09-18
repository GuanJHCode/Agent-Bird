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
