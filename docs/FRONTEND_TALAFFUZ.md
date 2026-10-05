# Talaffuz mashqi — frontend uchun API shartnomasi

Yangi bo'lim: **o'qib berish orqali talaffuzni tekshirish**. Backend to'liq
tayyor, bu hujjat frontend tomonidan nima qilinishi kerakligini yozadi.

```
mavzu + gap soni tanlanadi → matn chiqadi → ovoz chiqarib o'qiladi →
yuboriladi → so'zma-so'z natija + to'g'ri talaffuz eshittiriladi
```

Bu `/ai/check` (erkin nutq, IELTS bahosi) ni **almashtirmaydi** — yonida
turadigan alohida bo'lim.

---

## ⚠️ Eng muhim ikki qoida

### 1. Audio WAV bo'lishi SHART

Backend `libsndfile` ishlatadi: **WAV, FLAC, OGG, MP3** o'qiladi,
**WebM o'qilmaydi**. Brauzerdagi `MediaRecorder` esa odatda
`audio/webm;codecs=opus` beradi. Ya'ni yozuvni **16 kHz mono WAV** ga
o'zgartirib yuborish kerak.

To'g'ridan-to'g'ri `MediaRecorder` blob'ini yuborsangiz, backend
`400 "Audio o'qilmadi (WAV kutilgan)"` qaytaradi.

Tayyor va sinalgan kod pastda, "Audio konvertatsiyasi" bo'limida.

### 2. Natija "hukm" emas, "maslahat"

Bu UX talabi, chiroyli gap emas. Haqiqiy o'quvchi yozuvlarida o'lchandi:
model to'g'ri aytilgan so'zlarning **23%** ini "xato" deb belgilagan. Backend
buni filtrlab 0% ga tushirdi, lekin fonema darajasidagi aniqlik baribir
mo'rt — shuning uchun:

- ❌ "Xato aytdingiz", ❌ "Siz 3 ta xato qildingiz", ❌ ball/foiz ko'rsatish
- ✅ "Shu so'zni tekshirib ko'ring", ✅ "Taqqoslab eshiting"

Javobda `advisory: true` keladi — bu shuni eslatish uchun.

Tadqiqot: feedback 66% dan kam to'g'ri bo'lsa, o'qishga **zarar** beradi.
Ishonchsiz "xato" belgisi foydalanuvchi ishonchini yo'qotadi.

---

## Endpointlar

Hammasi `Authorization` talab qiladi (mavjud auth bilan bir xil).
Bazaviy yo'l: `/pronunciation`.

### 1. `GET /pronunciation/options`

Tanlov oynasini qurish uchun. Hech narsani frontendga qotirib yozish kerak emas.

```json
{
  "success": true,
  "data": {
    "topics": ["work","study","family","travel","food",
               "technology","sport","environment","hobbies","city"],
    "sentence_counts": [1, 2, 3, 4],
    "level": "B1",
    "available": true,
    "ready": true
  }
}
```

| Maydon | Ma'nosi |
|---|---|
| `topics` | Mavzular ro'yxati. **Erkin matn qabul qilinmaydi** |
| `sentence_counts` | Ruxsat etilgan gap soni. 1 gap ≈ 6 s, 4 gap ≈ 20 s audio |
| `level` | Foydalanuvchining CEFR darajasi (profilidan) |
| `available` | Funksiya yoqilganmi |
| `ready` | Model yuklanib bo'ldimi |

**`ready: false` holatini albatta boshqaring.** Server qayta ishga tushganda
model yuklanishi bir necha daqiqa oladi (birinchi marta 10-15 daqiqa, 1.5 GB
yuklaydi). Shu paytda "Talaffuz bo'limi tayyorlanmoqda, bir ozdan keyin urinib
ko'ring" ko'rsating.

### 2. `GET /pronunciation/passage`

| Parametr | Majburiy | Qiymat |
|---|---|---|
| `topic` | yo'q | `topics` dan biri. Bo'sh — istalgan mavzu |
| `sentences` | yo'q | 1-4, standart 2 |
| `level` | yo'q | A1..C2, standart — foydalanuvchi darajasi |

```json
{
  "success": true,
  "data": {
    "id": "8f3a...-...",
    "text": "My brother works in a small shop near the park. He starts work very early every morning.",
    "sentence_count": 2,
    "word_count": 18,
    "level": "A2",
    "topic": "family",
    "target_sounds": ["AE", "IY_IH", "W"]
  }
}
```

