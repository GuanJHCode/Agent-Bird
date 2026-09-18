"""Adopt launchd's passive Listener FD without owning its public socket path."""
from __future__ import annotations

import ctypes
import os
import socket


def activate_listener(name: str = 'Listener', *, library=None) -> socket.socket:
    if name != 'Listener':
        raise ValueError('the reviewed launchd socket name is Listener')
    lib = library if library is not None else ctypes.CDLL('/usr/lib/libSystem.B.dylib')
    activate = lib.launch_activate_socket
    activate.argtypes = [ctypes.c_char_p, ctypes.POINTER(ctypes.POINTER(ctypes.c_int)), ctypes.POINTER(ctypes.c_size_t)]
    activate.restype = ctypes.c_int
    free = lib.free
    free.argtypes = [ctypes.c_void_p]
    free.restype = None
    descriptors = ctypes.POINTER(ctypes.c_int)()
    count = ctypes.c_size_t()
    error = activate(name.encode('ascii'), ctypes.byref(descriptors), ctypes.byref(count))
    if error:
        raise OSError(error, 'launch_activate_socket failed')
    owned = [descriptors[index] for index in range(count.value)]
    try:
        if len(owned) != 1 or owned[0] < 0:
            raise ValueError('exactly one Listener FD is required')
        listener = socket.socket(fileno=os.dup(owned[0]))
        os.set_inheritable(listener.fileno(), False)
        return listener
    finally:
        for descriptor in set(owned):
            if descriptor >= 0:
                try:
                    os.close(descriptor)
                except OSError:
                    pass
        if descriptors:
            free(descriptors)
