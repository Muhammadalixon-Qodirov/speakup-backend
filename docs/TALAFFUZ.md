# Talaffuz mashqi (o'qib berish rejimi)

Foydalanuvchi mavzu va gap sonini tanlaydi → matn chiqadi → ovoz chiqarib
o'qiydi → so'zma-so'z natija oladi, yonida to'g'ri talaffuz eshittiriladi.

Bu `/ai/check` (erkin nutq) ni almashtirmaydi, yonida turadi. Sababi raqamli:
matn oldindan ma'lum bo'lsa fonema darajasidagi muvofiqlik **0.48 → 0.66** ga
chiqadi, chunki to'g'ri talaffuz lug'atdan **olinadi**, modelning taxminidan
emas. Batafsil: `tallafuz_ai/docs/TADQIQOT.md`.

---

## ⚠️ Eng muhim fakt: xom natija ishonchsiz

Inson tomonidan baholangan 4 ta speechocean762 yozuvida o'lchandi
(22 ta so'z, hammasi odam tomonidan **to'g'ri** deb belgilangan):

| | Natija |
|---|---|
| Xom sidecar chiqishi | **5 ta so'z "xato" deb belgilandi = 23% yolg'on signal** |
| Haqiqiy topilgan xato | **0** |
| Ishonch siyosatidan keyin | **0 yolg'on signal** |

Tadqiqotga ko'ra feedback **~66% dan kam to'g'ri** bo'lsa, o'qishga **zarar
beradi** (33% aniqlikdagi feedback 100% dan yomon natija bergan). Shuning uchun
xom natijani foydalanuvchiga ko'rsatish mumkin emas.

Yolg'on signallarning hammasi bir xil shaklda edi — model akustikani to'g'ri
eshitgan, lekin xulosani xato chiqargan:

| So'z | Kutilgan → Eshitilgan | Haqiqiy sabab |
|---|---|---|
| `WIN` | `w → u` | Bir xil artikulyatsiya, model boshqacha yozgan |
| `IMPORTANT` | `m → n` | `/p/` oldidan assimilyatsiya — normal ingliz tili |
| `IMPORTANT` | `ɹ → ʊ` | `r` vokalizatsiyasi — normal |
| `LAST` | `l → n` | Model chalkashligi |
| `DO` | `d → t` | So'z boshidagi aspiratsiya |
| `WAS` | `z → s` | Yordamchi so'zning tabiiy qisqarishi (`was an`) |

**Yechim:** `internal/services/pronunciation_confidence.go`. Xato faqat quyidagi
hollarda ko'rsatiladi:

- `θ→s/t/f`, `ð→z/d` (think→sink, this→zis)
- `w↔v`, `v→f` (west→vest)
- So'z **oxirida** jaranglilikni yo'qotish, faqat **mazmunli** so'zlarda
  (lab→lap). Yordamchi so'zlarda ingliz tilining o'zi qisqartiradi.

Qolgan hammasi `kichik` ga tushiriladi — ko'rsatiladi, eshittiriladi, lekin
"xato" deb aytilmaydi. Siyosat **faqat pasaytiradi**, hech qachon ko'tarmaydi.

Bu o'lchov doimiy test: `TestConfidencePolicyOnRealAudio`
(`testdata/speechocean762_verdicts.json` — haqiqiy sidecar chiqishi).

**Halol cheklovlar:** 22 so'z juda kichik namuna. Yozuvlar mandarin tilli
o'quvchilardan, o'zbek tilli emas. Namunalarda inson belgilagan xato bo'lmagani
uchun **filtr haqiqiy xatoni ham bosib qo'yishi mumkinligi o'lchanmagan** —
`TestApplyConfidenceKeepsRealErrors` buni faqat sun'iy holatlarda tekshiradi.

---

## Tuzilishi

```
Mini App
   │  GET  /pronunciation/options        mavzular, 1-4, daraja
   │  GET  /pronunciation/passage        ko'rilmagan parcha
   │  POST /pronunciation/attempt        audio + passage_id
   │  GET  /pronunciation/passage/:id/reference   etalon ovoz
   ▼
speakup-api (Go)
   │  HTTP (faqat ichki tarmoq)
   ▼
speakup-talaffuz (Python/FastAPI)
   ZIPA fonema modeli (ONNX, CPU) + Kokoro TTS
```

Sidecar hostga **chiqarilmagan** — faqat `speakup-api` undan foydalanadi.

## Matn qayerdan keladi

Kitob yoki maqoladan **emas**. Sababi: CMUdict'da bo'lmagan so'zning talaffuz
etaloni `g2p_en` tomonidan taxmin qilinadi, taxmin xato bo'lsa foydalanuvchi
to'g'ri aytganida ham "xato" oladi. Kitoblarda atoqli ism va eskirgan so'z ko'p,
daraja nazoratsiz, mualliflik huquqi muammoli.

Groq (`openai/gpt-oss-120b`) yozadi, biz **8 ta filtr**dan o'tkazamiz
(`pronunciation_generate.go`):

| # | Filtr |
|---|---|
| 1 | Qo'shtirnoq va g'alati belgilar yo'q (apostrof ruxsat — `don't`) |
| 2 | Gap soni so'ralganiga teng |
| 3 | Har gapda 5-14 so'z |
| 4 | **Har so'z CMUdict'da** — aniqlik kafolati |
| 5 | Atoqli ism yo'q (CEFR darajasi bor bosh harfli so'z — ruxsat: `Monday`) |
| 6 | Daraja haqiqatan to'g'ri (`cefr.tsv` bilan o'lchanadi, LLM so'ziga ishonilmaydi) |
| 7 | Gaplar qovushgan (kuchsiz tekshiruv, faqat ochiq mantiqsizlikni tutadi) |
| 8 | Dublikat emas |

Lug'at: `assets/pronunciation_dict.tsv` — 124 911 so'z, CMUdict fonemalari,
8 225 tasida CEFR darajasi. Qayta yasash:
`python3 scripts/gen_pronunciation_dict.py`.

### Hovuz matematikasi

```
10 mavzu × 5 daraja (A1..C1) × 4 uzunlik = 200 savat × 20 = ~4000 parcha
```

Takrorlanishni **hajm emas, kuzatuv** oldini oladi: `user_passage_seen`. Bir xil
parcha turli foydalanuvchilarga berilishi normal. Kuniga 3 mashqda bir odamga
~3.5 yil yetadi.

**So'zlar esa ataylab takrorlanadi.** Xato aytilgan so'z 3 kun, 1 hafta, 2 hafta,
1 oydan keyin qaytadi (`user_pronunciation_words.next_due_at`) — alohida
tovushlarga qaratilgan takroriy mashq adabiyotda eng katta effekt bergan yagona
narsa.

## Cron

| Vaqt (UTC) | Ish |
|---|---|
| 03:30 kunlik | `ReplenishPronunciation` — hovuzni to'ldiradi, ~320/kun |
| :15 har soat | `RenderPendingReferences` — etalon ovozni oldindan yasaydi |

Etalon ovoz bir marta yasalib faylga yoziladi (`UPLOAD_DIR/pronunciation/`), ish
vaqtida TTS **umuman ishlamaydi** — sidecar'ning yadrolari baholashga qoladi.

## Resurslar (o'lchangan, taxmin emas)

Lokal i5-1035G1, 4 thread:

| Audio | Tanish vaqti |
|---|---|
| 6 s (1 gap) | 0.48 s |
| 20 s (4 gap) | **1.85 s** |
| 30 s | 3.53 s |

RAM: model 1.5 GB, eng yuqori **2.06 GB**.

**4 thread 8 thread'dan 2 barobar tez** — yadrolarni ortiqcha yuklash tezlikni
yarmiga tushiradi. Shuning uchun `ZIPA_THREADS=4` aniq yozilgan,
`os.cpu_count()` ga tashlab ketilmagan.

`cpus: 2` chegarasi muhim: `speakup-coturn` shu serverda **tirik ovozli
xonalarni** uzatadi va kechikishga sezgir. Talaffuz tekshiruvi uzilgan
qo'ng'iroqdan arzonroq.

## Birinchi ishga tushirish

Sidecar birinchi marta ko'tarilganda ~1.5 GB model yuklaydi (ZIPA 1.15 GB +
Kokoro 340 MB) va `speakup_talaffuz_models` volume'ida saqlaydi. Shu sababli:

- Birinchi start **bir necha daqiqa** oladi; `/health` shu vaqt **503** qaytaradi
- `app` xizmati unga `service_started` bilan bog'langan, `service_healthy` emas —
  qolgan API model yuklanishini kutmaydi
- Talaffuz endpointlari shu orada "hozir ishlamayapti" deb javob beradi
- Keyingi `up -d --build` larda model qayta yuklanmaydi

## Litsenziya xavfi — hal qilinmagan

**ZIPA og'irliklari:** GitHub'da MIT, HuggingFace'da `cc-by-nc-4.0`
(**notijorat**) deb belgilangan. SpeakUp'da Payme/Click to'lovlari bor, ya'ni
tijorat xizmat. Variantlar:

1. ZIPA mualliflaridan yozma aniqlik olish
2. `facebook/wav2vec2-lv-60-espeak-cv-ft` ga o'tish (Apache-2.0, L2-ARCTIC
   PFER 2.89 — ZIPA'ning 1.75 idan yomonroq, lekin litsenziyasi toza)

Modelni almashtirish `ZIPA_REPO` ni o'zgartirishdan ko'proq ish talab qiladi
(fbank formati boshqa), lekin `modellar.py` izolyatsiyalangan.

**espeak-ng (Kokoro orqali):** GPL-3.0. Server tomonida ishlatiladi, hech narsa
tarqatilmaydi — shuning uchun copyleft shartlari yoqilmaydi. Agar kelajakda
tarqatish bo'lsa, TTS almashtirilishi kerak.

## Britancha imlo bo'shlig'i

CMUdict amerikacha. `analyse`, `aeroplane`, `organise`, `jeopardised` — lug'atda
**yo'q**, ya'ni filtr ularni rad etadi. IELTS esa britancha ingliz tiliga moyil.

Shuningdek etalon fonemalar amerikacha: foydalanuvchi britancha aytsa
(`schedule` da `ʃ`, `car` da `r` aytilmasligi) tizim xato deb belgilashi mumkin.
Kokoro'da britancha ovozlar bor (`bf_emma`, `bm_george`), lekin etalon
amerikacha — bu nomuvofiqlik **hal qilinmagan**. Shuning uchun etalon ovoz
ataylab amerikacha (`af_heart`) qilib qo'yilgan: hech bo'lmasa ko'rsatilgan
namuna baholanadigan etalon bilan mos keladi.

## Lokal sinov (Docker'siz)

```bash
cd ~/tallafuz_ai && python3 -m venv venv
./venv/bin/pip install -r requirements.txt fastapi uvicorn

# sidecar'ni ishga tushirish (Kokoro espeak-ng talab qiladi; faqat fonema
# yo'lini sinash uchun tts moduli stub bilan almashtiriladi)
# keyin Go tomonini tirik sidecar bilan tekshirish:
TALAFFUZ_TEST_URL=http://127.0.0.1:7881 \
TALAFFUZ_TEST_WAV=namuna/l2_700.wav \
TALAFFUZ_TEST_TEXT="IT WAS AN IMPORTANT WIN" \
go test ./internal/services/ -run TestSidecarEndToEnd -v
```

Namuna yozuvlar: `python namuna_ol.py` (speechocean762, inson baholari bilan).
