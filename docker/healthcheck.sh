#!/bin/sh
set -eu
exec python3 /usr/local/lib/sdn-agent/supervisor.py --healthcheck
