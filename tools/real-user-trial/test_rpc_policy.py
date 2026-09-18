from __future__ import annotations

import sys
from pathlib import Path

import pytest


ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / "plugins" / "codex-orchestrator" / "lib"))

from trial.rpc_policy import AuthRpcPolicyBlocked, enforce_auth_rpc_policy  # noqa: E402


@pytest.mark.parametrize(
    "message",
    [
        {"id": 1, "method": "account/read"},
        {"id": 1, "method": "account/read", "params": {}},
        {"id": 1, "method": "account/read", "params": {"refreshToken": False}},
    ],
)
def test_account_read_allows_only_omitted_or_strict_false_refresh(message: dict) -> None:
    assert enforce_auth_rpc_policy("client_to_server", message) is message


@pytest.mark.parametrize("value", [True, 0, 1, "false", None])
def test_account_read_rejects_refresh_or_non_boolean_flag(value: object) -> None:
    expected = "account_refresh_forbidden" if value is True else "account_refresh_flag_invalid"
    with pytest.raises(AuthRpcPolicyBlocked, match=f"^{expected}$"):
        enforce_auth_rpc_policy(
            "client_to_server",
            {"id": 1, "method": "account/read", "params": {"refreshToken": value}},
        )


def test_account_read_rejects_unknown_params_fail_closed() -> None:
    with pytest.raises(AuthRpcPolicyBlocked, match="^account_read_params_invalid$"):
        enforce_auth_rpc_policy(
            "client_to_server",
            {"id": 1, "method": "account/read", "params": {"futureAuth": False}},
        )


@pytest.mark.parametrize(
    "method",
    [
        "account/login/start",
        "account/login/cancel",
        "account/logout",
        "account/bedrock/discover",
        "account/bedrock/setup",
        "getAuthStatus",
        "mcpServer/oauth/login",
    ],
)
def test_client_auth_mutation_and_credential_access_methods_are_rejected(
    method: str,
) -> None:
    message = {
        "id": 1,
        "method": method,
        "params": {"apiKey": "secret-shaped-fixture"},
    }
    with pytest.raises(AuthRpcPolicyBlocked, match="^auth_rpc_forbidden$") as error:
        enforce_auth_rpc_policy("client_to_server", message)

    assert "secret-shaped-fixture" not in repr(error.value)


def test_server_chatgpt_token_refresh_request_is_rejected() -> None:
    with pytest.raises(AuthRpcPolicyBlocked, match="^account_refresh_forbidden$"):
        enforce_auth_rpc_policy(
            "server_to_client",
            {
                "id": 2,
                "method": "account/chatgptAuthTokens/refresh",
                "params": {"reason": "unauthorized"},
            },
        )


@pytest.mark.parametrize(
    ("direction", "method"),
    [
        ("client_to_server", "account/futureCredential/write"),
        ("server_to_client", "account/futureRefresh"),
        ("client_to_server", "future/auth/rotate"),
        ("server_to_client", "futureOAuthRequest"),
    ],
)
def test_unknown_account_or_auth_methods_fail_closed(
    direction: str, method: str
) -> None:
    with pytest.raises(
        AuthRpcPolicyBlocked, match="^unknown_account_or_auth_rpc$"
    ):
        enforce_auth_rpc_policy(direction, {"id": 1, "method": method})


@pytest.mark.parametrize(
    ("direction", "method"),
    [
        ("client_to_server", "initialize"),
        ("client_to_server", "thread/read"),
        ("client_to_server", "account/rateLimits/read"),
        ("client_to_server", "account/rateLimitResetCredit/consume"),
        ("client_to_server", "account/usage/read"),
        ("client_to_server", "account/workspaceMessages/read"),
        ("client_to_server", "account/sendAddCreditsNudgeEmail"),
        ("server_to_client", "account/updated"),
        ("server_to_client", "account/rateLimits/updated"),
        ("server_to_client", "account/login/completed"),
        ("server_to_client", "modelProvider/authRecoveryStarted"),
        ("server_to_client", "modelProvider/authRecoveryCompleted"),
        ("server_to_client", "mcpServer/oauthLogin/completed"),
    ],
)
def test_known_non_auth_operation_or_notification_is_unchanged(
    direction: str, method: str
) -> None:
    message = {"id": 1, "method": method, "params": {"opaque": "unchanged"}}
    assert enforce_auth_rpc_policy(direction, message) is message
    assert message["params"] == {"opaque": "unchanged"}


def test_json_rpc_response_without_method_is_unchanged() -> None:
    message = {"id": 1, "result": {"account": None}}
    assert enforce_auth_rpc_policy("server_to_client", message) is message
