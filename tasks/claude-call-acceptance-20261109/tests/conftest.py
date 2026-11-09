"""Keep test-owned runtime paths short and private; never use real trial state."""
import tempfile
from pathlib import Path
import pytest

@pytest.fixture
def tmp_path():
    with tempfile.TemporaryDirectory(prefix='ab5t-', dir='/private/tmp') as folder:
        yield Path(folder)
