from __future__ import annotations

import sys
from pathlib import Path
from urllib.parse import urlsplit

import pytest


ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / "plugins" / "codex-orchestrator" / "lib"))

from trial.auth_guard import (  # noqa: E402
    AuthGuardBlocked,
    MetadataObservation,
    extract_safe_auth_metadata,
    prepare_guarded_environment,
    project_login_status,
)


class FixedMetadataSource:
    def __init__(self, observation: MetadataObservation) -> None:
        self._observation = observation

    def read_verified_nonsecret_metadata(self) -> MetadataObservation:
        return self._observation


def observation(**changes: object) -> MetadataObservation:
    values: dict[str, object] = {
        "native_version": "0.154.0",
        "native_sha256": (
            "4f85982624b3898c8991cb80c0981b2aa71070e3537046c9a95950318a95afcc"
        ),
        "auth_kind": "managed_chatgpt",
        "bootstrap_auth_kind": "managed_chatgpt",
        "credential_store": "file",
        "external_auth_configured": False,
        "use_agent_identity": False,
        "source_kind": "fixed_native_internal_sdk",
        "proof_sha256": "a" * 64,
    }
    values.update(changes)
    return MetadataObservation(**values)  # type: ignore[arg-type]


def verified_metadata(**changes: object):
    return extract_safe_auth_metadata(FixedMetadataSource(observation(**changes)))


def test_prepare_adds_only_non_http_refresh_tripwire_to_child_copy() -> None:
    source_env = {
        "HOME": "/fake/home",
        "CODEX_HOME": "/fake/home/.codex",
        "OPENAI_API_KEY": "fake-test-value",
        "HTTPS_PROXY": "http://proxy.invalid",
    }
    original = dict(source_env)

    child_env = prepare_guarded_environment(source_env, verified_metadata())

    assert source_env == original
    assert child_env == {
        **original,
        "CODEX_REFRESH_TOKEN_URL_OVERRIDE": "invalid://refresh-blocked",
    }
    assert urlsplit(child_env["CODEX_REFRESH_TOKEN_URL_OVERRIDE"]).scheme not in {
        "http",
        "https",
    }


def test_prepare_blocks_existing_refresh_override_without_overwriting_it() -> None:
    source_env = {"CODEX_REFRESH_TOKEN_URL_OVERRIDE": "https://existing.invalid/token"}

    with pytest.raises(AuthGuardBlocked, match="^refresh_override_already_present$"):
        prepare_guarded_environment(source_env, verified_metadata())

    assert source_env == {
        "CODEX_REFRESH_TOKEN_URL_OVERRIDE": "https://existing.invalid/token"
    }


@pytest.mark.parametrize(
    ("auth_kind", "expected_code"),
    [
        ("unknown", "auth_kind_unknown"),
        ("workload_identity", "auth_kind_workload_identity"),
        ("command_external", "auth_kind_command_external"),
        ("external_chatgpt_tokens", "auth_kind_external_chatgpt_tokens"),
    ],
)
def test_prepare_blocks_unsupported_auth_sources(
    auth_kind: str, expected_code: str
) -> None:
    with pytest.raises(AuthGuardBlocked, match=f"^{expected_code}$"):
        prepare_guarded_environment({}, verified_metadata(auth_kind=auth_kind))


def test_prepare_blocks_effective_agent_identity_even_with_supported_auth() -> None:
    with pytest.raises(AuthGuardBlocked, match="^use_agent_identity_enabled$"):
        prepare_guarded_environment({}, verified_metadata(use_agent_identity=True))


def test_prepare_blocks_store_category_outside_supported_auth_contract() -> None:
    with pytest.raises(AuthGuardBlocked, match="^credential_store_unsupported$"):
        prepare_guarded_environment(
            {}, verified_metadata(credential_store="unverified-store")
        )


def test_prepare_accepts_verified_api_key_without_changing_its_source() -> None:
    source_env = {"CODEX_API_KEY": "fake-api-key"}

    child_env = prepare_guarded_environment(
        source_env,
        verified_metadata(auth_kind="api_key", credential_store="environment"),
    )

    assert source_env == {"CODEX_API_KEY": "fake-api-key"}
    assert child_env == {
        "CODEX_API_KEY": "fake-api-key",
        "CODEX_REFRESH_TOKEN_URL_OVERRIDE": "invalid://refresh-blocked",
    }


