# 90秒累计额度分配独审

`/root/review_claude_r5_acceptance`：PASS。仅单段60,000→90,000；总计90,000、max_attempts2、每方向wall180,000、串行、模型、.8身份、accept/ACK与消费门禁保持。产品grant=min(per-segment,total-charged)，第二attempt只可使用同group余量。postflight检查settled、单grant≤90,000及sum used≤90,000，旧超预算/额外attempt等负例保留。

root另核277pass/0fail/error/skip；setup51pins匹配，双方向90,000/90,000/2。允许有界真实验收，不代表LIVE通过。
