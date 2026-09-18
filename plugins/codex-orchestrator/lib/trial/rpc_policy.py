from __future__ import annotations

from typing import Mapping


class AuthRpcPolicyBlocked(RuntimeError):
    """Stable, non-secret rejection from the trial RPC auth boundary."""

    def __init__(self, code: str) -> None:
        self.code = code
        super().__init__(code)


_DIRECTIONS = {"client_to_server", "server_to_client"}

_CLIENT_BLOCKED_AUTH = {
    "account/login/start",
    "account/login/cancel",
    "account/logout",
    "account/bedrock/discover",
    "account/bedrock/setup",
    "getAuthStatus",
    "mcpServer/oauth/login",
}

_CLIENT_KNOWN_ACCOUNT_NONAUTH = {
    "account/rateLimits/read",
    "account/rateLimitResetCredit/consume",
    "account/usage/read",
    "account/workspaceMessages/read",
    "account/sendAddCreditsNudgeEmail",
}

_SERVER_REFRESH = "account/chatgptAuthTokens/refresh"

_SERVER_KNOWN_ACCOUNT_OR_AUTH_NOTIFICATIONS = {
    "account/updated",
    "account/rateLimits/updated",
    "account/login/completed",
    "modelProvider/authRecoveryStarted",
    "modelProvider/authRecoveryCompleted",
    "mcpServer/oauthLogin/completed",
}


def _looks_like_unknown_auth_method(method: str) -> bool:
    folded = method.casefold()
    return (
        folded.startswith("account/")
        or folded.startswith("auth/")
        or "/auth/" in folded
        or folded.endswith("/auth")
        or "oauth" in folded
        or "authtoken" in folded
        or ("account" in folded and ("login" in folded or "logout" in folded))
    )


def _enforce_account_read(message: Mapping[str, object]) -> None:
    if "params" not in message:
        return
    params = message["params"]
    if not isinstance(params, Mapping) or set(params) - {"refreshToken"}:
        raise AuthRpcPolicyBlocked("account_read_params_invalid")
    if "refreshToken" not in params:
        return
    refresh_token = params["refreshToken"]
    if type(refresh_token) is not bool:
        raise AuthRpcPolicyBlocked("account_refresh_flag_invalid")
    if refresh_token:
        raise AuthRpcPolicyBlocked("account_refresh_forbidden")


def enforce_auth_rpc_policy(
    direction: str, message: Mapping[str, object]
) -> Mapping[str, object]:
    if direction not in _DIRECTIONS:
        raise AuthRpcPolicyBlocked("rpc_direction_invalid")
    if not isinstance(message, Mapping):
        raise AuthRpcPolicyBlocked("rpc_message_invalid")
    if "method" not in message:
        return message
    method = message["method"]
    if not isinstance(method, str) or not method:
        raise AuthRpcPolicyBlocked("rpc_method_invalid")

    if direction == "client_to_server":
        if method == "account/read":
            _enforce_account_read(message)
            return message
        if method in _CLIENT_BLOCKED_AUTH:
            raise AuthRpcPolicyBlocked("auth_rpc_forbidden")
        if method in _CLIENT_KNOWN_ACCOUNT_NONAUTH:
            return message
    else:
        if method == _SERVER_REFRESH:
            raise AuthRpcPolicyBlocked("account_refresh_forbidden")
        if method in _SERVER_KNOWN_ACCOUNT_OR_AUTH_NOTIFICATIONS:
            return message

    if _looks_like_unknown_auth_method(method):
        raise AuthRpcPolicyBlocked("unknown_account_or_auth_rpc")
    return message
