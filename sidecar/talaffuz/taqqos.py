"""Model tanigan tovushlarni lug'atdagi to'g'ri talaffuz bilan solishtiradi.

Bosqichlar:
  1. normallashtir() - har xil modellarning belgilarini bitta ingliz fonema to'plamiga keltiradi
  2. tekisla()       - kutilgan va tanilgan ketma-ketlikni Needleman-Wunsch bilan tekislaydi
  3. solishtir()     - so'zma-so'z natija va o'zbekcha izoh qaytaradi
"""
import unicodedata

UNLILAR = {"ɑ", "æ", "ʌ", "ə", "ɔ", "aʊ", "aɪ", "ɛ", "ɚ", "eɪ", "ɪ", "i", "oʊ", "ɔɪ", "ʊ", "u"}

OLIB_TASHLANADI = set("ːˑʰʷʲˈˌ ")
JUFT_BIRLIK = ["aɪ", "aʊ", "eɪ", "oʊ", "ɔɪ", "əʊ", "tʃ", "dʒ"]
# Tokenlar orasida birlashtiriladigan juftliklar (ZIPA diftonglarni bo'lib yozadi)
BIRLASHTIR = {("a", "ɪ"), ("a", "ʊ"), ("e", "ɪ"), ("o", "ʊ"), ("ɔ", "ɪ"), ("ə", "ʊ"), ("t", "ʃ"), ("d", "ʒ")}

# Har xil yozuvlarni ingliz fonema to'plamiga keltirish
MOSLA = {
    "g": "ɡ", "ɜ": "ɚ", "ɝ": "ɚ", "ɐ": "ʌ", "ᵻ": "ɪ", "ɨ": "ɪ", "ɾ": "t", "ʔ": "", "r": "ɹ", "ɻ": "ɹ",
    "ɫ": "l", "ɒ": "ɑ", "a": "ɑ", "e": "ɛ", "o": "oʊ", "əʊ": "oʊ", "x": "h", "ɯ": "ʊ", "ɤ": "ʌ",
    "y": "u", "c": "k", "ɟ": "ɡ", "ɲ": "n", "β": "v", "ɸ": "f", "ʋ": "v", "ç": "h", "ɕ": "ʃ",
    "ʑ": "ʒ", "ʂ": "ʃ", "ʐ": "ʒ", "q": "k", "ɣ": "ɡ", "χ": "h", "ʁ": "ɹ", "ɭ": "l", "ɳ": "n",
    "ʈ": "t", "ɖ": "d", "ɦ": "h", "ʉ": "u",
}

# Xato hisoblanmaydigan farqlar (aksent variantlari): (kutilgan, eshitilgan)
TENG = {("ɑ", "ɔ"), ("ɔ", "ɑ"), ("ə", "ʌ"), ("ʌ", "ə"), ("ə", "ɪ"), ("ɚ", "ə"), ("ə", "ɚ")}

# O'xshash undoshlar - tekislashda arzonroq almashtirish
YAQIN = {
    ("θ", "s"), ("θ", "t"), ("θ", "f"), ("ð", "d"), ("ð", "z"), ("ð", "v"), ("w", "v"), ("v", "w"),
    ("ŋ", "n"), ("z", "s"), ("d", "t"), ("b", "p"), ("ɡ", "k"), ("v", "f"), ("ʒ", "ʃ"), ("dʒ", "tʃ"),
    ("s", "z"), ("t", "d"), ("p", "b"), ("k", "ɡ"), ("f", "v"), ("h", ""), ("l", "ɹ"), ("ɹ", "l"),
}

# Gapda odatda kuchsiz (ə bilan) aytiladigan yordamchi so'zlar
ZAIF_SOZLAR = {
    "to", "the", "a", "an", "of", "and", "for", "at", "from", "as", "can", "was", "were", "are",
    "that", "them", "you", "your", "do", "does", "but", "than", "some", "have", "has", "had",
    "would", "should", "could", "will", "shall", "must", "am", "us", "her", "or",
}

