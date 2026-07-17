"""Put this example's modules on the path so `python -m pytest examples/so101`
imports so101_bridge / so101_ports / so101_lerobot_bridge without an install."""

import os
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
