"""One guarded resume P -> side S -> cached P -> cached S observation.

Only --prepare is read-only. --run is an explicit native execution boundary.
Status observations never feed the attachment inferer. No model input is sent.
"""
from __future__ import annotations

import argparse
import asyncio
from dataclasses import asdict, dataclass
import datetime as dt
import hashlib
import json
import math
from pathlib import Path
import sys
import tomllib
from typing import Any, Mapping
import uuid

_REPO = Path(__file__).resolve().parents[3]
_RESUME_SCRIPTS = _REPO / 'tasks/g0-proxy-continuation/scripts'
if str(_RESUME_SCRIPTS) not in sys.path:
    sys.path.insert(0, str(_RESUME_SCRIPTS))
import resume_case as resume  # noqa: E402
from proxy_native_runtime import (  # noqa: E402
    AttachmentCandidate, CaseDirectory, DEFAULT_CLI, NativeRuntime,
    RuntimeInvariantError, _connection_summary, canonical_resume_thread_id, raw_seconds,
)

SOURCE_COMMIT = '6b9826e3aa83b1a5947db50f4332cb9c65f1b340'
CROSSTERM_COMMIT = '45fecb9508105988f42fe6ff0441783ed3717f92'
TOGGLE_BYTES = b'\x1f'  # Fixed parser: Ctrl-7; default Ctrl-/ compatibility path.
MODEL = 'gpt-5.6-luna'
_RPC_EVENTS = {'rpc', 'rpc_unknown', 'rpc_response'}


@dataclass(frozen=True)
class CachedSwitchCaseSpec(resume.ResumeCaseSpec):
    def __post_init__(self) -> None:
        super().__post_init__()
        for value, maximum in ((self.local_window_seconds, 10), (self.model_window_seconds, 120)):
            if isinstance(value, bool) or not math.isfinite(value) or not 0 < value <= maximum:
                raise ValueError('cached probe cannot enlarge the original 10/120 second windows')

    def implementation_sources(self) -> tuple[Path, ...]:
        return resume._dedupe_paths(super().implementation_sources() + (Path(__file__).resolve(),))


def _require_luna(spec: CachedSwitchCaseSpec) -> None:
    if spec.config_path is None:
        raise RuntimeInvariantError('explicit frozen project Luna configuration required')
    with spec.config_path.open('rb') as stream:
        config = tomllib.load(stream)
    if config.get('model') != MODEL:
        raise RuntimeInvariantError('cached probe requires the configured Luna model')


def prepare_cached_switch_case(spec: CachedSwitchCaseSpec) -> dict[str, Any]:
    _require_luna(spec)
    return resume.prepare_resume_case(spec)


def _candidate(value: AttachmentCandidate | None) -> dict[str, Any] | None:
    return asdict(value) if value is not None else None


def _uuid(value: Any) -> str | None:
    try:
        return canonical_resume_thread_id(value)
    except ValueError:
        return None


def validate_zero_model(bridge: Any) -> None:
    # The shared validator also rejects opaque server turn/started records.
    resume.validate_silent_resume(bridge)
    sequences = [row['local_seq'] for row in bridge.trace if 'local_seq' in row]
    if sequences and (sequences[0] != 1 or sequences != list(range(1, len(sequences) + 1))):
        raise RuntimeInvariantError('cached probe trace has a sequence gap')
    if any(row.get('event') == 'gap' for row in bridge.trace):
        raise RuntimeInvariantError('cached probe trace is incomplete')


def boundary_summary(rows: list[dict[str, Any]], epoch: int,
                     before: AttachmentCandidate | None,
                     after: AttachmentCandidate | None) -> dict[str, Any]:
    rpc = [row for row in rows if row.get('event') in _RPC_EVENTS]
    if any(row.get('conn_epoch') != epoch for row in rows):
        raise RuntimeInvariantError('cached switch crossed a connection epoch')
    methods = [row.get('method') for row in rpc]
    if not rpc:
        classification = 'zero_rpc'
    elif 'thread/fork' in methods:
        classification = 'fork_rpc'
    elif any(method in {'thread/resume', 'thread/read'} for method in methods):
        classification = 'refresh_rpc'
    elif any(method in {'thread/subscribe', 'thread/unsubscribe', 'thread/inject_items'} for method in methods):
        classification = 'channel_change_rpc'
    elif set(methods) == {'skills/list'}:
        classification = 'skills_refresh_only'
    else:
        classification = 'other_rpc'
    return {
        'classification': classification, 'zero_rpc': not rpc,
        'methods_in_order': methods,
        'rpc_events': [{key: row[key] for key in ('event', 'local_seq', 'direction', 'method',
                        'request_id', 'request_direction', 'response', 'ok', 'thread_id', 'thread') if key in row}
                       for row in rpc],
        'candidate_before': _candidate(before), 'candidate_after': _candidate(after),
        # The unchanged resume inferer has no side-selection qualification path.
        # Any candidate surviving the toggle is retained evidence, never renewed by status.
        'candidate_stale': after is not None, 'candidate_status': 'unknown',
        'status_used_for_candidate': False,
    }


