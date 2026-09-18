from __future__ import annotations

import re
from dataclasses import dataclass
from typing import Mapping
from typing import Protocol


EXPECTED_NATIVE_VERSION = "0.154.0"
EXPECTED_NATIVE_SHA256 = (
    "4f85982624b3898c8991cb80c0981b2aa71070e3537046c9a95950318a95afcc"
)
REFRESH_OVERRIDE_ENV = "CODEX_REFRESH_TOKEN_URL_OVERRIDE"
REFRESH_TRIPWIRE = "invalid://refresh-blocked"

_SHA256_RE = re.compile(r"^[0-9a-f]{64}$")
_VERIFIED_METADATA_SEAL = object()
_TRUSTED_SOURCE_KINDS = frozenset(
    {
        "fixed_native_internal_sdk",
        "fixed_native_login_status_and_internal_config",
        "g0_owner_attachment_and_internal_sdk",
    }
)
_SUPPORTED_STORES = {
    "managed_chatgpt": frozenset({"file", "keyring", "auto"}),
    "api_key": frozenset({"file", "keyring", "auto", "environment"}),
}
_UNSUPPORTED_AUTH_CODES = {
    "unknown": "auth_kind_unknown",
    "workload_identity": "auth_kind_workload_identity",
    "command_external": "auth_kind_command_external",
    "external_chatgpt_tokens": "auth_kind_external_chatgpt_tokens",
}
_SUPPORTED_BOOTSTRAP_AUTH_KINDS = frozenset({"none", "managed_chatgpt", "api_key"})
_UNSUPPORTED_BOOTSTRAP_AUTH_CODES = {
    "unknown": "bootstrap_auth_kind_unknown",
    "workload_identity": "bootstrap_auth_kind_workload_identity",
    "command_external": "bootstrap_auth_kind_command_external",
    "external_chatgpt_tokens": "bootstrap_auth_kind_external_chatgpt_tokens",
}


class AuthGuardBlocked(RuntimeError):
    """A stable, non-secret admission failure code."""

    def __init__(self, code: str) -> None:
        self.code = code
        super().__init__(code)


@dataclass(frozen=True)
class MetadataObservation:
    native_version: str
    native_sha256: str
    auth_kind: str
    bootstrap_auth_kind: str
    credential_store: str
    external_auth_configured: bool
    use_agent_identity: bool
    source_kind: str
    proof_sha256: str


class VerifiedAuthMetadata:
    """Opaque admission input minted only from a trusted in-process source."""

    __slots__ = (
        "_auth_kind",
        "_bootstrap_auth_kind",
        "_credential_store",
        "_external_auth_configured",
        "_use_agent_identity",
        "_source_kind",
        "_proof_sha256",
        "_seal",
    )

    def __init__(
        self,
        *,
        auth_kind: str,
        bootstrap_auth_kind: str,
        credential_store: str,
        external_auth_configured: bool,
        use_agent_identity: bool,
        source_kind: str,
        proof_sha256: str,
        _seal: object,
    ) -> None:
        if _seal is not _VERIFIED_METADATA_SEAL:
            raise AuthGuardBlocked("metadata_not_verified")
        self._auth_kind = auth_kind
        self._bootstrap_auth_kind = bootstrap_auth_kind
        self._credential_store = credential_store
        self._external_auth_configured = external_auth_configured
        self._use_agent_identity = use_agent_identity
        self._source_kind = source_kind
        self._proof_sha256 = proof_sha256
        self._seal = _seal


class VerifiedMetadataSource(Protocol):
    """Implemented only by the trusted native/SDK boundary, never a wire DTO."""

    def read_verified_nonsecret_metadata(self) -> MetadataObservation: ...


