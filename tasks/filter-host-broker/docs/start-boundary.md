# Filter 原始 Start 边界

F04 broker计时的首依赖已完成。生产2dad5f3直接把原Handle.DuplexLaunchState().SpawnedAt复制到Result.StartedAt（json:"-"），保留同进程monotonic；不从诊断Identity时间、journal或新时钟重建。非nil Handle缺metadata仍拒绝接受并沿用原Stop/Wait/tree/join，preflight为zero。不改变public filter限制，尚未实现Host active settlement/Store资源预约。

固定954da31真实RED：1top/3named，1PASS2FAIL，失败为缺原始时间；原两个filter均Wait/tree已证明。首race固定2dad5f3：1top/3named，1PASS2FAIL，monotonic比较通过但最后JSON oracle误匹配既有嵌套Identity.StartedAt诊断字符串；原FAIL保留。test-only1d24a18改精确顶层key与JSON读回live zero，未删除进程/mono断言或修改产品。

最终clean1d24a18定向race：1top/3named全部PASS，0FAIL/SKIP，exit0/stderr空。实际原actor normal exit0与post-Start missing-stream注入exit-15均原Wait/tree=true，构建actor原Wait0/tree。ROOT核三批raw及三个ownedfixture root absent，摘要见data/start-boundary-summary.json。定向gitfilter vet exit0/空输出（2dad5f3，之后仅oracle变化）；生产2dad5f3及oracle1d24a18独审PASS。旧1ec0cb5范围未改的25case不重复运行。

无真实Provider/model、用户安装替换或推送。该值仅供原Host下一阶段budget计时，不是wire/cold证明；完整durable broker、Git双视图与公开扩展尚未验收。
