# 测试说明

命令见 [开发与验证](development.md)。覆盖 Go 持久化状态、会话 Provider 控制、模型选择、工作区隔离、进程归属、Python bridge/auth 边界和打包契约。

真实子进程 fixture 不等于真实模型验收。默认跳过的 Provider、native attachment 和外部产品测试必须单独说明，不作为普通测试通过的一部分。请保留失败、预算和原始结果，不删断言或重建任务来制造成功。
