"""第 2 轮联合身份集重算与差分解释 (F-A step 4, D3「按断言/行为矩阵合并, 不按名称删除)。

计数一律由 go test -json 日志得出, 不预填目标数。三个输入路径全部参数化 (argv, 无默认
硬编码工作区):
  python3 identity_diff_round2.py <root-gate-identity-counts.json> <J1.jsonl> <J2.jsonl>

差分格: union(A,B)\\J2 必须空 (不削减旧必测); J2\\J1 = 第 2 轮新增; J1\\J2 必须空;
A\\J2 / B\\J2 的非空项必须逐条落进 opt-in 分账, 否则记 violation 并以 rc=1 拒绝。
"""
from __future__ import annotations

import json
import sys
from collections import Counter
from pathlib import Path

# 终态 action: 每个 (package, test) 身份取最后一次终态, 与 root 口径一致 (含子测试)。
TERMINAL = ("pass", "fail", "skip")


def parse_jsonl(path: Path):
    """返回 (unique 身份集合, 终态计数, 首尾时间, 重复终态告警)。"""
    final = {}
    order = []
    times = []
    dup = []
    for line in path.read_text().splitlines():
        try:
            e = json.loads(line)
        except json.JSONDecodeError:
            continue
        if e.get("Time"):
            times.append(e["Time"])
        test = e.get("Test")
        action = e.get("Action")
        if not test or action not in TERMINAL:
            continue
        key = (e.get("Package", ""), test)
        if key in final:
            dup.append(key)
        if key not in final:
            order.append(key)
        final[key] = action
    counts = Counter(final[k] for k in order)
    return (set(final), dict(counts), len(order),
            (times[0] if times else None, times[-1] if times else None), dup)


def describe(a: set, b: set) -> list:
    return sorted(f"{pkg} {name}" for pkg, name in (a - b))