`id` ni saqlang — yuborishda kerak.

**Bir xil parcha ikki marta kelmaydi.** Backend har foydalanuvchiga ko'rsatilgan
parchalarni belgilab boradi, shuning uchun "yangi matn" tugmasi bosilganda
hamisha boshqa matn keladi.

`target_sounds` — shu matn qaysi tovushlarni mashq qildiradi. Ixtiyoriy,
"Bugun mashq qilasiz: `th`, `w`" ko'rinishida ko'rsatish mumkin:

| Kod | Tovush | Misol |
|---|---|---|
| `TH` | /θ/ | think, three |
| `DH` | /ð/ | this, father |
| `W` | /w/ | west, win |
| `V` | /v/ | very, love |
| `AE` | /æ/ | cat, bad |
| `IY_IH` | /iː/ ~ /ɪ/ | sheep / ship |
| `FINAL_VOICED` | so'z oxirida jaranglilik | lab, bad |

**Xato holati:** `503` — bu daraja uchun matn hovuzi hali bo'sh. Matn kuniga bir
marta cron bilan yig'iladi, shuning uchun yangi o'rnatishdan keyin bir kun
kutish kerak bo'lishi mumkin. Xabar: *"Hozircha bu daraja uchun matn tayyor
emas"*.

### 3. `POST /pronunciation/attempt`

`multipart/form-data`:

| Maydon | Qiymat |
|---|---|
| `passage_id` | 2-qadamdan olingan `id` |
| `audio` | **16 kHz mono WAV** fayl |

```json
{
  "success": true,
  "data": {
    "passage_id": "8f3a...",
    "text": "My brother works in a small shop near the park. ...",
    "words": [
      {
        "soz": "brother",
        "kutilgan": "b ɹ ʌ ð ɚ",
        "eshitilgan": "b ɹ ʌ z ɚ",
        "holat": "xato",
        "izohlar": [
          {"tur": "xato", "matn": "«th (this)» o'rniga «z» eshitildi. Tilni tishlar orasiga qo'yib ayting."}
        ],
        "vaqt": [1.12, 1.58],
        "almashtirishlar": [{"kutilgan": "ð", "eshitilgan": "z", "oxirida": false}]
      },
      {
        "soz": "works",
        "kutilgan": "w ɝ k s",
        "eshitilgan": "w ɝ k s",
        "holat": "ok",
        "izohlar": [],
        "vaqt": [1.60, 1.95],
        "almashtirishlar": []
      }
    ],
    "phonemes": ["m","aɪ","b","ɹ","ʌ","z","ɚ", "..."],
    "duration": 8.4,
    "took": 0.81,
    "summary": {"total": 18, "wrong": 1, "softened": 2},
    "advisory": true
  }
}
```

#### `holat` — uch qiymat, uch xil ko'rinish

| `holat` | Ma'nosi | Tavsiya etilgan ko'rinish |
|---|---|---|
| `ok` | Muammo yo'q | Oddiy rang, belgi yo'q |
| `kichik` | Unli biroz boshqacha, **yoki** ishonchsiz signal | Kulrang/sariq nuqta, ustiga bosilsa izoh |
| `xato` | Yuqori ishonchli xato | Sariq/to'q sariq. **Qizil ishlatmang** |

`summary.softened` — backend ishonchsiz deb pasaytirgan belgilar soni.
Foydalanuvchiga ko'rsatilmaydi, faqat debug uchun.

#### `izohlar` — o'zbek tilida tayyor

`matn` maydoni allaqachon o'zbekcha va amaliy maslahat bilan. Tarjima qilish
yoki qayta yozish kerak emas, shundayligicha ko'rsatiladi.

#### `vaqt` — eng qimmatli maydon

`[boshlanish, tugash]` — sekundlarda, **foydalanuvchining o'z yozuvida**. Bu
"o'z ovozini eshitish" funksiyasini beradi: so'z bosilganda avval foydalanuvchi
o'sha so'zni qanday aytgani, keyin to'g'risi eshittiriladi. Taqqoslash eng
kuchli o'rganish vositasi.

`null` bo'lishi mumkin (so'z topilmasa) — tekshirib ishlating.

#### Xato holatlari