# Tovushning o'zbekcha tushunarli nomi
NOM = {
    "θ": "th (think)", "ð": "th (this)", "ʃ": "sh", "tʃ": "ch", "dʒ": "j (job)", "ʒ": "j (vision)",
    "ŋ": "ng", "ɹ": "r", "j": "y", "ɡ": "g", "æ": "a (cat)", "ɪ": "qisqa i (sit)", "i": "uzun i (see)",
    "ʊ": "qisqa u (good)", "u": "uzun u (food)", "ə": "kuchsiz unli (about)", "ʌ": "a (cup)",
    "ɑ": "a (father)", "ɔ": "o (law)", "ɛ": "e (bed)", "ɚ": "er (her)", "aɪ": "ay (my)",
    "eɪ": "ey (day)", "oʊ": "ou (go)", "aʊ": "au (now)", "ɔɪ": "oy (boy)",
}

# Ko'p uchraydigan xatolar uchun maslahat: (kutilgan, eshitilgan)
MASLAHAT = {
    ("θ", "s"): "Tilingiz uchini tishlar orasiga qo'yib, havo chiqaring — «s» emas.",
    ("θ", "t"): "Tilingiz uchini tishlar orasiga qo'yib, havoni to'xtatmay chiqaring — «t» emas.",
    ("θ", "f"): "Lab emas, til ishlaydi: til uchi tishlar orasida bo'lsin.",
    ("ð", "d"): "Tilingiz uchini tishlar orasiga qo'yib, ovoz bilan ayting — «d» emas.",
    ("ð", "z"): "Tilingiz uchini tishlar orasiga qo'yib, ovoz bilan ayting — «z» emas.",
    ("w", "v"): "Lablarni dumaloq qilib oldinga cho'zing, tish labga tegmasin — «v» emas.",
    ("v", "w"): "Yuqori tishlar pastki labga tegsin.",
    ("ŋ", "n"): "Til uchi emas, til orqasi tanglayga tegsin (burun orqali).",
    ("æ", "ɛ"): "Og'izni kengroq oching: «e» bilan «a» orasidagi tovush.",
    ("æ", "ɑ"): "Tilni oldinroqqa suring: «e» bilan «a» orasidagi tovush.",
    ("ɪ", "i"): "Qisqa va bo'shashgan «i» ayting (sit), cho'zmang.",
    ("i", "ɪ"): "Uzun, taranglashgan «i» ayting (see).",
    ("ʊ", "u"): "Qisqa va bo'shashgan «u» ayting (good), cho'zmang.",
    ("z", "s"): "So'z oxiridagi tovush jarangli bo'lsin: «z», «s» emas.",
    ("d", "t"): "So'z oxiridagi tovush jarangli bo'lsin: «d», «t» emas.",
    ("b", "p"): "So'z oxiridagi tovush jarangli bo'lsin: «b», «p» emas.",
    ("ɡ", "k"): "So'z oxiridagi tovush jarangli bo'lsin: «g», «k» emas.",
    ("v", "f"): "So'z oxiridagi tovush jarangli bo'lsin: «v», «f» emas.",
}


def nom(f):
    return NOM.get(f, f)


def _tozala(tok):
    return "".join(
        c for c in tok if c not in OLIB_TASHLANADI and not unicodedata.combining(c) and not c.isdigit()
    )


def _birliklar(s):
    """Bitta token ichini fonema birliklariga bo'ladi (diftong va affrikatlar bitta birlik)."""
    natija, i = [], 0
    while i < len(s):
        if s[i : i + 2] in JUFT_BIRLIK:
            natija.append(s[i : i + 2])
            i += 2
        else:
            natija.append(s[i])
            i += 1
    return natija


