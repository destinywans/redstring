#!/usr/bin/env bash
set -euo pipefail
# This command can delete only the dedicated product lab, never the active context.
kind delete cluster --name aiops-canvas-demo
