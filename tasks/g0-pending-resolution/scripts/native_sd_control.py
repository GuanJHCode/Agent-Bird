"""One fixed sd-qual-01 qualification controller; not a product launcher."""
from native_sd_collect import require_live_processes, same_process

def require_owned_pair(service,tui,socket,original):
    require_live_processes(service,tui)
    if not same_process(service,original['service']) or not same_process(tui,original['tui']):
        raise ValueError('native creation identity changed')
    if not socket.get('exists') or socket.get('service_holds_path') is not True:
        raise ValueError('owned native socket is not live')
    if any(socket.get(k)!=original['socket'].get(k) for k in ('dev','ino','uid','mode')):
        raise ValueError('native socket identity changed')
    return True
