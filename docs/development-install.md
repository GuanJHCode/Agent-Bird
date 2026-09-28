# 开发者构建与旧完整运行时

需要 Go 1.26+、Python 3，以及已由用户正常安装/登录的 Provider CLI。原生回注需要额外完成原生接入验收；通用 `owner-bind` / `collect` 不依赖它。

## 构建与打包

以下命令从仓库根目录运行，只操作这个演示目录：

```sh
umask 077
task_root="$(pwd)/tasks/local-install"
mkdir -p "$task_root/data" "$task_root/tmp"
(cd tools/orchestrator && go build -o "$task_root/data/codex-orchestrator" ./cmd/orchestrator)
task_python="$(python3 -c 'import pathlib,sys; print(pathlib.Path(sys.executable).resolve())')"
sh tasks/g1-g4-delivery/scripts/package-plugin.sh \
  --binary "$task_root/data/codex-orchestrator" \
  --python "$task_python" --version 0.2.0-dev \
  --out "$task_root/data/package"
```

输出目录须为空。打包校验 `plugin.json` 声明的 skills、入口执行位、Python 运行时与文件摘要；正式 Python 实现来自 `runtime/native`。

## 安装到指定私有目录

```sh
python3 - "$task_root" <<'PY'
import json, pathlib, sys
root = pathlib.Path(sys.argv[1])
request = {
    "source_root": str(root / "data/package/plugin"),
    "binary_path": str(root / "data/codex-orchestrator"),
    "destination_root": str(root / "data/install"),
    "data_root": str(root / "data/state"),
    "version": "0.2.0-dev",
}
path = root / "data/install.json"
path.write_text(json.dumps(request))
path.chmod(0o600)
PY
"$task_root/data/codex-orchestrator" install --request "$task_root/data/install.json"
```

后续使用安装响应 `result.version_root` 下 `bin/codex-orchestrator`，并把该 `version_root` 暴露给主脑的插件/Skill 管理器。此安装流程不自动修改 Agent CLI 的账号、配置或登录服务。

## 主脑的一次工作闭环

1. 用 [Provider 探测](provider-runtime.md#探测与升级) 生成锁候选，核对实际版本与能力；用户明确确认摘要后写锁。
2. 主脑执行 `owner-bind`，将投影加入 submit 请求。设置 `owner_mode=local`、`delivery_mode=collect`；为每个任务写清目标、输入、范围、约束、产物和验收条件。读取任务默认用 reviewer profile。
3. `submit --request <0600-request>` 返回控制文件路径。使用 `wait-events --timeout-ms 30000`，避免高频 status 轮询。
4. 收到 result 后读取登记的 artifact，核对实际测试证据。用该事件的 ID、revision、hash、action slot 执行 `accept` 或 `retry`；问题用 `answer`。不把 ACK 当成批准。
5. 使用该页的 collection proof 和逐事件处置决定完成 ACK。上下文压缩后运行 `summary`；owner 丢失先显式 rebind，再决定恢复。

这是主脑的操作契约，语义验收仍由主脑完成。[CLI README](../tools/orchestrator/README.md) 提供各命令和请求字段。

## 验证与卸载

`doctor --request <0600-json>` 接收 `{"destination_root":"<install-root>"}`，检查安装内容和 pins。

`uninstall --request <0600-json>` 接收 `destination_root` 和 `version`。已被任务 pin 的版本不能直接卸载；数据目录与安装目录分开，卸载不删除运行历史。测试、源码快照或未确认退出进程的现场不得作为“安装残留”直接删除。
