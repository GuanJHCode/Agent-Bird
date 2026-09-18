from __future__ import annotations

import json
from typing import Callable

from trial.rpc_policy import AuthRpcPolicyBlocked, enforce_auth_rpc_policy


class AuthWireRejected(ValueError):
    """Stable rejection from the trial WebSocket auth byte gate."""

    def __init__(self, code: str) -> None:
        self.code = code
        super().__init__(code)


class _InvalidAuthWireMessage(ValueError):
    pass


class _TrialAuthWirePolicyBlocked(ValueError):
    def __init__(self, code: str) -> None:
        self.code = code
        super().__init__(code)


_POLICY_CODES = frozenset(
    {
        "account_read_params_invalid",
        "account_refresh_flag_invalid",
        "account_refresh_forbidden",
        "auth_rpc_forbidden",
        "rpc_direction_invalid",
        "rpc_message_invalid",
        "rpc_method_invalid",
        "unknown_account_or_auth_rpc",
    }
)

_CONFIG_WRITE_METHODS = frozenset({"config/value/write", "config/batchWrite"})
_SESSION_OVERRIDE_METHODS = frozenset(
    {"thread/start", "thread/resume", "thread/fork", "turn/start"}
)
_THREAD_OVERRIDE_METHODS = frozenset(
    {"thread/start", "thread/resume", "thread/fork"}
)
# Fixed 0.154 TUI `config_request_overrides_from_config` output.  Shell
# environment policy is deliberately excluded because it can remove the
# refresh tripwire from descendant processes; login-shell use is allowed only
# when the recorded override is strict false.
_FIXED_TUI_CONFIG_OVERRIDE_KEYS = frozenset(
    {
        "allow_login_shell",
        "bypass_hook_trust",
        "default_permissions",
        "features",
        "model_reasoning_effort",
        "model_reasoning_summary",
        "model_verbosity",
        "network",
        "permissions",
        "personality",
        "sandbox_workspace_write",
        "web_search",
    }
)


def _fixed_tui_config_is_safe(method: str, value: object) -> bool:
    if value is None or value == {}:
        return True
    if method not in _THREAD_OVERRIDE_METHODS or not isinstance(value, dict):
        return False
    if set(value) - _FIXED_TUI_CONFIG_OVERRIDE_KEYS:
        return False
    if "allow_login_shell" in value and value["allow_login_shell"] is not False:
        return False
    if "features" not in value:
        return True
    features = value["features"]
    if not isinstance(features, dict):
        return False
    if set(features) - {"use_agent_identity"}:
        return False
    if "use_agent_identity" not in features:
        return True
    return features["use_agent_identity"] is False


def _enforce_trial_auth_overrides(message: dict[str, object]) -> None:
    method = message.get("method")
    if method in _CONFIG_WRITE_METHODS:
        raise _TrialAuthWirePolicyBlocked("trial_config_write_forbidden")
    if method not in _SESSION_OVERRIDE_METHODS:
        return
    params = message.get("params")
    if not isinstance(params, dict):
        return
    if params.get("modelProvider") is not None or (
        "config" in params and not _fixed_tui_config_is_safe(method, params["config"])
    ):
        raise _TrialAuthWirePolicyBlocked(
            "trial_session_auth_override_forbidden"
        )


def _unique_json(payload: bytes) -> dict[str, object]:
    def unique_object(pairs: list[tuple[str, object]]) -> dict[str, object]:
        result: dict[str, object] = {}
        for key, value in pairs:
            if key in result:
                raise _InvalidAuthWireMessage("duplicate_json_key")
            result[key] = value
        return result

    try:
        value = json.loads(
            payload,
            object_pairs_hook=unique_object,
            parse_constant=lambda _value: (_ for _ in ()).throw(
                _InvalidAuthWireMessage("json_constant")
            ),
        )
    except (UnicodeDecodeError, json.JSONDecodeError, TypeError) as error:
        raise _InvalidAuthWireMessage("invalid_json") from error
    if not isinstance(value, dict):
        raise _InvalidAuthWireMessage("rpc_object_required")
    return value


