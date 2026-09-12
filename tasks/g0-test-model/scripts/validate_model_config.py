#!/usr/bin/env python3
"""Static, offline validation of the native test model pin."""

from pathlib import Path
import tomllib


ROOT = Path(__file__).resolve().parents[2]
CONFIG = ROOT / "g0-macos/data/test-repo/.codex/config.toml"


def main() -> None:
    with CONFIG.open("rb") as handle:
        value = tomllib.load(handle)
    assert value["model"] == "gpt-5.6-luna"
    assert value["model_reasoning_effort"] == "medium"
    assert "astra" not in value["model"].lower()
    print("model=gpt-5.6-luna")
    print("model_reasoning_effort=medium")
    print("native_service_or_model_started=false")


if __name__ == "__main__":
    main()
