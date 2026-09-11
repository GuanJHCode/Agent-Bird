"""Bounded native retained experiment evidence; no raw model or tool bodies."""
import argparse
import datetime
import json
import pathlib
import re
import shlex
import sqlite3
import uuid
from native_retained_data import safe_output, text_summary


REPO = pathlib.Path('/Users/guanjunhui/code/ai/codex-cli-orchestration-design/tasks/g0-macos/data/test-repo')


def literal_call(text):
    """Only one observed text(await tool({literal options})) form is supported."""
    match = re.fullmatch(r'\s*text\(await tools\.(exec_command|write_stdin)\(\{(.*)\}\)\);?\s*', text, re.S)
    if not match:
        return None, {}
    body = match[2]; position = 0; options = {}
    pattern = re.compile(r'''\s*"?(cmd|workdir|yield_time_ms|max_output_tokens|session_id|chars)"?\s*:\s*("(?:[^"\\]|\\.)*"|'[^'\\\r\n]*'|[0-9]+)\s*(,|$)''')
    while position < len(body):
        item = pattern.match(body, position)
        if not item or item[1] in options:
            return None, {}
        token = item[2]
        try:
            options[item[1]] = token[1:-1] if token.startswith("'") else json.loads(token)
        except json.JSONDecodeError:
            return None, {}
        position = item.end()
    if 'workdir' in options and options['workdir'] != str(REPO):
        return None, {}
    allowed = {'cmd','workdir','yield_time_ms','max_output_tokens'} if match[1] == 'exec_command' else {'session_id','chars','yield_time_ms','max_output_tokens'}
    if not set(options) <= allowed:
        return None, {}
    for key in ('yield_time_ms', 'max_output_tokens'):
        if key in options and (type(options[key]) is not int or not 1 <= options[key] <= 300000):
            return None, {}
    return match[1], options


def safe_input(text, root, nonce, binary, directory):
    result = text_summary(text)
    result['goal_operation_mentioned'] = bool(re.search(r'tools\.(?:create_goal|update_goal|get_goal)\s*\(', text))
    operation, options = literal_call(text)
    if operation == 'write_stdin':
        sid = options.get('session_id')
        if type(sid) is int and 0 < sid < 2**31 and options.get('chars') == '' and 'cmd' not in options:
            result['stdin_session_id'] = sid
        return result
    if operation != 'exec_command' or type(options.get('cmd')) is not str:
        return result
    commands = []
    for segment in options['cmd'].split(' && '):
        try:
            words = shlex.split(segment)
        except ValueError:
            return result
        direct = bool(words and words[0] == 'exec')
        if direct:
            words = words[1:]
        if len(words) < 2 or words[0] != binary or (len(words) - 2) % 2:
            return result
        flags = dict(zip(words[2::2], words[3::2]))
        if len(flags) != (len(words) - 2) // 2 or flags.get('--dir') != directory or flags.get('--nonce') != nonce:
            return result
        base = {'--dir', '--nonce'}; verb = words[1]
        record = {'verb': verb, 'nonce': nonce, 'expected_binary_and_dir': True, 'shell_exec': direct}
        if verb == 'retained-inspect' and set(flags) == base:
            commands.append(record); continue
        if not direct or flags.get('--controller-thread') not in ('$CODEX_THREAD_ID', '${CODEX_THREAD_ID}'):
            return result
        record['controller_from_native_env'] = True
        if verb == 'retained-start' and set(flags) == base | {'--controller-thread', '--steps', '--interval'}:
            interval = re.fullmatch(r'([0-9]{1,6})(ms|s)', flags['--interval'])
            if not re.fullmatch(r'[0-9]{1,2}', flags['--steps']) or not interval:
                return result
            steps = int(flags['--steps']); milliseconds = int(interval[1]) * (1000 if interval[2] == 's' else 1)
            if not 1 <= steps <= 32 or milliseconds < 100 or steps * milliseconds > 120000:
                return result
            record.update(total_steps=steps, interval_ms=milliseconds)
        elif verb == 'retained-resume' and set(flags) == base | {'--controller-thread', '--revision', '--segment'}:
            if flags['--revision'] != '1' or not re.fullmatch(r'[0-9]{1,7}', flags['--segment']) or not 1 <= int(flags['--segment']) <= 1000000:
                return result
            record.update(revision=1, expected_segment=int(flags['--segment']))
        else:
            return result
        commands.append(record)
    if commands:
        result['commands'] = commands
    return result


def body_text(value):
    if type(value) is str:
        return value
    if type(value) is list:
        return ''.join(x['text'] for x in value if type(x) is dict and type(x.get('text')) is str)
    return ''


