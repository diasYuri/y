#!/bin/bash
set -euo pipefail

MODEL_ID='${MODEL_ID}'
NAME='${NAME}'
PORT=${PORT}
VLLM_ARGS="${VLLM_ARGS}"

cd ~
python -m vllm.entrypoints.openai.api_server \
  --model "$MODEL_ID" \
  --port "$PORT" \
  $VLLM_ARGS