def fork_summary(rows: list[dict[str, Any]], epoch: int, primary: str) -> dict[str, Any]:
    result: dict[str, Any] = {'status': 'unknown', 'p_thread_id': primary, 's_thread_id': None,
                              'thread_started_seen': False, 'inject_items': []}
    requests = [row for row in rows if row.get('event') == 'rpc' and row.get('direction') == 'client'
                and row.get('method') == 'thread/fork' and row.get('conn_epoch') == epoch]
    if len(requests) != 1:
        return result
    request = requests[0]
    responses = [row for row in rows if row.get('event') == 'rpc_response' and row.get('direction') == 'server'
                 and row.get('method') == 'thread/fork' and row.get('conn_epoch') == epoch
                 and row.get('request_id') == request.get('request_id')]
    if len(responses) != 1:
        return result
    response = responses[0]
    thread = response.get('thread', {})
    side = _uuid(thread.get('id')) if isinstance(thread, Mapping) else None
    result.update(request_id=request.get('request_id'), request_seq=request.get('local_seq'),
                  response_seq=response.get('local_seq'), response_ok=response.get('ok') is True,
                  request_thread_id=request.get('thread_id'), s_thread_id=side,
                  forked_from_id=thread.get('forkedFromId') if isinstance(thread, Mapping) else None)
    if (request.get('thread_id') == primary and side not in (None, primary)
            and response.get('ok') is True and thread.get('forkedFromId') == primary
            and type(request.get('local_seq')) is int and type(response.get('local_seq')) is int
            and request['local_seq'] < response['local_seq']):
        result['status'] = 'observed'
    result['thread_started_seen'] = side is not None and any(
        row.get('event') == 'rpc' and row.get('direction') == 'server'
        and row.get('method') == 'thread/started' and row.get('conn_epoch') == epoch
        and isinstance(row.get('thread'), Mapping) and row['thread'].get('id') == side
        for row in rows)
    for item in rows:
        if not (item.get('event') == 'rpc' and item.get('direction') == 'client'
                and item.get('method') == 'thread/inject_items' and item.get('conn_epoch') == epoch):
            continue
        replies = [row for row in rows if row.get('event') == 'rpc_response' and row.get('direction') == 'server'
                   and row.get('method') == 'thread/inject_items' and row.get('conn_epoch') == epoch
                   and row.get('request_id') == item.get('request_id')]
        valid = (side is not None and item.get('thread_id') == side and len(replies) == 1
                 and replies[0].get('ok') is True and replies[0]['local_seq'] > item['local_seq'])
        result['inject_items'].append({'request_id': item.get('request_id'), 'thread_id': item.get('thread_id'),
            'request_seq': item.get('local_seq'), 'response_seq': replies[0].get('local_seq') if len(replies) == 1 else None,
            'ok': replies[0].get('ok') if len(replies) == 1 else None, 'target_and_response_match': valid})
        if not valid:
            result['status'] = 'unknown'
    return result


def _live(runtime: Any, bridge: Any, tui: Any, epoch: int) -> None:
    validate_zero_model(bridge)
    if (tui.poll() is not None or bridge.epoch_invalid.get(epoch) is not None
            or len(bridge.connection_records) != 1
            or not any(record.epoch == epoch for record in bridge.connection_records.values())
            or getattr(bridge, 'identity_errors', {})):
        raise RuntimeInvariantError('cached probe lost its one live owned epoch')


def _deadline(spec: CachedSwitchCaseSpec, overall: float) -> float:
    now = raw_seconds()
    if now >= overall:
        raise TimeoutError('cached probe original overall window ended')
    return min(now + spec.local_window_seconds, overall)


async def _status(spec: CachedSwitchCaseSpec, runtime: Any, bridge: Any, tui: Any,
                  epoch: int, overall: float, expected: str | None) -> dict[str, Any]:
    _live(runtime, bridge, tui, epoch)
    begin = raw_seconds()
    deadline = _deadline(spec, overall)
    start_seq = len(bridge.trace)
    query_id = str(uuid.uuid4())
    candidate_before = runtime.candidate
    tui.command('/status')
    ids = await tui.async_status_checkpoint(deadline)
    _live(runtime, bridge, tui, epoch)
    capture = getattr(tui, '_status_buffer', None)
    if len(ids) != 1 or any(_uuid(value) is None for value in ids) or not isinstance(capture, bytes) or not capture:
        raise RuntimeInvariantError('fresh status does not contain one canonical ID and captured bytes')
    # Deliberately do not call record_status_reference or any inferer mutation.
    return {'query_id': query_id, 'command': '/status', 'ids': sorted(ids),
            'expected_id': expected, 'exact': expected is not None and ids == {expected},
            'started_at_raw': begin, 'deadline_raw': deadline, 'completed_at_raw': raw_seconds(),
            'captured_at': dt.datetime.now(dt.timezone.utc).isoformat(),
            'screen_bytes': len(capture), 'screen_sha256': hashlib.sha256(capture).hexdigest(),
            'trace_start_index': start_seq, 'trace_end_index': len(bridge.trace),
            'candidate_before': _candidate(candidate_before), 'candidate_after': _candidate(runtime.candidate),
            'status_used_for_candidate': False}


