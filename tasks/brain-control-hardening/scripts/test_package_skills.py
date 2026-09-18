"""Execute the packager; fixtures do not establish native runtime acceptance."""
import json
from pathlib import Path
import shutil
import subprocess
import sys

import pytest


REPO = Path(__file__).resolve().parents[3]
SCRIPT = Path("tasks/g1-g4-delivery/scripts/package-plugin.sh")
PLUGIN = Path("plugins/codex-orchestrator")


def package(root, out):
    return subprocess.run(
        ["/bin/sh", str(root / SCRIPT), "--binary", "/bin/echo",
         "--python", str(Path(sys.executable).resolve()), "--version", "0.1.0-skill-test",
         "--out", str(out)], capture_output=True, text=True, timeout=30,
    )


@pytest.mark.parametrize("fault", ["missing-directory", "empty-directory", "traversal", "symlink-directory", "symlink-file"])
def test_package_rejects_unusable_declared_skills(tmp_path, fault):
    root = tmp_path / "repo"
    (root / SCRIPT).parent.mkdir(parents=True)
    shutil.copy2(REPO / SCRIPT, root / SCRIPT)
    shutil.copytree(REPO / PLUGIN, root / PLUGIN)
    skill_root = root / PLUGIN / "skills"
    skill = skill_root / "orchestrate/SKILL.md"
    manifest = root / PLUGIN / ".codex-plugin/plugin.json"
    if fault == "missing-directory":
        shutil.rmtree(skill_root)
    elif fault == "empty-directory":
        skill.unlink()
    elif fault == "traversal":
        value = json.loads(manifest.read_text())
        value["skills"] = "../../outside"
        manifest.write_text(json.dumps(value))
    elif fault == "symlink-directory":
        shutil.move(skill_root, root / "outside")
        skill_root.symlink_to(root / "outside", target_is_directory=True)
    elif fault == "symlink-file":
        shutil.move(skill, root / "outside.md")
        skill.symlink_to(root / "outside.md")
    result = package(root, tmp_path / "package")
    assert result.returncode != 0
    assert "plugin_skills_invalid" in result.stderr, result.stderr
    assert not (tmp_path / "package/package-manifest.json").exists()


def test_package_contains_declared_skill_in_hash_manifest(tmp_path):
    out = tmp_path / "package"
    result = package(REPO, out)
    assert result.returncode == 0, result.stderr
    manifest = json.loads((out / "package-manifest.json").read_text())
    paths = {item["path"] for item in manifest["files"]}
    assert "plugin/skills/orchestrate/SKILL.md" in paths
    assert (out / "plugin/skills/orchestrate/SKILL.md").read_text().startswith("---\n")