| Kod | Sabab | Nima qilish |
|---|---|---|
| `400` | `Audio juda qisqa` | Kamida 0.3 s yozish kerak |
| `400` | `Audio o'qilmadi (WAV kutilgan)` | WAV ga o'tkazilmagan |
| `422` | Sidecar javob bermadi | "Qayta urinib ko'ring" |
| `503` | Model hali tayyor emas | "Tayyorlanmoqda" |

### 4. `GET /pronunciation/passage/:id/reference`

To'liq matnning to'g'ri talaffuzi. **WAV qaytaradi** (`audio/wav`), odatda
`302` bilan statik faylga yo'naltiradi — `fetch` da `redirect: 'follow'`
(standart) yetarli, yoki to'g'ridan-to'g'ri `<audio src="...">` ga berish mumkin.

| Parametr | Standart | Izoh |
|---|---|---|
| `voice` | `af_heart` | `af_heart`, `am_michael` (Amerika), `bf_emma`, `bm_george` (Britaniya) |
| `speed` | `0.85` | 0.5-1.3 |

Standart tezlik 1.0 dan past — maqsad taqlid qilish mumkin bo'lishi.

Standart ovoz **amerikacha**, va buni o'zgartirmaslik tavsiya etiladi: etalon
talaffuz amerikacha lug'atdan (CMUdict) olinadi, britancha ovoz esa baholanadigan
etalonga mos kelmaydi.

Parametrsiz so'rov keshlanadi (`max-age=604800, immutable`) — parcha matni
o'zgarmaydi, shuning uchun bir marta yuklab olinsa yetadi.

**Alohida so'zni eshittirish** uchun bu endpoint emas, sidecar'ning TTS'i kerak
bo'ladi. Hozir bu endpoint faqat to'liq matn uchun. Alohida so'z kerak bo'lsa
ayting — qo'shiladi.

---

## Audio konvertatsiyasi — tayyor kod

Bu kod prototipda sinalgan (`tallafuz_ai/index.html`), shundayligicha
ishlatish mumkin. `sonPcm` ni saqlab qoling — so'z bo'lagini qayta eshittirish
uchun kerak.

```js
let sonPcm = null;  // oxirgi yozuv (16 kHz mono float32)

// Istalgan audio blob -> 16 kHz mono WAV
async function wavQil(blob) {
  const AC = window.AudioContext || window.webkitAudioContext;
  const ctx = new AC();
  const buf = await ctx.decodeAudioData(await blob.arrayBuffer());
  ctx.close();
  const SR = 16000;
  const off = new OfflineAudioContext(1, Math.ceil(buf.duration * SR), SR);
  const src = off.createBufferSource();
  src.buffer = buf; src.connect(off.destination); src.start();
  sonPcm = (await off.startRendering()).getChannelData(0);
  return wavKodla(sonPcm);
}

// 16 kHz mono Float32 PCM -> WAV blob
function wavKodla(pcm) {
  const SR = 16000;
  const dv = new DataView(new ArrayBuffer(44 + pcm.length * 2));
  const yoz = (o, s) => { for (let i = 0; i < s.length; i++) dv.setUint8(o + i, s.charCodeAt(i)); };
  yoz(0, 'RIFF'); dv.setUint32(4, 36 + pcm.length * 2, true); yoz(8, 'WAVE'); yoz(12, 'fmt ');
  dv.setUint32(16, 16, true); dv.setUint16(20, 1, true); dv.setUint16(22, 1, true);
  dv.setUint32(24, SR, true); dv.setUint32(28, SR * 2, true);
  dv.setUint16(32, 2, true); dv.setUint16(34, 16, true);
  yoz(36, 'data'); dv.setUint32(40, pcm.length * 2, true);
  for (let i = 0; i < pcm.length; i++) {
    const s = Math.max(-1, Math.min(1, pcm[i]));
    dv.setInt16(44 + i * 2, s < 0 ? s * 0x8000 : s * 0x7FFF, true);
  }
  return new Blob([dv.buffer], { type: 'audio/wav' });
}
```

### Yozishdan yuborishgacha

