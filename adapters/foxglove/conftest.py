"""Make the adapter importable as ``gantry_foxglove`` and expose the test
helpers (``fake_server``) without an install, mirroring examples/so101/conftest.py.
"""

import os
import sys

HERE = os.path.dirname(os.path.abspath(__file__))
for path in (HERE, os.path.join(HERE, "tests")):
    if path not in sys.path:
        sys.path.insert(0, path)
