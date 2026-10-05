"""ZIPA fonema tanuvchi modeli (Zipformer CR-CTC, ONNX, CPU).

Foydalanish:
    from modellar import yukla, audio_oqi
    m = yukla()
    fonemalar, vaqtlar = m.tani(audio_oqi("gap.wav"))   # audio: 16 kHz mono float32
"""
import os

import numpy as np

ZIPA_REPO = "anyspeech/zipa-large-crctc-ns-800k"
SR = 16000


def audio_oqi(yol):
    """Istalgan audio faylni 16 kHz mono float32 ga o'qiydi."""
    import librosa

    audio, _ = librosa.load(yol, sr=SR, mono=True)
    return audio.astype(np.float32)


def ctc_dekod(ids, blank, tokli, davomiylik):
    """CTC greedy dekodlash, har bir token uchun vaqt oralig'i bilan.

    ids: har bir kadr uchun eng ehtimolli token id; tokli(id) -> token matni yoki "" (tashlanadi).
    Qaytaradi: (tokenlar, vaqtlar), vaqtlar[i] = [boshlanish_s, tugash_s].
    """
    kadr = davomiylik / max(1, len(ids))
    tokenlar, vaqtlar, oldingi, joriy = [], [], None, False
    for t, i in enumerate(ids):
        if i != oldingi:
            joriy = False
            if i != blank:
                tok = tokli(i)
                if tok:
                    tokenlar.append(tok)
                    vaqtlar.append([t * kadr, (t + 1) * kadr])
                    joriy = True
        elif joriy:
            vaqtlar[-1][1] = (t + 1) * kadr
        oldingi = i
    return tokenlar, vaqtlar


class ZipaModel:
    qurilma = "cpu"

    def __init__(self, repo=ZIPA_REPO):
        import kaldi_native_fbank as knf
        import onnxruntime as ort
        from huggingface_hub import hf_hub_download

        # ZIPA 80 o'lchamli Kaldi fbank kutadi (lhotse Fbank(num_filters=80, dither=0,
        # snip_edges=False) bilan bir xil natija beradi, lekin torch talab qilmaydi)
        self.fbank_opts = knf.FbankOptions()
        self.fbank_opts.frame_opts.samp_freq = SR
        self.fbank_opts.frame_opts.dither = 0.0
        self.fbank_opts.frame_opts.snip_edges = False
        self.fbank_opts.mel_opts.num_bins = 80
        self.fbank_opts.mel_opts.high_freq = -400
        self._knf = knf

        self.id2tok = {}
        with open(hf_hub_download(repo, "tokens.txt"), encoding="utf-8") as f:
            for qator in f:
                qism = qator.strip().split()
                if qism:
                    self.id2tok[int(qism[1]) if len(qism) > 1 else len(self.id2tok)] = qism[0]
        opts = ort.SessionOptions()
        # Pinned, not derived from os.cpu_count(): measured on a 4-core box,
        # 4 threads ran the 20 s case in 1.85 s while 8 threads took 3.13 s -
        # oversubscribing the cores costs about half the speed. ZIPA_THREADS
        # also keeps the container inside its cpus= budget so coturn keeps
        # enough CPU for live voice rooms.
        opts.intra_op_num_threads = int(os.getenv("ZIPA_THREADS", "4"))
        self.sess = ort.InferenceSession(
            hf_hub_download(repo, "model.onnx"), opts, providers=["CPUExecutionProvider"]
        )

    def _fbank(self, audio):
        f = self._knf.OnlineFbank(self.fbank_opts)
        f.accept_waveform(SR, audio.tolist())
        f.input_finished()
        return np.stack([f.get_frame(i) for i in range(f.num_frames_ready)]).astype(np.float32)

    def _tok(self, i):
        # SentencePiece so'z boshi belgisi va maxsus tokenlarni tashlaymiz
        tok = self.id2tok.get(i, "").replace("▁", "")
        return tok if tok and not (tok.startswith("<") and tok.endswith(">")) else ""

    def tani(self, audio):
        """audio -> (fonema tokenlari, har birining [bosh, oxir] vaqti)."""
        feat = self._fbank(audio)
        lens = np.array([feat.shape[0]], dtype=np.int64)
        log_probs = self.sess.run(None, {"x": feat[None], "x_lens": lens})[0][0]
        return ctc_dekod(log_probs.argmax(-1).tolist(), 0, self._tok, len(audio) / SR)


def yukla():
    return ZipaModel()
