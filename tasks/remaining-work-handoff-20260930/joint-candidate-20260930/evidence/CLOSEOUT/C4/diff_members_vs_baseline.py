"""C4 — 最终冻结树源码包成员差异机械对账 (收尾集成轮, 2026-09-30).

输入:
  --final-manifest : build_source_archive.py 对最终树导出的 source-manifest.json (逐文件 SHA)
  --baseline-archive : B 的基线源码包 source.tar.gz (只读, 从 tar 流直接算 SHA, 不落盘)

输出 JSON: 每路径分类 added / removed / changed / identical, 并给出归属解释。
解释表来自本批交付事实 (F-A 并回 A 必测 / C1 消费改接 / R1 观察协议 helper 入树),
**凡无法归类的路径一律进 unexplained, 只要 unexplained 非空就 ok=false**。
"""

from __future__ import annotations

import argparse
import hashlib
import io
import json
from pathlib import Path
import tarfile

EXPLANATIONS = {
    "tools/orchestrator/internal/host/n04_pipe_sensitive_test.go":
        "R3 (第 1 轮返修) 新增的 N04 管道敏感组合判据; 相对 B 基线是新增成员, "
        "root 第 1/2 轮复核均已验收并钉入 REQUIRED",
    "tools/native-product-bridge/n02_owner_readiness_observer.py":
        "R1 观察协议 helper; 收尾集成轮把它同步进联合运行树供消费测试 import (此前只在 candidate/)",
    "tools/native-product-bridge/test_n02_owner_readiness_observer.py":
        "R1 观察协议负例套; 同上随 helper 入树",
    "tools/orchestrator/internal/host/stop_timing_test.go":
        "第 2 轮 F-A: 逐字并回 A 必测 TestProgressStaysBoundedWhileDescendantHoldsPipeAndNeverFinalizes, "
        "合并后 SHA 即 A 冻结 1322b337…",
    "tools/native-product-bridge/test_local_service_chain.py":
        "收尾集成 C1 (D1): owner-ready 5s 硬观察改接单一 readline 协议",
    "tasks/g0-completion/tests/test_owner_helper_production_pipeline.py":
        "收尾集成 C1 (D1): owner-ready 5s 硬观察改接单一 readline 协议 (两处调用点)",
}


def sha256_stream(stream: io.RawIOBase) -> str:
    digest = hashlib.sha256()
    for chunk in iter(lambda: stream.read(1 << 20), b""):
        digest.update(chunk)
    return digest.hexdigest()


def baseline_members(archive: Path):
    members = {}
    duplicates = []
    with tarfile.open(archive, "r:*") as tar:
        for info in tar.getmembers():
            if not info.isfile():
                continue
            stream = tar.extractfile(info)
            key = info.name
            if key in members:
                duplicates.append(key)
            members[key] = sha256_stream(stream)
    return members, sorted(duplicates)


def manifest_members(path: Path):
    data = json.loads(path.read_text())
    entries = {entry["path"]: entry["sha256"] for entry in data["files"]}
    if len(entries) != len(data["files"]):
        raise SystemExit(f"duplicate members in final manifest {path}")
    return entries, len(data["files"])


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--final-manifest", required=True, type=Path)
    parser.add_argument("--baseline-archive", required=True, type=Path)
    parser.add_argument("--out", required=True, type=Path)
    args = parser.parse_args()
    if args.out.exists():
        raise SystemExit(f"refusing to overwrite existing evidence: {args.out}")
    final, final_total = manifest_members(args.final_manifest)
    base, base_duplicates = baseline_members(args.baseline_archive)
    added = sorted(set(final) - set(base))
    removed = sorted(set(base) - set(final))
    changed = sorted(p for p in set(final) & set(base) if final[p] != base[p])
    identical = sorted(set(final) & set(base) - set(changed))
    unexplained = sorted(p for p in added + changed if p not in EXPLANATIONS) + \
        sorted(removed)  # removed 必须为空: 联合树不得丢 B 的任何一个源码成员
    result = {
        "final_manifest": str(args.final_manifest), "final_manifest_sha256":
        hashlib.sha256(args.final_manifest.read_bytes()).hexdigest(),
        "final_members": len(final), "final_total_field": final_total,
        "baseline_archive": str(args.baseline_archive), "baseline_archive_sha256":
        hashlib.sha256(args.baseline_archive.read_bytes()).hexdigest(),
        "baseline_members": len(base), "baseline_duplicates": base_duplicates,
        "counts": {"added": len(added), "removed": len(removed), "changed": len(changed),
                   "identical": len(identical)},
        "added": {p: {"sha256": final[p], "explanation": EXPLANATIONS.get(p, "")} for p in added},
        "changed": {p: {"baseline_sha256": base[p], "final_sha256": final[p],
                        "explanation": EXPLANATIONS.get(p, "")} for p in changed},
        "removed": removed,
        "unexplained": unexplained,
        "ok": not unexplained and not removed and not base_duplicates,
    }
    args.out.parent.mkdir(parents=True, exist_ok=True)
    args.out.write_text(json.dumps(result, indent=1, ensure_ascii=False))
    print(json.dumps({"ok": result["ok"], "counts": result["counts"],
                      "baseline_members": result["baseline_members"],
                      "final_members": result["final_members"],
                      "unexplained": unexplained, "out": str(args.out)}, ensure_ascii=False))
    return 0 if result["ok"] else 1


if __name__ == "__main__":
    raise SystemExit(main())
