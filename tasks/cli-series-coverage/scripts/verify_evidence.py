#!/usr/bin/env python3
"""Check public series evidence consistency without credentials or model calls."""

import argparse
import hashlib
import json
from pathlib import Path


def verify(repo, report_path=None):
    docs = repo / "tasks/cli-series-coverage/docs"
    report = json.loads((report_path or docs / "version-series-verification.json").read_text())
    catalog_path = repo / "tools/orchestrator/internal/adapter/compatibility_catalog.json"
    catalog = json.loads(catalog_path.read_text())
    assert report["catalog_sha256"] == hashlib.sha256(catalog_path.read_bytes()).hexdigest()
    expected = {(p, v["version"]) for p, c in catalog["providers"].items() for v in c["versions"]}
    rows = {(r["provider"], r["version"]): r for r in report["versions"]}
    assert len(rows) == len(report["versions"]) and set(rows) == expected
    real = json.loads((docs / "real-series-evidence.json").read_text())
    accepted = {(r["provider"], r["version"]): r for r in real["accepted"]}
    assert len(accepted) == len(real["accepted"])
    for key, row in rows.items():
        if row["no_model"] == "passed":
            assert row["raw_version"] and len(row["sha256"]) == 64 and row["source"]
        if row["real_model"] == "passed":
            evidence = accepted[key]
            assert evidence == row["real_evidence"]
            assert evidence["status"] == "passed" and evidence["package"] == real["package"]
            assert evidence["binary_sha256"] == row["sha256"]
            artifact = evidence["artifact"]
            assert all(artifact[k] for k in ("exact_match", "size_matches", "digest_matches"))
            assert artifact["observed_size"] == artifact["declared_size"]
            assert artifact["observed_sha256"] == artifact["declared_sha256"]
            assert evidence["collect"] == "passed" and evidence["ack"] == "acknowledged"
            assert evidence["accept"] == "accepted" and evidence["final_status"] == "completed"
        else:
            assert key not in accepted
    for provider, summary in report["summary"].items():
        members = [r for r in rows.values() if r["provider"] == provider]
        assert len(members) == summary["catalog"]
        for category in ("no_model", "real_model"):
            for state in ("passed", "not_tested"):
                assert summary[category + "_" + state] == sum(r[category] == state for r in members)
    for cohort in report["cohorts"]:
        members = [r for r in rows.values() if r.get("cohort") == cohort["id"]]
        assert len(members) == cohort["published_count"]
        assert set(cohort["direct_real_versions"]) == {r["version"] for r in members if r["real_model"] == "passed"}
        assert set(cohort["inferred_versions"]) == {r["version"] for r in members if r["real_model"] != "passed"}
        assert cohort["direct_real_versions"]
    return {"catalog": len(rows), "direct_real_model": len(accepted), "cohorts": len(report["cohorts"])}


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--repo", type=Path, default=Path(__file__).resolve().parents[3])
    parser.add_argument("--report", type=Path)
    args = parser.parse_args()
    print(json.dumps(verify(args.repo, args.report)))