def extract_safe_auth_metadata(source: VerifiedMetadataSource) -> VerifiedAuthMetadata:
    """Project a verified source into an opaque, non-secret admission object.

    The production caller owns source verification. This function deliberately
    accepts no dict/JSON representation, token, credential path, or raw config.
    """

    try:
        reader = getattr(source, "read_verified_nonsecret_metadata", None)
    except Exception:
        raise AuthGuardBlocked("metadata_source_failed") from None
    if not callable(reader):
        raise AuthGuardBlocked("metadata_source_unverified")
    try:
        observation = reader()
    except Exception:
        raise AuthGuardBlocked("metadata_source_failed") from None
    if not isinstance(observation, MetadataObservation):
        raise AuthGuardBlocked("metadata_source_invalid")
    if not all(
        isinstance(value, str)
        for value in (
            observation.native_version,
            observation.native_sha256,
            observation.auth_kind,
            observation.bootstrap_auth_kind,
            observation.credential_store,
            observation.source_kind,
            observation.proof_sha256,
        )
    ) or not all(
        isinstance(value, bool)
        for value in (
            observation.external_auth_configured,
            observation.use_agent_identity,
        )
    ):
        raise AuthGuardBlocked("metadata_source_invalid")
    if observation.native_version != EXPECTED_NATIVE_VERSION:
        raise AuthGuardBlocked("native_version_mismatch")
    if observation.native_sha256 != EXPECTED_NATIVE_SHA256:
        raise AuthGuardBlocked("native_sha256_mismatch")
    if not observation.proof_sha256:
        raise AuthGuardBlocked("metadata_proof_missing")
    if _SHA256_RE.fullmatch(observation.proof_sha256) is None:
        raise AuthGuardBlocked("metadata_proof_invalid")
    if observation.source_kind not in _TRUSTED_SOURCE_KINDS:
        raise AuthGuardBlocked("metadata_source_untrusted")

    return VerifiedAuthMetadata(
        auth_kind=observation.auth_kind,
        bootstrap_auth_kind=observation.bootstrap_auth_kind,
        credential_store=observation.credential_store,
        external_auth_configured=observation.external_auth_configured,
        use_agent_identity=observation.use_agent_identity,
        source_kind=observation.source_kind,
        proof_sha256=observation.proof_sha256,
        _seal=_VERIFIED_METADATA_SEAL,
    )


def prepare_guarded_environment(
    source_env: Mapping[str, str], metadata: VerifiedAuthMetadata
) -> dict[str, str]:
    """Return a child-only environment with fail-closed OAuth refresh routing."""

    if not isinstance(metadata, VerifiedAuthMetadata) or (
        metadata._seal is not _VERIFIED_METADATA_SEAL
    ):
        raise AuthGuardBlocked("metadata_not_verified")
    if REFRESH_OVERRIDE_ENV in source_env:
        raise AuthGuardBlocked("refresh_override_already_present")
    if metadata._auth_kind not in _SUPPORTED_STORES:
        raise AuthGuardBlocked(
            _UNSUPPORTED_AUTH_CODES.get(metadata._auth_kind, "auth_kind_unknown")
        )
    if not metadata._bootstrap_auth_kind:
        raise AuthGuardBlocked("bootstrap_auth_kind_missing")
    if metadata._bootstrap_auth_kind not in _SUPPORTED_BOOTSTRAP_AUTH_KINDS:
        raise AuthGuardBlocked(
            _UNSUPPORTED_BOOTSTRAP_AUTH_CODES.get(
                metadata._bootstrap_auth_kind, "bootstrap_auth_kind_unknown"
            )
        )
    if metadata._external_auth_configured:
        raise AuthGuardBlocked("external_auth_configured")
    if (
        metadata._auth_kind == "managed_chatgpt"
        and metadata._bootstrap_auth_kind != "managed_chatgpt"
    ):
        raise AuthGuardBlocked("auth_path_inconsistent")
    if metadata._use_agent_identity:
        raise AuthGuardBlocked("use_agent_identity_enabled")
    if metadata._credential_store not in _SUPPORTED_STORES[metadata._auth_kind]:
        raise AuthGuardBlocked("credential_store_unsupported")

    child_env = dict(source_env)
    child_env[REFRESH_OVERRIDE_ENV] = REFRESH_TRIPWIRE
    return child_env


def project_login_status(exit_code: int, stderr: bytes) -> str:
    """Discard raw fixed-CLI output and return only a non-secret auth category."""

    if exit_code != 0:
        return "unknown"
    try:
        output = stderr.decode("utf-8", errors="strict")
    except UnicodeDecodeError:
        return "unknown"
    if not output.endswith("\n") or output.count("\n") != 1:
        return "unknown"
    line = output[:-1]
    exact = {
        "Logged in using ChatGPT": "managed_chatgpt",
        "Logged in using workload identity": "workload_identity",
        "Logged in using access token": "agent_identity",
        "Logged in using personal access token": "personal_access_token",
        "Logged in using Amazon Bedrock API key": "bedrock_api_key",
        "Logged in using Amazon Bedrock AWS access keys": "bedrock_access_keys",
    }
    if line in exact:
        return exact[line]

    prefix = "Logged in using an API key - "
    if line.startswith(prefix):
        redacted = line[len(prefix) :]
        if redacted == "***":
            return "api_key"
        if (
            len(redacted) == 16
            and redacted[8:11] == "***"
            and all(char.isprintable() and not char.isspace() for char in redacted)
        ):
            return "api_key"
    return "unknown"
