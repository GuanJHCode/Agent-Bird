"""Compatibility entry point for historical experiments; implementation is runtime/native."""
from pathlib import Path as _RuntimePath
import sys as _runtime_sys
_runtime_file = _RuntimePath(__file__).resolve().parents[3] / "runtime/native/receipt_store.py"
__file__ = str(_runtime_file)
exec(compile(_runtime_file.read_bytes(), __file__, "exec"), globals(), globals())
