"""F-B RED 变异体生成器 (第 2 轮): 从修复后的 run_n01.py 反向注入第 1 轮的两项真实缺陷。

RED-STUB-1  窗端元数据缺失: close() 不写 end/load_end, fields() 不拒绝缺失窗端而返回空串
            (逐字复刻归档 `evidence/R2/n01-paired/window.log` 的 `end=`/`load_end=` 空值与
            `n01-paired-summary.json` 的 `"utc_window_start": ""`)。
RED-STUB-2  warm 臂 provider 绝对路径逐迭代变化: 每迭代用自己的 BIRD_N01_OUT
            (复刻 `warm-00/warmroot` … `warm-09/warmroot` 的归档形状)。

用法: python3 make_fb_red_stub.py <fixed_run_n01.py> <out_stub.py>
"""
from __future__ import annotations

import sys
from pathlib import Path

MUTATIONS = [
    ("""        self.end = load_snapshot()
        self._flush()""",
     """        # RED-STUB-1: 复刻第 1 轮缺陷 —— close() 不写 end/load_end
        self._flush()"""),
    ("""    def fields(self) -> dict:
        if self.start is None or self.end is None:
            raise RuntimeError("window metadata incomplete: open()/close() required")""",
     """    def fields(self) -> dict:
        # RED-STUB-1b: 复刻第 1 轮缺陷 —— 窗端不抛错也不落值, 一律空串/空列表
        if self.start is None:
            self.start = {"utc": "", "getloadavg": [], "sysctl_vm_loadavg": ""}
        if self.end is None:
            self.end = {"utc": "", "getloadavg": [], "sysctl_vm_loadavg": ""}"""),
    ("""                "utc_window_start": self.start["utc"], "utc_window_end": self.end["utc"],""",
     """                "utc_window_start": "", "utc_window_end": "",  # RED-STUB-1c"""),
    ("""            warmroot = probe_shared / "warmroot"
            if arm == "warm" and warmroot.exists():
                shutil.rmtree(warmroot)
            env = base_env(ws, {"BIRD_N01_OUT": str(probe_shared), "BIRD_N01_MODE": arm})""",
     """            env = base_env(ws, {"BIRD_N01_OUT": str(slot), "BIRD_N01_MODE": arm})  # RED-STUB-2"""),
    ("""            iter_json = probe_shared / "iter-001.json"
            moved = slot / "iter-001.json\"""",
     """            iter_json = slot / "iter-001.json"          # RED-STUB-2b
            moved = slot / "iter-retained.json\""""),
]


def build(src: Path, dst: Path) -> int:
    text = src.read_text()
    applied = 0
    for old, new in MUTATIONS:
        assert old in text, f"mutation anchor missing: {old[:60]!r}"
        text = text.replace(old, new, 1)
        applied += 1
    assert "RED-STUB-1" in text and "RED-STUB-2" in text
    dst.write_text(text)
    return applied


if __name__ == "__main__":
    n = build(Path(sys.argv[1]), Path(sys.argv[2]))
    print(f"red stub built: {sys.argv[2]} mutations={n}")
