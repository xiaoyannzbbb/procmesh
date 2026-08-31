#!/usr/bin/env bash

# Start a temporary agent in /tmp/procmesh-dev
./bin/procmesh-agent \
  --data-dir /tmp/procmesh-dev \
  --listen 127.0.0.1:18680 \
  --rpc 127.0.0.1:18683 \
  --control 127.0.0.1:18685 \
  --gossip 127.0.0.1:18689 \
  --shim-bin ./bin/procmesh-shim \
  --log-level info