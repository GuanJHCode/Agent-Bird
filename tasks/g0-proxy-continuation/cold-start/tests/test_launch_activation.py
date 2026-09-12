"""C ABI activation adapter with real owned FDs; never contacts launchd."""
import ctypes
import errno
import os
from pathlib import Path
import socket
import sys
import tempfile

import pytest

SCRIPTS = Path(__file__).resolve().parents[1] / 'scripts'
sys.path.insert(0, str(SCRIPTS))
try:
    import launch_activation as activation
except ModuleNotFoundError:
    activation = None


def module():
    assert activation is not None, 'launch_activate_socket adapter has not been implemented'
    return activation


class Function:
    def __init__(self, call): self.call = call
    def __call__(self, *args): return self.call(*args)


class LaunchAPI:
    def __init__(self, descriptors, error=0):
        self.descriptors = descriptors
        self.error = error
        self.names = []
        self.freed = 0
        self.array = (ctypes.c_int * len(descriptors))(*descriptors)
        self.launch_activate_socket = Function(self.activate)
        self.free = Function(self.free_array)

    def activate(self, name, output, count):
        self.names.append(name)
        if self.error: return self.error
        ctypes.cast(output, ctypes.POINTER(ctypes.POINTER(ctypes.c_int)))[0] = ctypes.cast(self.array, ctypes.POINTER(ctypes.c_int))
        ctypes.cast(count, ctypes.POINTER(ctypes.c_size_t))[0] = len(self.descriptors)
        return 0

    def free_array(self, pointer):
        assert ctypes.cast(pointer, ctypes.c_void_p).value == ctypes.addressof(self.array)
        self.freed += 1


def test_launch_activate_socket_abi_keeps_queued_connection_and_frees_array(monkeypatch):
    p = module()
    with tempfile.TemporaryDirectory(prefix='g0-auth-', dir='/private/tmp') as raw:
        path = str(Path(raw)/'p.sock')
        listener = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM); listener.bind(path); listener.listen(4)
        client = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM); client.connect(path); client.sendall(b'queued-wire')
        transferred = os.dup(listener.fileno()); api = LaunchAPI([transferred])
        try:
            adopted = p.activate_listener('Listener', library=api)
            assert api.names == [b'Listener'] and api.freed == 1
            accepted, _ = adopted.accept()
            assert accepted.recv(32) == b'queued-wire'
            accepted.close(); adopted.close()
            assert os.path.exists(path)
            with pytest.raises(OSError): os.fstat(transferred)
        finally:
            client.close(); listener.close()


def test_multiple_activation_fds_fail_closed_without_leaking_them():
    p = module()
    left, right = socket.socketpair()
    owned = [os.dup(left.fileno()), os.dup(right.fileno())]; api = LaunchAPI(owned)
    try:
        with pytest.raises(ValueError, match='one'):
            p.activate_listener('Listener', library=api)
        assert api.freed == 1
        for fd in owned:
            with pytest.raises(OSError): os.fstat(fd)
    finally:
        left.close(); right.close()


def test_launch_api_error_does_not_invent_a_listener():
    p = module(); api = LaunchAPI([], errno.ESRCH)
    with pytest.raises(OSError) as failure:
        p.activate_listener('Listener', library=api)
    assert failure.value.errno == errno.ESRCH and api.freed == 0