def _normallashtir(tokenlar, tokenlararo_birlashtir, vaqtlar):
    """Tokenlar -> (fonemalar, har birining [bosh, oxir] vaqti)."""
    if vaqtlar is None:
        vaqtlar = [(0.0, 0.0)] * len(tokenlar)
    xom = []  # [birlik, bosh, oxir]
    for tok, (bosh, oxir) in zip(tokenlar, vaqtlar):
        for b in _birliklar(_tozala(tok)):
            if b == "˞":  # r-rangli unli belgisi
                if xom and xom[-1][0] in ("ə", "ɜ", "ɚ", "ɝ"):
                    xom[-1][0], xom[-1][2] = "ɚ", oxir
                else:
                    xom.append(["ɹ", bosh, oxir])
            else:
                xom.append([b, bosh, oxir])
    if tokenlararo_birlashtir:
        birlashgan, i = [], 0
        while i < len(xom):
            if i + 1 < len(xom) and (xom[i][0], xom[i + 1][0]) in BIRLASHTIR:
                birlashgan.append([xom[i][0] + xom[i + 1][0], xom[i][1], xom[i + 1][2]])
                i += 2
            else:
                birlashgan.append(xom[i])
                i += 1
        xom = birlashgan
    natija = []
    for b, bosh, oxir in xom:
        f = MOSLA.get(b, b)
        if not f:
            continue
        if natija and natija[-1][0] == f and f in UNLILAR:  # cho'ziq unli ikki marta yozilgan
            natija[-1][2] = oxir
            continue
        natija.append([f, bosh, oxir])
    return [n[0] for n in natija], [(n[1], n[2]) for n in natija]


def normallashtir(tokenlar, tokenlararo_birlashtir=False):
    """Model chiqargan tokenlar -> ingliz fonemalari ro'yxati."""
    return _normallashtir(tokenlar, tokenlararo_birlashtir, None)[0]


def _narx(kut, esh):
    if kut == esh or (kut, esh) in TENG:
        return 0.0
    ku, eu = kut in UNLILAR, esh in UNLILAR
    if ku and eu:
        return 0.5
    if ku != eu:
        return 1.5
    return 0.5 if (kut, esh) in YAQIN else 1.0


def tekisla(kutilgan, eshitilgan):
    """Har bir kutilgan fonema uchun mos eshitilgan fonemaning indeksini (yoki None) qaytaradi."""
    n, m = len(kutilgan), len(eshitilgan)
    d = [[0.0] * (m + 1) for _ in range(n + 1)]
    for i in range(1, n + 1):
        d[i][0] = float(i)
    for j in range(1, m + 1):
        d[0][j] = float(j)
    for i in range(1, n + 1):
        for j in range(1, m + 1):
            d[i][j] = min(
                d[i - 1][j - 1] + _narx(kutilgan[i - 1], eshitilgan[j - 1]),
                d[i - 1][j] + 1.0,
                d[i][j - 1] + 1.0,
            )
    juft = [None] * n
    i, j = n, m
    while i > 0 and j > 0:
        if d[i][j] == d[i - 1][j - 1] + _narx(kutilgan[i - 1], eshitilgan[j - 1]):
            juft[i - 1] = j - 1
            i, j = i - 1, j - 1
        elif d[i][j] == d[i - 1][j] + 1.0:
            i -= 1
        else:
            j -= 1
    return juft