```js
const stream = await navigator.mediaDevices.getUserMedia({ audio: true });
const rec = new MediaRecorder(stream);
const bo = [];
rec.ondataavailable = e => bo.push(e.data);
rec.onstop = async () => {
  const wav = await wavQil(new Blob(bo));          // <- MUHIM
  const fd = new FormData();
  fd.append('passage_id', passage.id);
  fd.append('audio', wav, 'audio.wav');
  const r = await fetch('/pronunciation/attempt', {
    method: 'POST',
    headers: { Authorization: `Bearer ${token}` },  // Content-Type QO'YMANG
    body: fd,
  });
};
```

`FormData` bilan `Content-Type` ni **qo'lda qo'ymang** — brauzer `boundary`
bilan o'zi qo'yadi.

### So'zni bosganda: avval o'zi, keyin to'g'risi

```js
async function sozniEshit(soz) {
  if (sonPcm && soz.vaqt) {
    const a = Math.floor(soz.vaqt[0] * 16000);
    const b = Math.ceil(soz.vaqt[1] * 16000);
    toast(`«${soz.soz}» — siz aytganingiz`);
    await ijro(wavKodla(sonPcm.slice(a, b)));
    await kut(350);
  }
  toast(`«${soz.soz}» — to'g'risi`);
  // to'liq matn etaloni yoki alohida so'z TTS'i
}
```

---

## Mikrofon: HTTPS shart

`getUserMedia` faqat `https://` yoki `localhost` da ishlaydi. Telegram Mini App
HTTPS da ochiladi, shuning uchun prodda muammo yo'q — lekin lokal ishlab
chiqishda `localhost` ishlating, IP manzil orqali mikrofon ishlamaydi.

---

## Tavsiya etilgan ekran oqimi

```
┌─ Tanlov ────────────────────────────┐
│ Mavzu:  [Oila ▾]                    │
│ Uzunlik: (1) (2) (3) (4) gap        │
│          [ Matn olish ]             │
└─────────────────────────────────────┘
          ↓
┌─ O'qish ────────────────────────────┐
│ "My brother works in a small shop   │
│  near the park. He starts work      │
│  very early every morning."         │
│                                     │
│ 🔊 Namunani eshitish                │
│ ⏺ Yozish / ⏹ To'xtatish             │
└─────────────────────────────────────┘
          ↓
┌─ Natija ────────────────────────────┐
│ My ·brother· works in a small shop  │
│      ↑ bosilsa taqqoslab eshitadi   │
│                                     │
│ «th» o'rniga «z» eshitildi.         │
│ Tilni tishlar orasiga qo'yib ayting.│
│                                     │
│ [ Qayta o'qish ]  [ Yangi matn ]    │
└─────────────────────────────────────┘
```

"Qayta o'qish" — o'sha `passage_id` ga yana yuborish (ruxsat etilgan).
"Yangi matn" — `GET /passage` ni qayta chaqirish.

### Nimalarni ko'rsatmaslik kerak

- Umumiy ball, foiz, IELTS band — backend ularni **bermaydi**, chunki akustik
  belgilardan IELTS bandga tasdiqlangan xarita yo'q
- "Siz N ta xato qildingiz" — `summary.wrong` bor, lekin uni shunday
  ko'rsatmang; so'zlarning o'zini belgilash yetarli
- Qizil rang va ❌ belgisi — signal ishonchsiz, ko'rinish ham yumshoq bo'lsin

---

## Backendda nima qo'shildi (qisqacha)

Frontendga bevosita ta'sir qilmaydi, lekin kontekst uchun:

- `pronunciation_passages` — matn hovuzi, kunlik cron bilan Groq yozadi,
  8 ta filtr (har so'z CMUdict'da bo'lishi shart)
- `user_passage_seen` — takrorlanmaslik
- `user_pronunciation_words` — xato aytilgan so'z 3 kun / 1 hafta / 2 hafta /
  1 oydan keyin qaytadi
- `speakup-talaffuz` konteyneri — ZIPA fonema modeli + Kokoro TTS, CPU'da
- Ishonch siyosati — ishonchsiz "xato" belgilarini "kichik" ga tushiradi

Batafsil: `docs/TALAFFUZ.md`.

---

## Savollar bo'lsa

- Alohida so'z uchun TTS endpoint kerak bo'lsa — qo'shiladi
- Mashq tarixi (foydalanuvchi qaysi tovushlarda zaif) ekrani kerak bo'lsa,
  ma'lumot bazada bor, endpoint qo'shish oson
- `target_sounds` bo'yicha filtr ("faqat `th` mashqi") kerak bo'lsa ham mumkin
