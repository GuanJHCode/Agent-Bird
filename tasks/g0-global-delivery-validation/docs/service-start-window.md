# service-start 时间下界修复

InheritedFdScheduler 在 bootstrap 中启动独立服务，合法 service-start 可以早于 register 返回以及随后 native 编排阶段的 start。旧下界用 native start，会因调度时序误判 UNKNOWN。

生产仅一行修改：下界改为本次 `registration.started_raw`，即已 O_EXCL 持久化的 `register-start.json.started_raw`；上界仍为原 startup。新state、manifest、实际job PID、kernel/service身份与后续owner/helper绑定都不变。

真实独立服务时序回归先等待 service-start 写成，再延迟 register 返回，明确验证 `register-start <= service-start < register-return <= native-start`。RED 在 ungranted-activation 因时间窗口拒绝；GREEN 完成实际dummy服务/HTTP/GoACK和对称revoke。

最后源码修改后原34项加该回归：**35 passed in 18.95s**。RED/GREEN原输出分别在 `service-start-window-red.log` / `service-start-window-green.log`。使用 runner.md 的固定safePython和显式只读pytest依赖，禁自动插件，测试选择 `tasks/g0-global-delivery-validation/tests`。

未执行native、launchctl或真实auth，没有改动旧case或其他接口。