@pytest.mark.parametrize(
    ("bootstrap_auth_kind", "expected_code"),
    [
        ("", "bootstrap_auth_kind_missing"),
        ("unknown", "bootstrap_auth_kind_unknown"),
        ("workload_identity", "bootstrap_auth_kind_workload_identity"),
        ("command_external", "bootstrap_auth_kind_command_external"),
    ],
)
def test_prepare_blocks_unsupported_bootstrap_auth_even_for_final_api_key(
    bootstrap_auth_kind: str, expected_code: str
) -> None:
    metadata = verified_metadata(
        auth_kind="api_key",
        bootstrap_auth_kind=bootstrap_auth_kind,
        credential_store="environment",
    )

    with pytest.raises(AuthGuardBlocked, match=f"^{expected_code}$"):
        prepare_guarded_environment({}, metadata)


def test_prepare_blocks_any_configured_external_auth_path() -> None:
    with pytest.raises(AuthGuardBlocked, match="^external_auth_configured$"):
        prepare_guarded_environment(
            {}, verified_metadata(external_auth_configured=True)
        )


def test_prepare_does_not_echo_unknown_auth_kind_in_block_reason() -> None:
    with pytest.raises(AuthGuardBlocked, match="^auth_kind_unknown$"):
        prepare_guarded_environment(
            {}, verified_metadata(auth_kind="secret-shaped-untrusted-value")
        )


@pytest.mark.parametrize(
    ("field", "value", "expected_code"),
    [
        ("native_version", "0.155.0", "native_version_mismatch"),
        ("native_sha256", "b" * 64, "native_sha256_mismatch"),
        ("proof_sha256", "", "metadata_proof_missing"),
        ("source_kind", "worker_report", "metadata_source_untrusted"),
    ],
)
def test_metadata_extraction_blocks_unverified_or_wrong_native_evidence(
    field: str, value: object, expected_code: str
) -> None:
    with pytest.raises(AuthGuardBlocked, match=f"^{expected_code}$"):
        extract_safe_auth_metadata(
            FixedMetadataSource(observation(**{field: value}))
        )


def test_metadata_source_failure_discards_original_exception_detail() -> None:
    class FailingSource:
        def read_verified_nonsecret_metadata(self) -> MetadataObservation:
            raise RuntimeError("secret-shaped-source-error")

    with pytest.raises(AuthGuardBlocked, match="^metadata_source_failed$") as error:
        extract_safe_auth_metadata(FailingSource())

    assert error.value.__cause__ is None


def test_metadata_extraction_blocks_malformed_nonsecret_projection() -> None:
    with pytest.raises(AuthGuardBlocked, match="^metadata_source_invalid$"):
        extract_safe_auth_metadata(
            FixedMetadataSource(observation(proof_sha256=1234))  # type: ignore[arg-type]
        )


def test_prepare_rejects_self_reported_mapping_instead_of_treating_it_as_proof() -> None:
    with pytest.raises(AuthGuardBlocked, match="^metadata_not_verified$"):
        prepare_guarded_environment(  # type: ignore[arg-type]
            {},
            {
                "auth_kind": "managed_chatgpt",
                "native_version": "0.154.0",
                "native_sha256": (
                    "4f85982624b3898c8991cb80c0981b2aa71070e3537046c9a95950318a95afcc"
                ),
            },
        )


@pytest.mark.parametrize(
    ("stderr", "expected"),
    [
        (b"Logged in using ChatGPT\n", "managed_chatgpt"),
        (b"Logged in using an API key - sk-fake-***value\n", "api_key"),
        (b"Logged in using an API key - ***\n", "api_key"),
        (b"Logged in using workload identity\n", "workload_identity"),
    ],
)
def test_login_status_projection_returns_only_nonsecret_auth_kind(
    stderr: bytes, expected: str
) -> None:
    assert project_login_status(0, stderr) == expected


def test_login_status_projection_rejects_extra_or_failed_output() -> None:
    assert project_login_status(0, b"warning\nLogged in using ChatGPT\n") == "unknown"
    assert project_login_status(1, b"Logged in using ChatGPT\n") == "unknown"
