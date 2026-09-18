# 公开发布边界

公开仓库包含当前产品源码、合成 fixture、通用运行时和打包测试。新仓库保留自己的初始历史，不导入旧研发仓库的 Git 对象。

不发布：个人 AGENTS/Skill 配置、认证材料、Provider 会话、本地 lock/handle/capability、SQLite 状态、原始对话和输出、个人机器路径、一次性升级脚本及历史验收 data/tmp。private native product acceptance 驱动及绑定个人环境的测试不属于公开测试闭包。

路径规则：项目文件从仓库根目录或脚本位置相对定位；用户和系统目录通过运行时 API/显式参数获取。安全校验要求的绝对路径、系统工具路径以及明确标记的合成 fixture 不代表开发者本机配置。

发布前在独立检出中执行构建、测试、个人路径检查和 secret scan，并检查 Git 暂存清单。测试或构建生成的数据库、缓存和打包输出不能纳入提交。生成的 Provider locks 和能力文件仅限本机私有目录。

首次源码导入的本地验证：核心 Go 592 pass / 2 opt-in skip，相关 race 35 pass；独立 Go 模块 70 + 94 pass；公开 Python 套件 141 pass / 2 opt-in skip；portable 构建、完整包执行位校验和 Plugin/Skill 校验通过。暂存源码的个人路径检查与 Gitleaks 扫描未发现敏感信息；这些结果不替代真实 Provider 或 native TUI 验收。