async def run_cached_switch_case(spec: CachedSwitchCaseSpec, *, preflight: Mapping[str, Any] | None = None,
                                 runtime_factory: Any = NativeRuntime) -> dict[str, Any]:
    _require_luna(spec)
    prepared = dict(preflight) if preflight is not None else resume._load_preflight(spec)
    resume.verify_preflight(spec, prepared)
    case = CaseDirectory.create(spec.case_dir)
    config = spec.runtime_config()
    runtime = runtime_factory(config)
    bridge = tui = None
    failure = None
    cleanup: dict[str, Any] = {}
    epoch = None
    exit_code = None
    eof_seen = False
    epoch_reason = None
    stage = 'preflight'
    result: dict[str, Any] = {'case': case.name, 'status': 'unknown', 'source_commit': SOURCE_COMMIT,
        'crossterm_commit': CROSSTERM_COMMIT, 'toggle_hex': TOGGLE_BYTES.hex(),
        'configured_model': MODEL, 'argv': list(spec.argv()), 'backend_argv': list(config.backend_argv),
        'p_thread_id': spec.expected_resume_thread_id, 's_thread_id': None, 'status_checks': [], 'switches': [],
        'generic_attachment_verified': False, 'cached_zero_rpc_observed': False,
        'protected_hashes': prepared['hashes'], 'quit_sent': False}
    overall = raw_seconds() + spec.model_window_seconds
    result['overall_deadline_raw'] = overall
    try:
        resume.verify_preflight(spec, prepared, case_created=True)
        config.require_frozen_binary()
        stage = 'resume'
        await runtime.start()
        bridge, tui = runtime.bridge, runtime.tui_driver
        if bridge is None or tui is None:
            raise RuntimeInvariantError('native runtime omitted bridge or PTY evidence')
        deadline = _deadline(spec, overall)
        while runtime.candidate is None and raw_seconds() < deadline:
            await tui.aread_until(min(deadline, raw_seconds() + 0.1))
        candidate = runtime.candidate
        if candidate is None or candidate.thread_id != spec.expected_resume_thread_id:
            raise RuntimeInvariantError('known primary resume candidate missing')
        epoch = candidate.connection_epoch
        _live(runtime, bridge, tui, epoch)
        result['initial_candidate'] = _candidate(candidate)
        stage = 'status-primary'
        check = await _status(spec, runtime, bridge, tui, epoch, overall, spec.expected_resume_thread_id)
        result['status_checks'].append(check)
        if not check['exact'] or runtime.candidate != candidate:
            raise RuntimeInvariantError('initial primary status or candidate mismatch')
        for index, label in enumerate(('first-side', 'cached-primary', 'cached-side'), 1):
            stage = label
            _live(runtime, bridge, tui, epoch)
            if tui.evidence.get('ui_state') in {'onboarding', 'picker', 'error'}:
                raise RuntimeInvariantError('side toggle requires a command-ready TUI')
            start = len(bridge.trace)
            before = runtime.candidate
            deadline = _deadline(spec, overall)
            tui.write(TOGGLE_BYTES, action=f'side-toggle-{index}')
            await tui.aread_until(deadline)
            _live(runtime, bridge, tui, epoch)
            status_start = len(bridge.trace)
            if index == 1:
                result['fork'] = fork_summary(bridge.trace[start:], epoch, spec.expected_resume_thread_id)
                result['s_thread_id'] = result['fork']['s_thread_id']
            expected = spec.expected_resume_thread_id if index == 2 else result['s_thread_id']
            check = await _status(spec, runtime, bridge, tui, epoch, overall, expected)
            result['status_checks'].append(check)
            summary = boundary_summary(bridge.trace[start:], epoch, before, runtime.candidate)
            summary.update(label=label, toggle_index=index, trace_start_index=start,
                           status_trace_start_index=status_start, trace_end_index=len(bridge.trace),
                           active_thread_before=result['status_checks'][-2]['ids'][0],
                           active_thread_after=check['ids'][0], observed_only=True)
            result['switches'].append(summary)
            if expected is not None and not check['exact']:
                raise RuntimeInvariantError('independent status does not corroborate the expected P/S boundary')
        stage = 'quit'
        _live(runtime, bridge, tui, epoch)
        terminal_before = resume._terminal_event_count(bridge, epoch)
        deadline = _deadline(spec, overall)
        tui.command('/quit')
        result['quit_sent'] = True
        exit_code = await tui.await_exit(deadline)
        await tui.aread_until(deadline)
        terminal_after = resume._terminal_event_count(bridge, epoch)
        eof_seen = terminal_after > terminal_before
        epoch_reason = bridge.epoch_invalid.get(epoch)
        result['terminal_events'] = {'before_quit': terminal_before, 'after_quit': terminal_after}
        validate_zero_model(bridge)
    except BaseException as exc:
        failure = resume._failure_record(exc, stage)
    finally:
        bridge = bridge if bridge is not None else runtime.bridge
        tui = tui if tui is not None else runtime.tui_driver
        try:
            cleanup = await runtime.close()
            result['cleanup'] = cleanup
        except BaseException as exc:
            result['cleanup_failure'] = resume._failure_record(exc, 'cleanup')
        try:
            if resume._source_hashes(config, spec) != prepared['hashes']:
                raise RuntimeInvariantError('cached probe source or protected files changed')
        except BaseException as exc:
            result['protection_failure'] = resume._failure_record(exc, 'protection')
        if failure is None:
            try:
                result['termination'] = resume.validate_resume_terminal(termination_mode='normal_quit',
                    quit_sent=result['quit_sent'], eof_seen=eof_seen, exit_code=exit_code,
                    epoch_reason=epoch_reason, cleanup_result=cleanup)
            except BaseException as exc:
                failure = resume._failure_record(exc, 'terminal')
        if bridge is not None:
            result['trace_complete'] = bridge.trace_complete
            result['model_turns'] = bridge.model_turns if type(bridge.model_turns) is int else None
            result['connections'] = [_connection_summary(record) for record in bridge.connection_records.values()]
            result['epoch_invalid'] = dict(bridge.epoch_invalid)
            try:
                validate_zero_model(bridge)
                case.write_json('trace.json', {'complete': bridge.trace_complete, 'events': bridge.trace})
            except BaseException as exc:
                result['trace_failure'] = resume._failure_record(exc, 'trace')
        if tui is not None:
            result['tui'] = tui.evidence
        if failure is not None:
            result['failure'] = failure
        complete = (failure is None and result.get('fork', {}).get('status') == 'observed'
                    and len(result['switches']) == 3 and all(row['exact'] for row in result['status_checks'])
                    and result.get('trace_complete') is True and result.get('model_turns') == 0
                    and result.get('termination', {}).get('status') == 'observed'
                    and not any(key in result for key in ('cleanup_failure', 'protection_failure', 'trace_failure')))
        result['status'] = 'observed' if complete else 'unknown'
        result['cached_zero_rpc_observed'] = complete and all(row['zero_rpc'] for row in result['switches'][1:])
        case.write_json('result.json', result)
    return result


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    mode = parser.add_mutually_exclusive_group(required=True)
    mode.add_argument('--prepare', action='store_true')
    mode.add_argument('--run', action='store_true')
    for name in ('case-dir', 'frontend-socket', 'backend-socket', 'cwd', 'config'):
        parser.add_argument('--' + name, type=Path, required=True)
    parser.add_argument('--cli', type=Path, default=DEFAULT_CLI)
    parser.add_argument('--resume-thread-id', required=True)
    parser.add_argument('--preflight-path', type=Path)
    for name in ('global-config', 'test-config', 'hook', 'seed-protection'):
        parser.add_argument('--' + name, type=Path, action='append', default=[])
    args = parser.parse_args(argv)
    try:
        spec = CachedSwitchCaseSpec(case_dir=args.case_dir, frontend_socket=args.frontend_socket,
            backend_socket=args.backend_socket, cli=args.cli, cwd=args.cwd, config_path=args.config,
            expected_resume_thread_id=args.resume_thread_id, preflight_path=args.preflight_path,
            global_config_paths=tuple(args.global_config), test_config_paths=tuple(args.test_config),
            hook_paths=tuple(args.hook), seed_protection_paths=tuple(args.seed_protection))
        result = prepare_cached_switch_case(spec) if args.prepare else asyncio.run(run_cached_switch_case(spec))
    except (OSError, RuntimeInvariantError, ValueError) as exc:
        parser.exit(1, f'cached probe failed: {resume._failure_code(exc)}\n')
    print(json.dumps(result, sort_keys=True))
    return 0 if args.prepare or result['status'] == 'observed' else 1


if __name__ == '__main__':
    raise SystemExit(main())
