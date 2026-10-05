#!/usr/bin/env python3
"""Generates internal/games/assets/pronunciation_dict.tsv.

    python3 scripts/gen_pronunciation_dict.py

Output columns: word <TAB> ARPAbet <TAB> CEFR or empty <TAB> target sounds

Two jobs in one file:
  1. Membership  - is the reference pronunciation trustworthy? A word absent
                   from CMUdict has no human transcription, so a g2p model
                   would have to guess it. A wrong guess marks correct speech
                   as an error, so the passage validator rejects such words.
  2. CEFR column - is a generated passage really at the level it claims? Only
                   the ~8k words in cefr.tsv carry a level. Inflected forms and
                   contractions are valid words but ungraded, so the level
                   check skips them instead of failing them.

Contractions (don't, it's) are deliberately kept: natural English needs them,
and excluding them would make the validator reject almost every passage.

Source: CMU Pronouncing Dictionary, Carnegie Mellon University (BSD-style
licence). Fetched from jsdelivr because raw.githubusercontent.com is not
reachable from every network we build on.
"""
import collections
import os
import sys
import urllib.request

HERE = os.path.dirname(os.path.abspath(__file__))
ROOT = os.path.dirname(HERE)
ASSETS = os.path.join(ROOT, "internal", "games", "assets")
CEFR = os.path.join(ASSETS, "cefr.tsv")
OUT = os.path.join(ASSETS, "pronunciation_dict.tsv")
CACHE = os.path.join(HERE, ".cmudict.cache")

CMU_URL = "https://cdn.jsdelivr.net/gh/cmusphinx/cmudict@master/cmudict.dict"

# Word-final consonants that must stay voiced. Uzbek and Russian L1 speakers
# devoice them ("lab" -> "lap"), so a word ending in one of these is worth
# drilling.
FINAL_VOICED = {"B", "D", "G", "V", "Z", "ZH", "DH", "JH"}


def target_sounds(phones):
    """Which of the drilled target sounds this pronunciation exercises."""
    found = set()
    bare = [p.rstrip("012") for p in phones]
    for p in bare:
        if p in ("TH", "DH", "W", "V", "AE"):
            found.add(p)
        elif p in ("IY", "IH"):
            found.add("IY_IH")
    if bare and bare[-1] in FINAL_VOICED:
        found.add("FINAL_VOICED")
    return sorted(found)


def usable(word):
    """Letters and inner apostrophes only - no digits, dots or symbols."""
    if not word or not word[0].isalpha():
        return False
    return all(ch.isalpha() or ch == "'" for ch in word)


def fetch_cmudict():
    if os.path.exists(CACHE) and os.path.getsize(CACHE) > 3_000_000:
        print(f"cmudict: cache ({CACHE})", file=sys.stderr)
        return CACHE
    print(f"cmudict: downloading {CMU_URL}", file=sys.stderr)
    urllib.request.urlretrieve(CMU_URL, CACHE + ".part")
    os.replace(CACHE + ".part", CACHE)
    return CACHE


def main():
    path = fetch_cmudict()

    cmu = {}
    with open(path, encoding="utf-8", errors="replace") as f:
        for line in f:
            line = line.strip()
            if not line or line.startswith(";;;"):
                continue
            parts = line.split()
            word = parts[0]
            if "(" in word:  # cmudict's 2nd/3rd variant - the first is enough
                continue
            word = word.lower()
            if usable(word):
                cmu[word] = parts[1:]
    print(f"cmudict entries kept : {len(cmu)}", file=sys.stderr)

    cefr = {}
    with open(CEFR, encoding="utf-8") as f:
        for line in f:
            p = line.rstrip("\n").split("\t")
            if len(p) == 2 and p[0]:
                cefr[p[0].lower()] = p[1].strip().upper()

    rows = [
        (w, " ".join(cmu[w]), cefr.get(w, ""), ",".join(target_sounds(cmu[w])))
        for w in sorted(cmu)
    ]

    with open(OUT, "w", encoding="utf-8") as f:
        f.write("# Pronunciation dictionary: CMUdict + CEFR level where known\n")
        f.write("# Source: CMU Pronouncing Dictionary, Carnegie Mellon University (BSD-style licence)\n")
        f.write("# Regenerate with: python3 scripts/gen_pronunciation_dict.py\n")
        f.write("# word\tARPAbet\tCEFR\ttarget_sounds\n")
        for r in rows:
            f.write("\t".join(r) + "\n")

    size = os.path.getsize(OUT) / 1048576
    print(f"written              : {len(rows)} words, {size:.1f} MB -> {OUT}", file=sys.stderr)
    print(f"with a CEFR level    : {sum(1 for r in rows if r[2])}", file=sys.stderr)
    print(f"contractions         : {sum(1 for r in rows if chr(39) in r[0])}", file=sys.stderr)
    counts = collections.Counter()
    for r in rows:
        for s in r[3].split(","):
            if s:
                counts[s] += 1
    print("target sound coverage:", file=sys.stderr)
    for s, n in counts.most_common():
        print(f"   {s:<13}: {n:>6}", file=sys.stderr)


if __name__ == "__main__":
    main()