def filter_events(events, root, nonce, binary, directory):
    result = []; sources = {}; sessions = set()
    for event in events:
        payload = event.get('payload', {}); kind = payload.get('type')
        if event.get('type') == 'event_msg' and kind in ('task_started', 'task_complete', 'turn_aborted', 'error'):
            row = {'timestamp': event['timestamp'], 'record_type': 'event_msg', 'type': kind}
            turn = payload.get('turn_id')
            if type(turn) is str and re.fullmatch(r'[0-9a-f-]{36}', turn):
                row['turn_id'] = turn
            if payload.get('last_agent_message') in (nonce + '-WAITING', nonce + '-ACK'):
                row['last_agent_message'] = payload['last_agent_message']
            result.append(row); continue
        if event.get('type') != 'response_item' or kind not in ('message', 'agent_message', 'custom_tool_call', 'custom_tool_call_output', 'function_call', 'function_call_output'):
            continue
        row = {'timestamp': event['timestamp'], 'record_type': 'response_item', 'type': kind}
        for key, choices in {'role': ('user','assistant','developer','system','tool'), 'channel': ('analysis','commentary','final'),
                             'phase': ('commentary','final_answer'), 'name': ('exec','sleep','spawn_agent','wait_agent'),
                             'namespace': ('functions','clock','collaboration')}.items():
            if payload.get(key) in choices:
                row[key] = payload[key]
        call_id = payload.get('call_id')
        if type(call_id) is str and re.fullmatch(r'[A-Za-z0-9_-]{1,100}', call_id):
            row['call_id'] = call_id
        if kind in ('message','agent_message'):
            text = body_text(payload.get('content')); row['message_summary'] = text_summary(text)
            if text in (nonce + '-WAITING', nonce + '-ACK'):
                row['text'] = text
            if payload.get('role') == 'user':
                row['explicit_continue_input'] = text.startswith('继续这一合成任务') and nonce in text
                row['native_goal_control'] = text.startswith('<codex_internal_context source="goal">')
        elif kind == 'custom_tool_call':
            text = payload.get('input', '')
            row['input_summary'] = safe_input(text, root, nonce, binary, directory)
            if payload.get('name') == 'exec' and call_id:
                info = row['input_summary']
                if 'commands' in info:
                    sources[call_id] = 'literal_retained_command'
                elif info.get('stdin_session_id') in sessions:
                    sources[call_id] = 'known_retained_process_join'
        elif kind in ('custom_tool_call_output','function_call_output'):
            info = safe_output(body_text(payload.get('output')), root, nonce)
            if call_id in sources:
                row['retained_source'] = sources[call_id]
                for runtime in info.get('runtime', []):
                    if 'session_id' in runtime:
                        sessions.add(runtime['session_id'])
            else:
                info.pop('snapshots', None); info.pop('errors', None)
            row['output_summary'] = info
        elif kind == 'function_call':
            row['arguments_summary'] = text_summary(payload.get('arguments', ''))
        result.append(row)
    return result


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    for name in ('root','nonce','binary','dir','since','through','output'):
        parser.add_argument('--' + name, required=True)
    args = parser.parse_args()
    assert str(uuid.UUID(args.root)) == args.root
    match = re.fullmatch(r'rr-retained-(quit|kill)-([0-9]{2})', args.nonce); assert match
    expected = REPO / ('tasks/retained-resume/data/native-retained-' + match[1] + '-' + match[2])
    assert pathlib.Path(args.dir).resolve() == expected and pathlib.Path(args.binary).is_absolute()
    lower = datetime.datetime.fromisoformat(args.since.replace('Z','+00:00'))
    upper = datetime.datetime.fromisoformat(args.through.replace('Z','+00:00')); assert lower <= upper
    db = sqlite3.connect('file:/Users/guanjunhui/.codex/state_5.sqlite?mode=ro', uri=True)
    row = db.execute('SELECT source,cwd,rollout_path,cli_version FROM threads WHERE id=?', (args.root,)).fetchone(); db.close()
    assert row and row[0] == 'cli' and pathlib.Path(row[1]) == REPO
    path = pathlib.Path(row[2]).resolve(); assert pathlib.Path('/Users/guanjunhui/.codex/sessions') in path.parents
    assert re.fullmatch(r'[0-9]+\.[0-9]+\.[0-9]+', row[3])
    def selected():
        for line in path.open():
            event = json.loads(line); at = datetime.datetime.fromisoformat(event['timestamp'].replace('Z','+00:00'))
            if lower <= at <= upper:
                yield event
    result = {'captured_at':datetime.datetime.now(datetime.timezone.utc).isoformat(), 'root_thread':args.root,
              'nonce':args.nonce, 'native_source':'cli', 'expected_experiment_cwd_matches':True, 'cli_version':row[3],
              'window':{'since':args.since,'through':args.through},
              'privacy':'exact owned root; no reasoning/raw text; typed retained outputs require matching literal command or known process join',
              'events':filter_events(selected(), args.root, args.nonce, args.binary, args.dir)}
    pathlib.Path(args.output).write_text(json.dumps(result,ensure_ascii=False,indent=2)+'\n')
    print(json.dumps({'root_thread':args.root,'events':len(result['events']),'output':args.output}))


if __name__ == '__main__':
    main()