def _gate_type(native_websocket_gate_type: type):
    class AuthWebSocketGate(native_websocket_gate_type):
        """Reuse the native gate's bounded frame decoder; replace RPC policy."""

        def __init__(
            self,
            *,
            server: bool,
            on_reject: Callable[[dict[str, object]], None],
        ) -> None:
            super().__init__("trial-auth-wire", server=server)
            self._auth_on_reject = on_reject
            self._auth_closed = False

        def _complete_auth_message(self, payload: bytes) -> bytes:
            message = _unique_json(payload)
            direction = "server_to_client" if self.server else "client_to_server"
            if not self.server:
                _enforce_trial_auth_overrides(message)
            enforce_auth_rpc_policy(direction, message)
            frames = self._fragment_frames
            self._fragment_frames = []
            self._fragment_payload.clear()
            return b"".join(frames)

        def __call__(self, data: bytes) -> bytes:
            return self.feed(data)

        def feed(self, data: bytes) -> bytes:
            try:
                return self._feed_auth(data)
            except AuthRpcPolicyBlocked as error:
                code = error.code if error.code in _POLICY_CODES else "auth_wire_rejected"
            except _TrialAuthWirePolicyBlocked as error:
                code = error.code
            except Exception:
                code = "auth_wire_invalid_message"
            self._buffer.clear()
            self._fragment_payload.clear()
            self._fragment_frames = []
            try:
                self._auth_on_reject(
                    {
                        "stage": (
                            "trial_auth_wire_server"
                            if self.server
                            else "trial_auth_wire_client"
                        ),
                        "reason": code,
                    }
                )
            except Exception:
                pass
            raise AuthWireRejected(code) from None

        def _feed_auth(self, data: bytes) -> bytes:
            if (
                not isinstance(data, (bytes, bytearray))
                or len(data) > 4 * 1024 * 1024
                or self._auth_closed
            ):
                raise _InvalidAuthWireMessage("wire_chunk")
            self._buffer.extend(data)
            output: list[bytes] = []
            if not self._handshake_done:
                marker = self._buffer.find(b"\r\n\r\n")
                if marker < 0:
                    if len(self._buffer) > 65536:
                        raise _InvalidAuthWireMessage("websocket_handshake_size")
                    return b""
                end = marker + 4
                if end > 65536:
                    raise _InvalidAuthWireMessage("websocket_handshake_size")
                output.append(bytes(self._buffer[:end]))
                del self._buffer[:end]
                self._handshake_done = True

            while True:
                parsed = self._frame()
                if parsed is None:
                    break
                fin, opcode, payload, raw = parsed
                if opcode == 1:
                    if self._fragment_frames:
                        raise _InvalidAuthWireMessage("websocket_fragment_start")
                    self._fragment_payload.extend(payload)
                    self._fragment_frames = [raw]
                    if fin:
                        output.append(
                            self._complete_auth_message(bytes(self._fragment_payload))
                        )
                elif opcode == 0:
                    if not self._fragment_frames:
                        raise _InvalidAuthWireMessage(
                            "websocket_fragment_continuation"
                        )
                    self._fragment_payload.extend(payload)
                    self._fragment_frames.append(raw)
                    if len(self._fragment_payload) > 65535:
                        raise _InvalidAuthWireMessage("websocket_message_too_large")
                    if fin:
                        output.append(
                            self._complete_auth_message(bytes(self._fragment_payload))
                        )
                elif opcode in (8, 9, 10):
                    if not fin or len(payload) > 125 or (opcode == 8 and len(payload) == 1):
                        raise _InvalidAuthWireMessage("websocket_control_frame")
                    if opcode == 8:
                        if self._buffer:
                            raise _InvalidAuthWireMessage("websocket_after_close")
                        self._fragment_frames = []
                        self._fragment_payload.clear()
                        self._auth_closed = True
                    output.append(raw)
                    if opcode == 8:
                        break
                else:
                    raise _InvalidAuthWireMessage("websocket_frame_opcode")
            return b"".join(output)

    return AuthWebSocketGate


def make_auth_wire_gate_factory(native_websocket_gate_type: type):
    """Build the explicit process-local factory consumed by ActivationService."""

    if not isinstance(native_websocket_gate_type, type):
        raise TypeError("native_websocket_gate_type_required")
    auth_gate_type = _gate_type(native_websocket_gate_type)

    def factory(
        _frontend_peer: object,
        _backend_peer: object,
        _connection_id: str,
        _epoch: int,
        on_reject: Callable[[dict[str, object]], None],
    ):
        if not callable(on_reject):
            raise TypeError("auth_wire_reject_callback_required")
        return (
            auth_gate_type(server=False, on_reject=on_reject),
            auth_gate_type(server=True, on_reject=on_reject),
        )

    return factory


__all__ = ["AuthWireRejected", "make_auth_wire_gate_factory"]
