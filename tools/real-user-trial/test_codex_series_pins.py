"""Cross-language exact-pin contract for the frozen Codex native series."""

from __future__ import annotations

from pathlib import Path
import re
import sys


ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / "plugins" / "codex-orchestrator" / "lib"))

from trial import auth_guard, codex_exec  # noqa: E402


FROZEN_CODEX_VERSIONS = (
    "0.154.0",
    "0.155.0",
    "0.155.1",
    "0.156.0",
    "0.156.1",
    "0.157.0",
    "0.157.1",
    "0.158.0",
    "0.159.0",
    "0.159.1",
    "0.159.2",
    "0.159.3",
    "0.160.0",
    "0.160.1",
    "0.161.0",
    "0.162.0",
    "0.162.1",
    "0.163.0",
    "0.164.0",
    "0.165.0",
    "0.166.0",
)
SHA256 = re.compile(r"^[0-9a-f]{64}$")


def test_python_native_maps_exactly_cover_frozen_series() -> None:
    expected = set(FROZEN_CODEX_VERSIONS)
    assert set(auth_guard.NATIVE_ADMISSIONS) == expected
    assert set(codex_exec.NATIVE_COMPANIONS) == expected
    assert all(
        SHA256.fullmatch(digest) for digest in auth_guard.NATIVE_ADMISSIONS.values()
    )
    assert all(
        SHA256.fullmatch(digest) for digest in codex_exec.NATIVE_COMPANIONS.values()
    )


def test_go_literal_native_pin_map_matches_python_admissions() -> None:
    source = (
        ROOT / "tools" / "orchestrator" / "internal" / "adapter" / "admit.go"
    ).read_text(encoding="utf-8")
    function = re.search(
        r"func\s+CodexNativePins\(\)\s+map\[string\]string\s*\{\s*"
        r"return\s+map\[string\]string\s*\{(?P<body>.*?)\}\s*\}",
        source,
        flags=re.DOTALL,
    )
    assert function is not None, "CodexNativePins must return one explicit literal map"
    body = function.group("body")
    entry = re.compile(
        r'"(?P<raw>codex-cli\s+\d+\.\d+\.\d+)"\s*:\s*'
        r'"(?P<sha>[0-9a-f]{64})"\s*,?'
    )
    pairs = [(match.group("raw"), match.group("sha")) for match in entry.finditer(body)]
    residue = entry.sub("", body).replace(",", "").strip()
    assert residue == "", f"CodexNativePins contains a non-literal entry: {residue!r}"
    go_pins = dict(pairs)
    assert len(go_pins) == len(pairs), "CodexNativePins contains duplicate raw versions"

    expected_raw = {f"codex-cli {version}" for version in FROZEN_CODEX_VERSIONS}
    assert set(go_pins) == expected_raw
    assert {
        raw.removeprefix("codex-cli "): digest for raw, digest in go_pins.items()
    } == auth_guard.NATIVE_ADMISSIONS