def solishtir(lugat, tokenlar, tokenlararo_birlashtir=False, vaqtlar=None):
    """lugat: [{"soz", "ipa"}] (app.lugat_talaffuzi natijasi); tokenlar: model chiqishi.

    Qaytaradi: [{"soz", "kutilgan", "eshitilgan", "holat": ok|kichik|xato, "izohlar": [...],
                 "almashtirishlar": [{"kutilgan", "eshitilgan", "oxirida"}],
                 "vaqt": [bosh_s, oxir_s] | None}]
    vaqt - so'z yozuvning qaysi qismida aytilgani (vaqtlar berilgan bo'lsa).
    """
    eshitilgan, esh_vaqt = _normallashtir(tokenlar, tokenlararo_birlashtir, vaqtlar)
    kutilgan, chegara = [], []
    for s in lugat:
        fonemalar = s["ipa"].split()
        fonemalar = ["ɚ" if f == "ɝ" else f for f in fonemalar]
        chegara.append((len(kutilgan), len(kutilgan) + len(fonemalar)))
        kutilgan.extend(fonemalar)
    juft = tekisla(kutilgan, eshitilgan)

    natija = []
    for s, (a, b) in zip(lugat, chegara):
        izohlar, holat, almashtirishlar = [], "ok", []
        kut, idx = kutilgan[a:b], juft[a:b]
        esh = [None if j is None else eshitilgan[j] for j in idx]
        topilgan = [j for j in idx if j is not None]
        # So'zning yozuvdagi o'rni: birinchi va oxirgi mos tovush orasida
        oraliq = None
        if topilgan and vaqtlar:
            oxirgi = topilgan[0]
            for j in topilgan[1:]:
                # So'z ichida 0.6 s dan uzun uzilish bo'lsa, tekislash adashgan - shu yerda to'xtaymiz
                if esh_vaqt[j][0] - esh_vaqt[oxirgi][1] > 0.6:
                    break
                oxirgi = j
            oraliq = [esh_vaqt[topilgan[0]][0], esh_vaqt[oxirgi][1]]
        if kut and all(e is None for e in esh):
            natija.append(
                {
                    "soz": s["soz"],
                    "kutilgan": " ".join(kut),
                    "eshitilgan": "",
                    "holat": "xato",
                    "izohlar": [{"tur": "xato", "matn": "Bu so'z eshitilmadi."}],
                    "almashtirishlar": [],
                    "vaqt": None,
                }
            )
            continue
        for k, (f, e) in enumerate(zip(kut, esh)):
            if e is None:
                # Unlidan keyingi «r» tushishi - britancha talaffuz, xato emas
                if f == "ɹ" and k > 0 and kut[k - 1] in UNLILAR:
                    continue
                izohlar.append({"tur": "xato", "matn": f"«{nom(f)}» tovushi eshitilmadi."})
                almashtirishlar.append({"kutilgan": f, "eshitilgan": None, "oxirida": k == len(kut) - 1})
                holat = "xato"
            elif _narx(f, e) > 0:
                kichik = f in UNLILAR and e in UNLILAR
                # Yordamchi so'zlarda unlining kuchsizlashishi (to -> tə) - normal ingliz talaffuzi
                if kichik and e in ("ə", "ʌ") and s["soz"].lower().strip(".,!?;:\"'") in ZAIF_SOZLAR:
                    continue
                matn = f"«{nom(f)}» o'rniga «{nom(e)}» eshitildi."
                if (f, e) in MASLAHAT:
                    matn += " " + MASLAHAT[(f, e)]
                izohlar.append({"tur": "kichik" if kichik else "xato", "matn": matn})
                almashtirishlar.append({"kutilgan": f, "eshitilgan": e, "oxirida": k == len(kut) - 1})
                if not kichik:
                    holat = "xato"
                elif holat == "ok":
                    holat = "kichik"
        natija.append(
            {
                "soz": s["soz"],
                "kutilgan": " ".join(kut),
                "eshitilgan": " ".join(e for e in esh if e),
                "holat": holat,
                "izohlar": izohlar,
                # Tuzilgan almashtirishlar: Go tomoni ishonch siyosatini shu
                # ro'yxatga qarab qo'llaydi (o'zbekcha izoh matnini tahlil
                # qilish o'rniga). "oxirida" - so'z oxiridagi tovushmi.
                "almashtirishlar": almashtirishlar,
                "vaqt": oraliq,
            }
        )
    # CTC modellari tovushni qisqa "nuqta" sifatida belgilaydi, shuning uchun so'z chegarasini
    # biroz kengaytiramiz, lekin keyingi so'z boshlanishidan o'tkazmaymiz
    bor = [s for s in natija if s["vaqt"]]
    for k, s in enumerate(bor):
        bosh, oxir = s["vaqt"]
        keyingi = bor[k + 1]["vaqt"][0] if k + 1 < len(bor) else None
        oxir = oxir + 0.30 if keyingi is None else max(oxir + 0.04, min(oxir + 0.30, keyingi - 0.04))
        s["vaqt"] = [round(max(0.0, bosh - 0.08), 2), round(oxir, 2)]
    return natija
