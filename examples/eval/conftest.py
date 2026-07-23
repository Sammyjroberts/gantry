"""Put this example's modules on the path so `python -m pytest examples/eval`
imports gantry_eval / so101_sim_gate without an install."""

import os
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
