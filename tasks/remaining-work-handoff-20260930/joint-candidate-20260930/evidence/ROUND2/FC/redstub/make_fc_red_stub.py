"""F-C RED 变异体生成器 (第 2 轮): 从修复后的 n03_s1_s3_storm.py 反向注入第 1 轮形态。

RED-STUB-1  窗台账退化为第 1 轮的「只有 UTC start/end」: 不记 sysctl vm.loadavg 快照、
            不记自建 storm 真实状态、不记窗间间隔、不记 duration。
RED-STUB-2  报告缺少实验 ID / 外层窗序列标签 / 解析后的路径登记。
RED-STUB-3  路径解析退回第 1 轮的模块期硬编码派生工作树 (argv/env 一律忽略)。
RED-STUB-4  main 只接受单个 argv (第 1 轮签名), 参数化入口消失。

用法: python3 make_fc_red_stub.py <fixed_n03.py> <out_stub.py>
"""
from __future__ import annotations

import sys
from pathlib import Path

MUTATIONS = [
    # RED-STUB-1
    ("""        snap = load_snapshot()
        gap = None if self.prev_end is None else _delta_s(self.prev_end, snap["utc"])
        self.bounds[name] = {"start": snap["utc"], "load_start": snap["getloadavg"],
                             "sysctl_loadavg_start": snap["sysctl_vm_loadavg"],
                             "self_storm_active": bool(self_storm),
                             "gap_since_prev_window_end_s": gap, "end": None}""",
     """        self.bounds[name] = {"start": utc()}  # RED-STUB-1"""),
    ("""        rec = self.bounds[name]
        snap = load_snapshot()
        rec["end"] = snap["utc"]
        rec["load_end"] = snap["getloadavg"]
        rec["sysctl_loadavg_end"] = snap["sysctl_vm_loadavg"]
        rec["duration_s"] = _delta_s(rec["start"], snap["utc"])
        self.prev_end = snap["utc"]""",
     """        self.bounds[name]["end"] = utc()  # RED-STUB-1b"""),
    # RED-STUB-2
    ("""           "experiment_id": experiment_id,
           "outer_window_label": "no-self-storm",""",
     """           "experiment_id": "",  # RED-STUB-2
           "outer_window_label": None,"""),
    ("""           "resolved": {"source_root": str(ws), "go": str(go),""",
     """           "resolved": None,  # RED-STUB-2b
           "_resolved_unused": {"source_root": str(ws), "go": str(go),"""),
    # RED-STUB-3
    ("""    env = os.environ if env is None else env
    argv = sys.argv if argv is None else argv
    if len(argv) > 2:
        return Path(argv[2])
    if env.get("BIRD_N03_WS"):
        return Path(env["BIRD_N03_WS"])
    derived = REPO / REL_WS
    if not derived.exists():
        raise SystemExit(
            f"derived default source tree does not exist: {derived}; "
            "pass it as argv[2] or set BIRD_N03_WS")
    return derived""",
     """    # RED-STUB-3: 退回第 1 轮的模块期派生工作树, argv/env 一律忽略, 存在性也不核对。
    return REPO / REL_WS"""),
    # RED-STUB-4
    ("""    if len(sys.argv) < 2 or len(sys.argv) > 4:""",
     """    if len(sys.argv) < 2:  # RED-STUB-4: 多余位置参数被忽略 (第 1 轮无参数化入口)"""),
]


def build(src: Path, dst: Path) -> int:
    text = src.read_text()
    applied = 0
    for old, new in MUTATIONS:
        assert old in text, f"mutation anchor missing: {old[:70]!r}"
        text = text.replace(old, new, 1)
        applied += 1
    assert "RED-STUB-1" in text and "RED-STUB-3" in text
    dst.write_text(text)
    return applied


if __name__ == "__main__":
    count = build(Path(sys.argv[1]), Path(sys.argv[2]))
    print(f"red stub built: {sys.argv[2]} mutations={count}")
