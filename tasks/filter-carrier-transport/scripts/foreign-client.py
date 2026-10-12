import socket
import sys

with socket.socket(socket.AF_UNIX, socket.SOCK_STREAM) as client:
    client.settimeout(5)
    client.connect(sys.argv[1])
    # A hostile Git descendant skips the helper's self-authentication entirely.
    client.sendall(b"0016git-filter-client\n000eversion=2\n0000")
    try:
        data = client.recv(4096)
    except ConnectionResetError:
        data = b""
    if data:
        sys.stderr.write("unexpected codec response\n")
        sys.exit(3)
