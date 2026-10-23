#!/usr/bin/env bash
#
# connector/setup.sh - optional. REPLACE OR DELETE THIS FILE.
#
# Invoked ONCE before the scenario series, with network access allowed. Build
# your connector, install dependencies, generate whatever run.sh needs. run.sh
# itself runs with no network access beyond the two local servers, so anything
# that needs to be downloaded is downloaded here.
#
# Requirements:
#   - idempotent: running it twice is harmless
#   - exits non-zero when the build fails, so a broken build is a harness event
#     and not a silently empty run
#   - prints what it did, and never prints a credential
#
# Examples:
#   go build -o bin/connector ./cmd/connector
#   python3 -m venv .venv && .venv/bin/pip install -r requirements.txt
#   npm ci
#   dotnet publish -c Release -o bin
#
# As shipped it does nothing and succeeds, which is correct for a connector that
# needs no build step.

set -euo pipefail

echo "setup.sh: nothing to build"
