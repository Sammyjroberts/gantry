"""Generic Foxglove-WebSocket -> Gantry tap.

Subscribes to an open ``foxglove.websocket.v1`` server (lerobot's foxglove-sdk
backend, ROS 2 ``foxglove_bridge``, or anything else speaking the protocol),
maps advertised channels to Gantry devices/packets/channels via a config-driven
rule set, and publishes over Gantry's plain-HTTP JSON ingest. Image topics can
optionally be teed to Gantry's video lane as rolling MP4 chunks.

See ``README.md`` for the quickstart and mapping-config format.
"""

__all__ = ["__version__"]

__version__ = "0.1.0"
