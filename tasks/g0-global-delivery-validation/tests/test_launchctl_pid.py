from pathlib import Path
import sys
import pytest
ROOT=Path(__file__).resolve().parents[3]
sys.path.insert(0,str(ROOT/'tasks/g0-auth-preserving-activation/scripts'))
from activation_transaction import SubprocessLaunchctl,UnknownStateError


def output(lines):
    return 'org.codex.orchestration.proxy = {\n path = /controlled/job.plist\n arguments = {\n /controlled/python\n }\n'+lines+'\n}'


def test_launchctl_preserves_only_top_level_pid():
    result=SubprocessLaunchctl._parse_text(output(' pid = 321\n nested = {\n pid = 999\n token = secret\n }'))
    assert result['pid']==321
    assert 'secret' not in str(result)


@pytest.mark.parametrize('lines',[' pid = 0',' pid = -1',' pid = 3.0',' pid = abc',' pid = 321\n pid = 321',' pid = 321\n pid = 999'])
def test_launchctl_rejects_invalid_or_duplicate_pid(lines):
    with pytest.raises(UnknownStateError):SubprocessLaunchctl._parse_text(output(lines))


def test_nested_pid_cannot_supply_job_identity():
    assert 'pid' not in SubprocessLaunchctl._parse_text(output(' nested = {\n pid = 999\n }'))
