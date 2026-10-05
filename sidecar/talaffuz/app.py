"""Talaffuz tekshirish sayti (FastAPI): ZIPA fonema modeli + Kokoro TTS.

    python app.py        # http://127.0.0.1:7880
"""
import io
import os
import threading
import time

import anyio
import numpy as np
import soundfile as sf
import uvicorn
from fastapi import FastAPI, HTTPException, Request
from fastapi.responses import JSONResponse, Response

import taqqos
import tts
from modellar import SR, ZIPA_REPO, yukla

PAPKA = os.path.dirname(os.path.abspath(__file__))
PORT = 7880
MAKS_SONIYA = 120

ARPA_IPA = {
    "AA": "ɑ", "AE": "æ", "AH": "ʌ", "AO": "ɔ", "AW": "aʊ", "AY": "aɪ", "B": "b", "CH": "tʃ",
    "D": "d", "DH": "ð", "EH": "ɛ", "ER": "ɝ", "EY": "eɪ", "F": "f", "G": "ɡ", "HH": "h",
    "IH": "ɪ", "IY": "i", "JH": "dʒ", "K": "k", "L": "l", "M": "m", "N": "n", "NG": "ŋ",
    "OW": "oʊ", "OY": "ɔɪ", "P": "p", "R": "ɹ", "S": "s", "SH": "ʃ", "T": "t", "TH": "θ",
    "UH": "ʊ", "UW": "u", "V": "v", "W": "w", "Y": "j", "Z": "z", "ZH": "ʒ",
}

model = None
qulf = threading.Lock()
g2p = None


def hammasini_yukla():
    global model, g2p
    t0 = time.time()
    model = yukla()
    print(f"  [zipa] yuklandi ({time.time() - t0:.1f} s)", flush=True)

    import nltk

    for resurs in ("averaged_perceptron_tagger_eng", "cmudict"):
        nltk.download(resurs, quiet=True)
    from g2p_en import G2p

    g2p = G2p()
    tts.kokoro_yukla()
    print("  [kokoro TTS] yuklandi", flush=True)


def lugat_talaffuzi(matn):
    """Matn -> [{"soz": ..., "ipa": ...}] (CMUdict / g2p_en asosida)."""
    if not g2p or not matn.strip():
        return []
    sozlar = [s for s in matn.split() if any(c.isalpha() for c in s)]
    natija = []
    for soz in sozlar:
        ipa = []
        for f in g2p(soz):
            asos = f.rstrip("012")
            if asos not in ARPA_IPA:
                continue
            if f == "AH0":
                ipa.append("ə")
            elif f == "ER0":
                ipa.append("ɚ")
            else:
                ipa.append(ARPA_IPA[asos])
        natija.append({"soz": soz, "ipa": " ".join(ipa)})
    return natija


app = FastAPI()


@app.get("/health")
def health():
    """Compose healthcheck va Go tarafdagi tekshiruv uchun.

    "tayyor" model yuklanganini bildiradi: konteyner ko'tarilgandan keyin
    model yuklanishiga ~5 s ketadi, shu orada so'rov yubormaslik kerak.
    """
    tayyor = model is not None
    return JSONResponse(
        {"status": "ok" if tayyor else "loading", "model": ZIPA_REPO, "tayyor": tayyor},
        status_code=200 if tayyor else 503,
    )


@app.get("/api/holat")
def api_holat():
    return {"model": ZIPA_REPO, "tayyor": model is not None, "ovozlar": tts.KOKORO_OVOZLAR}


@app.get("/api/tts")
async def api_tts(matn: str, ovoz: str = "af_heart", tezlik: float = 1.0):
    """To'g'ri talaffuzni ovoz chiqarib aytadi. Javob: WAV."""
    matn = matn.strip()[:400]
    if not matn:
        raise HTTPException(400, "Matn bo'sh")
    if ovoz not in tts.KOKORO_OVOZLAR:
        raise HTTPException(400, "Bunday ovoz yo'q")
    tezlik = min(1.3, max(0.5, tezlik))
    t0 = time.time()
    try:
        wav = await anyio.to_thread.run_sync(tts.kokoro_ayt, matn, ovoz, tezlik)
    except Exception as e:
        raise HTTPException(500, f"{type(e).__name__}: {e}")
    return Response(
        wav,
        media_type="audio/wav",
        headers={"X-Vaqt": f"{time.time() - t0:.2f}", "Cache-Control": "no-store"},
    )


@app.post("/api/tani")
async def api_tani(request: Request, matn: str = ""):
    """Tana: WAV fayl (brauzer 16 kHz mono qilib yuboradi).

    matn berilsa, tanilgan tovushlar lug'atdagi talaffuz bilan so'zma-so'z solishtiriladi.
    """
    tana = await request.body()
    try:
        audio, sr = sf.read(io.BytesIO(tana), dtype="float32", always_2d=True)
    except Exception:
        raise HTTPException(400, "Audio o'qilmadi (WAV kutilgan)")
    audio = audio.mean(axis=1)
    if sr != SR:
        import librosa

        audio = librosa.resample(audio, orig_sr=sr, target_sr=SR)
    if len(audio) < SR * 0.3:
        raise HTTPException(400, "Audio juda qisqa")
    audio = np.ascontiguousarray(audio[: SR * MAKS_SONIYA], dtype=np.float32)

    def ishla():
        with qulf:  # bir vaqtda bitta so'rov ishlaydi
            t0 = time.time()
            fonemalar, vaqtlar = model.tani(audio)
            return fonemalar, vaqtlar, time.time() - t0

    fonemalar, vaqtlar, vaqt = await anyio.to_thread.run_sync(ishla)
    lugat = lugat_talaffuzi(matn[:1000])
    sozlar = taqqos.solishtir(lugat, fonemalar, tokenlararo_birlashtir=True, vaqtlar=vaqtlar) if lugat else []
    return JSONResponse(
        {
            "fonemalar": fonemalar,
            "sozlar": sozlar,
            "vaqt": round(vaqt, 2),
            "davomiylik": round(len(audio) / SR, 1),
        }
    )


if __name__ == "__main__":
    # Konteyner ichida 0.0.0.0 kerak, lekin port tashqariga chiqarilmaydi:
    # faqat Docker ichki tarmog'idan, speakup-api dan kiriladi.
    hammasini_yukla()
    uvicorn.run(
        app,
        host=os.getenv("HOST", "127.0.0.1"),
        port=int(os.getenv("PORT", str(PORT))),
        log_level="warning",
    )
