# Agent Bird — CLI 版本兼容交接快照

本分支用于接手本地研发中的 Go 编排器，不是产品发布或已完成的主线整合。

- 基线为本仓库 main 的 `2e78f24c5e9694f287d19065f2ce9c80b16a59af`，保留公开仓库历史。
- `tools/orchestrator/` 是本次冻结的完整研发模块；同时带入打包脚本的缓存过滤变更。
- 其他产品入口和运行时文件沿用上述公开基线，尚未与本模块做产品级兼容整合。
- 私人研发 Git 历史、原始运行记录、账户材料、机器配置和二进制不在此分支。

当前可复现的入口是 Go 控制面，要求 Go 1.26+、macOS；真实 Provider 测试默认关闭：

```sh
cd tools/orchestrator
go test -count=1 ./...
go test -race -count=1 ./...
go vet ./...
go build ./...
```

后续工作、验收结果及已知问题见 [交接说明](docs/cli-version-handoff.md)。
正式产品说明请查看仓库的 main / master 分支；本交接分支不宣称其安装入口已验收。
