# Gate证据交接

源码9aca86b已由ROOT合并到62c9e4e，完整独审中。原focused仅host reconcile代表入口：plain Go test exit0；其named-counts.txt是包摘要，不是具名测试计数，Attach门禁当时尚未添加。

worker额外启动全Store命令，最后报告运行中但未交可恢复tool session。ROOT按其原PID只读核查时进程列表已无这些PID，但没有原工具终态、exit文件或stdout内容，**不能据此推断完成、通过或原Wait**。全部partial raw/command保留；未伪造exit、未重跑全Store、未通过PID补造Wait。ROOT改用已集成固定源码的一批相关-race -json作有效验收，详见主F10任务最终证据。该额外全量不纳入通过声明。
