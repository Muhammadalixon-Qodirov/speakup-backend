#!/usr/bin/env bash
# Ensures the model files are present, then starts the API.
#
# Downloads land in /models, which is a named volume: the ~1.5 GB is fetched
# once and survives every later `docker compose up --build`.
set -euo pipefail

KOKORO_DIR=/app/tts_models
KOKORO_BASE="https://github.com/thewh1teagle/kokoro-onnx/releases/download/model-files-v1.0"

mkdir -p "$KOKORO_DIR" /models/hf /models/nltk

# Kokoro TTS (~340 MB). Kept in the image's working dir, symlinked to the
# volume so a rebuilt container does not re-download it.
mkdir -p /models/kokoro
for f in kokoro-v1.0.onnx voices-v1.0.bin; do
    if [ ! -s "/models/kokoro/$f" ]; then
        echo "[entrypoint] downloading $f"
        curl -fsSL "$KOKORO_BASE/$f" -o "/models/kokoro/$f.part"
        mv "/models/kokoro/$f.part" "/models/kokoro/$f"
    fi
    ln -sf "/models/kokoro/$f" "$KOKORO_DIR/$f"
done

# ZIPA (~1.2 GB) is fetched by huggingface_hub into HF_HOME on first load,
# so nothing to do here beyond having the directory.
echo "[entrypoint] models ready, starting API on ${HOST}:${PORT}"
exec python app.py