def main(argv=None):
    argv = sys.argv if argv is None else argv
    if len(argv) != 4:
        print("usage: identity_diff_round2.py <root-gate-identity-counts.json> "
              "<J1.jsonl> <J2.jsonl>", file=sys.stderr)
        return 2
    counts_path, j1_path, j2_path = (Path(a) for a in argv[1:4])
    for p in (counts_path, j1_path, j2_path):
        if not p.exists():
            print(f"MISSING INPUT: {p}", file=sys.stderr)
            return 2
    root_counts = json.loads(counts_path.read_text())
    A = {tuple(x) for x in root_counts["primaryIdentities"]}
    B = {tuple(x) for x in root_counts["secondIdentities"]}
    J1, c1, n1, t1, d1 = parse_jsonl(j1_path)
    J2, c2, n2, t2, d2 = parse_jsonl(j2_path)
    U = A | B

    out = {
        "inputs": {"A_authoritative": len(A), "B_authoritative": len(B),
                   "J1_log": str(j1_path), "J2_log": str(j2_path)},
        "counts": {
            "J1": {"uniqueRun": n1, **c1, "first": t1[0], "last": t1[1],
                   "duplicate_terminal": len(d1)},
            "J2": {"uniqueRun": n2, **c2, "first": t2[0], "last": t2[1],
                   "duplicate_terminal": len(d2)},
        },
        "checks": {
            "unionAB_minus_J2": describe(U, J2),
            "J2_minus_A": describe(J2, A),
            "J2_minus_B": describe(J2, B),
            "A_minus_J2": describe(A, J2),
            "B_minus_J2": describe(B, J2),
            "J2_minus_J1": describe(J2, J1),
            "J1_minus_J2": describe(J1, J2),
        },
    }
    explanation = {
        "unionAB_minus_J2": "必须恰等于 opt-in 诊断登记表 (两个 A 实验身份); 出现第三个即为漏件。"
                            " 必测集合按 mandatory_expected/mandatory_missing 单独核算。",
        "J2_minus_A": "联合新增: R3 组合判据 TestN04PipeSensitiveDescendantHoldingStdoutIsNecessary "
                      "+ B 独有必测三件 (TestEscapedPipeHolderProgressDoesNotAdvanceAfterTerminal / "
                      "TestPipeHoldingDescendantRecordsSingleStoppingProgress / store "
                      "TestHostProgressRejectsUnknownSegmentAndEnvelopeBranches)。",
        "J2_minus_B": "联合新增: A 的必测 TestProgressStaysBoundedWhileDescendantHoldsPipeAndNeverFinalizes "
                      "(第 2 轮 F-A 并入) 与上述 R3 组合判据。",
        "A_minus_J2": "非空即违规: 这些 A 身份未进第 2 轮默认集, 必须逐条给出分账登记理由。",
        "B_minus_J2": "非空即违规: 同上, 针对 B 身份。",
        "J2_minus_J1": "第 2 轮相对第 1 轮联合集的新增 (应为 F-A 并入的 A 必测)。",
        "J1_minus_J2": "第 2 轮相对第 1 轮联合集的消失 (必须为空, 否则说明削减了已交付身份)。",
    }
    out["cell_explanations"] = explanation
    # opt-in 诊断登记表 (ROOT-INFERRED, 逐条抄自 R6-status.md §0 三列分账与 A/B 归档的
    # experiments-only 文件): 这两个身份是"测量面"而非"验收身份", 第 1 轮起被移出默认身份集,
    # 因此允许不出现在 J2; 其余 union(A,B) 身份必须在集。
    opt_in_registered = {
        ("codex-cli-orchestration-design/tools/orchestrator/internal/adapter",
         "TestProbeCauseDecompositionObservation"),
        ("codex-cli-orchestration-design/tools/orchestrator/internal/host",
         "TestS4StageTimingObservation"),
    }
    registered_only_primary = {tuple(x) for x in root_counts["differences"]["onlyPrimary"]}
    registered_only_second = {tuple(x) for x in root_counts["differences"]["onlySecond"]}
    mandatory = (A | B) - opt_in_registered
    out["accounting"] = {
        "opt_in_diagnostics_registered": sorted(f"{p} {n}" for p, n in opt_in_registered),
        "opt_in_present_in_default_set_anyway": sorted(
            f"{p} {n}" for p, n in opt_in_registered if (p, n) in J2),
        "opt_in_absent_from_default_set": sorted(
            f"{p} {n}" for p, n in opt_in_registered if (p, n) not in J2),
        "A_only_identities_present_in_J2": sorted(
            f"{p} {n}" for p, n in registered_only_primary if (p, n) in J2),
        "B_only_identities_present_in_J2": sorted(
            f"{p} {n}" for p, n in registered_only_second if (p, n) in J2),
        "B_only_identities_absent": sorted(
            f"{p} {n}" for p, n in registered_only_second if (p, n) not in J2),
        "mandatory_expected": len(mandatory),
        "mandatory_missing": sorted(f"{p} {n}" for p, n in mandatory - J2),
    }
    violations = []
    if out["accounting"]["mandatory_missing"]:
        violations.append({"violation": "union(A,B) 必测身份未进第 2 轮默认集 (opt-in 之外)",
                           "identities": out["accounting"]["mandatory_missing"]})
    if out["checks"]["J1_minus_J2"]:
        violations.append({"violation": "第 2 轮削减了第 1 轮已交付身份",
                           "identities": out["checks"]["J1_minus_J2"]})
    for item in out["checks"]["A_minus_J2"] + out["checks"]["B_minus_J2"]:
        pkg, _, name = item.partition(" ")
        if (pkg, name) not in opt_in_registered:
            violations.append({"violation": "身份缺失且未登记为 opt-in 诊断", "identity": item})
    if n2 != sum(c2.values()):
        violations.append({"violation": "uniqueRun != pass+fail+skip",
                           "uniqueRun": n2, "counts": c2})
    if d2:
        violations.append({"violation": "同一身份出现多个终态 (运行多重集不干净)",
                           "duplicates": [f"{p} {n}" for p, n in d2]})
    out["violations"] = violations
    print(json.dumps(out, indent=1, ensure_ascii=False))
    return 0 if not violations else 1


if __name__ == "__main__":
    sys.exit(main())
