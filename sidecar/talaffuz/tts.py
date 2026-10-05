"""Kokoro TTS (ONNX, CPU): to'g'ri talaffuzni ovoz chiqarib aytadi."""
import io
import os
import threading

import soundfile as sf

PAPKA = os.path.dirname(os.path.abspath(__file__))
MODEL_PAPKA = os.path.join(PAPKA, "tts_models")

# Kokoro ovozlari: a = Amerika, b = Britaniya; f = ayol, m = erkak
KOKORO_OVOZLAR = {
    "af_heart": "Amerika, ayol (Heart)",
    "am_michael": "Amerika, erkak (Michael)",
    "bf_emma": "Britaniya, ayol (Emma)",
    "bm_george": "Britaniya, erkak (George)",
}

_kokoro = None
_qulf = threading.Lock()


def kokoro_yukla():
    global _kokoro
    if _kokoro is None:
        from kokoro_onnx import Kokoro

        _kokoro = Kokoro(
            os.path.join(MODEL_PAPKA, "kokoro-v1.0.onnx"),
            os.path.join(MODEL_PAPKA, "voices-v1.0.bin"),
        )
    return _kokoro


def kokoro_ayt(matn, ovoz="af_heart", tezlik=1.0):
    """Matn -> WAV baytlari."""
    k = kokoro_yukla()
    til = "en-gb" if ovoz.startswith("b") else "en-us"
    with _qulf:
        audio, sr = k.create(matn, voice=ovoz, speed=tezlik, lang=til)
    buf = io.BytesIO()
    sf.write(buf, audio, sr, format="WAV", subtype="PCM_16")
    return buf.getvalue()
