#!/usr/bin/env python3
"""Check exact native series evidence without credentials or model requests."""

import argparse
import hashlib
import json
from pathlib import Path
import re


def verify(repo, report_path=None):
    report_path = report_path or repo / "tasks/codex-agy-series/docs/verification.json"
    report = json.loads(report_path.read_text())
    catalog_path = repo / "tools/orchestrator/internal/adapter/compatibility_catalog.json"
    catalog = json.loads(catalog_path.read_text())
    assert report["catalog_sha256"] == hashlib.sha256(catalog_path.read_bytes()).hexdigest()
    providers = ("codex-cli", "antigravity-cli")
    expected = {(p, v["version"]) for p in providers for v in catalog["providers"][p]["versions"]}
    rows = {(r["provider"], r["version"]): r for r in report["versions"]}
    assert len(rows) == len(report["versions"]) and set(rows) == expected
    digest = lambda value: isinstance(value, str) and re.fullmatch(r"[a-f0-9]{64}", value)
    admitted = []
    accepted = []
    for key, row in rows.items():
        assert row["metadata"]["status"] == "passed"
        assert row["raw_version"] == ("codex-cli " if key[0] == "codex-cli" else "") + key[1]
        assert digest(row["binary_sha256"]) and row["source"].startswith("https://github.com/")
        assert row["metadata"]["version_exit"] == row["metadata"]["help_exit"] == 0
        assert row["metadata"]["required_flags_verified"] is True
        assert row["model"]["status"] in ("passed", "not_tested", "blocked", "failed")
        if key[0] == "codex-cli":
            assert digest(row["companion_sha256"])
            assert row["native_contracts"]["status"] in ("passed", "failed", "not_tested")
            if row["native_contracts"]["status"] == "passed":
                assert row["native_contracts"]["tests_passed"] == 6
                assert row["native_contracts"]["tests_skipped"] == 0
                assert digest(row["native_contracts"]["log_sha256"])
            if row["production_admitted"]:
                assert row["native_contracts"]["status"] == "passed"
                assert row["model"]["status"] == "passed"
                admitted.append(key)
        if row["model"]["status"] != "passed":
            continue
        evidence = row["model"]["evidence"]
        assert evidence["binary_sha256"] == row["binary_sha256"]
        assert evidence["package"] == report["package"]["version"]
        assert evidence["orchestrator_sha256"] == report["package"]["binary_sha256"]
        assert evidence["installed_package"] and evidence["pin_bypass"] is False
        assert evidence["collect"] == "passed" and evidence["ack"] == "acknowledged"
        assert evidence["accept"] == "accepted" and evidence["final_status"] == "completed"
        artifact = evidence["artifact"]
        assert all(artifact[k] for k in ("exact_match", "size_matches", "digest_matches"))
        assert artifact["observed_size"] == artifact["declared_size"] > 0
        assert artifact["observed_sha256"] == artifact["declared_sha256"]
        assert digest(artifact["observed_sha256"]) and digest(evidence["round_sha256"])
        accepted.append(key)
    assert report["summary"]["metadata_passed"] == len(rows)
    assert report["summary"]["current_package_direct_model_passed"] == len(accepted)
    assert report["summary"]["codex_exact_production_pins"] == len(admitted)
    for group in report["agy_cohorts"]:
        members = [rows[("antigravity-cli", version)] for version in group["versions"]]
        direct = {r["version"] for r in members if r["model"]["status"] == "passed"}
        assert direct == set(group["direct_model_versions"])
        assert set(group["inferred_versions"]) == set(group["versions"]) - direct
        assert group["inference_status"] == ("supported" if set(group["required_anchors"]) <= direct else "not_established")
    return {"metadata": len(rows), "direct_model": len(accepted), "exact_codex_pins": len(admitted)}


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--repo", type=Path, default=Path(__file__).resolve().parents[3])
    parser.add_argument("--report", type=Path)
    args = parser.parse_args()
    print(json.dumps(verify(args.repo, args.report)))
