# T12 native loopback auth oracle 与测试入口校正

最终 .3 产品二进制和包不改变。补测固定Claude2.1.316、owned loopback synthetic gateway、无真实credential/model后发现原native fixture在ORCHESTRATOR_CLAUDE_NATIVE_AUTH=api-key时，managed分支仍无条件清APIKEY、设AUTH_TOKEN；服务器却期待X-Api-Key，因而一读通过一写失败。原失败证据保留在外部t12-regression-r3/data/native-claude-transport.jsonl。

仅改*_test.go按相同既有apiKey选择初始化Host源环境，两个auth变量先清零后仅设选中的一个，不删除或放宽header/model/tool/Write/freeze断言。正常token与APIkey读写分别用固定CLI实测；不借用用户网关，不调用模型。包不含测试文件，生产Go/插件/runtime逐文件SHA保持d700356c，故无需重建同一最终 .3。

另外docs/testing.md引用两个未跟踪/不存在Python测试和verify_install.py，真实执行报pytest exit4、0tests。改为当前已跟踪且实际执行的两个Python套件，并记录installedGo opt-in和短TMPDIR；旧错误报告保留。新301pass/7明确nativeoptin skip，独立于native真实loopback/APIkey证据。docs/testing.md不属于最终package manifest，不改变其字节。此批须独审/root签收后单独commit/push。

包独审还确认archive本身mode正确，但限制性umask下解包若不保留permissions会导致校验失败；docs/installation.md补充在0700新目录内tar -xzpf，不改全局umask、不放宽产品验证。该文档同样不纳入.3归档。旧外部acceptance-plan残留.2标题仅是历史准备材料，真实.3 implementation report、identity和独审报告为交付依据，不覆盖旧158pins伪称其已审过新文字。
