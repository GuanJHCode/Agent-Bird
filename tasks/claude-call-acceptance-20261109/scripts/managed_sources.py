"""Claude managed-source absence gate; raw defaults output stays in memory.

Paths/domains mirror host/claude.go checkClaudeManagedSources. Injection exists
solely for pure fixtures; production callers use check_managed_sources().
"""
import os
import subprocess

MANAGED_PATHS = (
    '/Library/Application Support/ClaudeCode/managed-settings.json',
    '/Library/Application Support/ClaudeCode/managed-mcp.json',
    '/Library/Application Support/ClaudeCode/managed-settings.d',
    '/etc/claude-code/managed-settings.json',
    '/etc/claude-code/managed-mcp.json',
    '/Library/Managed Preferences/com.anthropic.claudecode.plist',
    '/Library/Managed Preferences/com.anthropic.ClaudeCode.plist',
)
MANAGED_DOMAINS = ('com.anthropic.claudecode', 'com.anthropic.ClaudeCode')
FAILURE = 'claude_gateway_managed_config_unverified'


class ManagedSourcesUnverified(RuntimeError):
    pass


def _defaults_read(domain):
    result = subprocess.run(
        ['/usr/bin/defaults', 'read', domain],
        env={'PATH': '/usr/bin:/bin', 'LANG': 'en_US.UTF-8'},
        stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=2, check=False,
    )
    return result.returncode, result.stdout, result.stderr


def check_managed_sources(*, lstat=os.lstat, defaults_read=_defaults_read):
    for path in MANAGED_PATHS:
        try:
            lstat(path)
        except FileNotFoundError:
            continue
        except Exception:
            raise ManagedSourcesUnverified(FAILURE) from None
        raise ManagedSourcesUnverified(FAILURE)
    for domain in MANAGED_DOMAINS:
        try:
            code, stdout, stderr = defaults_read(domain)
            absent = (type(code) is int and code != 0
                      and isinstance(stdout, bytes) and isinstance(stderr, bytes)
                      and b'does not exist' in stdout + stderr)
        except Exception:
            raise ManagedSourcesUnverified(FAILURE) from None
        if not absent:
            raise ManagedSourcesUnverified(FAILURE)
