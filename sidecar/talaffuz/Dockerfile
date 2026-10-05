# Pronunciation sidecar: ZIPA phoneme recogniser + Kokoro TTS, CPU only.
#
# The models (~1.5 GB) are NOT baked into the image. They are downloaded on
# first start into a named volume, because every deploy runs
# `docker compose up -d --build` and baking them in would re-download or
# re-copy 1.5 GB on each one. The volume survives rebuilds, so the download
# happens once.
FROM python:3.13-slim

# espeak-ng: kokoro-onnx phonemises through it. libsndfile: soundfile.
# Note on licensing - espeak-ng is GPL-3.0. We run it as a server-side
# process and distribute nothing, so its copyleft terms are not triggered,
# but swapping TTS engines is the clean fix if that ever changes.
RUN apt-get update && apt-get install -y --no-install-recommends \
        espeak-ng \
        libsndfile1 \
        curl \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /app

# Requirements first so the heavy pip layer is cached across code changes.
COPY requirements.txt .
RUN pip install --no-cache-dir -r requirements.txt

COPY modellar.py taqqos.py tts.py app.py entrypoint.sh ./
RUN chmod +x entrypoint.sh

# Model cache lives on volumes mounted at these paths.
ENV HF_HOME=/models/hf \
    HOST=0.0.0.0 \
    PORT=7880 \
    ZIPA_THREADS=4 \
    NLTK_DATA=/models/nltk

EXPOSE 7880

# The healthcheck returns 503 until the model has finished loading (~5 s),
# so compose will not route traffic at a container that cannot answer yet.
HEALTHCHECK --interval=10s --timeout=5s --start-period=180s --retries=3 \
    CMD curl -fsS http://127.0.0.1:7880/health || exit 1

ENTRYPOINT ["./entrypoint.sh"]
