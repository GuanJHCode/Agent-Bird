# launchctl PID 最小投影

原文本parser丢弃pid，无法把launchd job身份与真实服务OS peer/receipt交叉。仅保留depth=1且唯一的正整数pid；nested pid忽略，重复（即使相同）/负值/0/小数/非数字拒绝UNKNOWN。其余标量仍丢弃，不导出env/token或改变事务授权逻辑。

TDD RED 7 failed/1 passed；与原transaction同进程 GREEN 44 passed/4.64s，未执行真实launchctl。
