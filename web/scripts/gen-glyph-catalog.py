#!/usr/bin/env python3
"""生成图标选择器的完整字形目录 `src/lib/glyph-catalog.gen.ts`（选择弹层打开时才按需加载）。

用法（在 web/ 下）：python3 scripts/gen-glyph-catalog.py
依赖：fontTools（核对每个码点都在打包的 Symbols Nerd Font 里）；数据源按下面固定的版本下载。

- Nerd 字形：Nerd Fonts 3.4.0 `glyphnames.json` 里 dev（开发品牌与语言）、linux（发行版）、md（Material Design）三类；
  与 `src/assets/fonts/SymbolsNerdFontMono-Regular.woff2` 的 cmap 求交，字体里没有的码点直接报错。
- Emoji：Unicode Emoji 15.1 `emoji-test.txt` 的 fully-qualified 条目，去掉肤色 / 发型等组件变体；
  中文名与关键词取 CLDR 注释（zh 与 zh 派生），供中文搜索。
"""

from __future__ import annotations

import json
import re
import urllib.request
from pathlib import Path

from fontTools.ttLib import TTFont

WEB = Path(__file__).resolve().parent.parent
FONT = WEB / "src/assets/fonts/SymbolsNerdFontMono-Regular.woff2"
OUT = WEB / "src/lib/glyph-catalog.gen.ts"

GLYPHNAMES = "https://raw.githubusercontent.com/ryanoasis/nerd-fonts/v3.4.0/glyphnames.json"
EMOJI_TEST = "https://unicode.org/Public/emoji/15.1/emoji-test.txt"
CLDR = "https://raw.githubusercontent.com/unicode-org/cldr-json/48.0.0/cldr-json"
ZH_ANNOTATIONS = f"{CLDR}/cldr-annotations-full/annotations/zh/annotations.json"
ZH_DERIVED = f"{CLDR}/cldr-annotations-derived-full/annotationsDerived/zh/annotations.json"

# 目录里 Nerd 字形的分组顺序（键 = glyphnames 的类前缀）
NERD_SETS = ("dev", "linux", "md")
# emoji-test.txt 的分组 → 目录分组 id（Component 是肤色 / 发型修饰符本身，不收）
EMOJI_GROUPS = {
    "Smileys & Emotion": "smileys",
    "People & Body": "people",
    "Animals & Nature": "nature",
    "Food & Drink": "food",
    "Travel & Places": "travel",
    "Activities": "activities",
    "Objects": "objects",
    "Symbols": "symbols",
    "Flags": "flags",
}
# 肤色与发型修饰符：带它们的变体不单列（基础 emoji 已在）
MODIFIERS = re.compile("[\U0001f3fb-\U0001f3ff\U0001f9b0-\U0001f9b3]")


def fetch(url: str) -> bytes:
    with urllib.request.urlopen(url) as r:
        return r.read()


def nerd_sets() -> dict[str, list[tuple[str, str]]]:
    names = json.loads(fetch(GLYPHNAMES))
    names.pop("METADATA")
    cmap = set(TTFont(FONT).getBestCmap())
    out: dict[str, list[tuple[str, str]]] = {k: [] for k in NERD_SETS}
    seen: set[str] = set()
    for key in sorted(names):
        cls, _, name = key.partition("-")
        if cls not in out:
            continue
        code = names[key]["code"]
        if int(code, 16) not in cmap:
            raise SystemExit(f"字体里没有 {key} (U+{code})")
        # 同一码点的别名只留第一个
        if code in seen:
            continue
        seen.add(code)
        out[cls].append((code, name.replace("_", " ")))
    return out


def zh_annotations() -> dict[str, tuple[str, list[str]]]:
    merged: dict[str, tuple[str, list[str]]] = {}
    for url, root in ((ZH_ANNOTATIONS, "annotations"), (ZH_DERIVED, "annotationsDerived")):
        for ch, a in json.loads(fetch(url))[root]["annotations"].items():
            tts = (a.get("tts") or [""])[0]
            merged.setdefault(ch, (tts, a.get("default") or []))
    return merged


def emoji_groups() -> list[tuple[str, list[str]]]:
    zh = zh_annotations()
    groups: list[tuple[str, list[str]]] = []
    current: list[str] | None = None
    line_re = re.compile(r"^[0-9A-F ]+;\s*fully-qualified\s*#\s*(\S+)\s+E\d+\.\d+\s+(.+)$")
    for line in fetch(EMOJI_TEST).decode().splitlines():
        if line.startswith("# group: "):
            gid = EMOJI_GROUPS.get(line[len("# group: ") :])
            current = None if gid is None else []
            if current is not None:
                groups.append((gid, current))
            continue
        m = line_re.match(line)
        if not m or current is None:
            continue
        ch, en = m.groups()
        if MODIFIERS.search(ch):
            continue
        tts, kws = zh.get(ch) or zh.get(ch.replace("\ufe0f", "")) or ("", [])
        words = [w for w in dict.fromkeys(kws) if w != tts]
        # 每行：字符 \t 英文名 \t 中文名 \t 中文关键词（空格分隔）
        current.append("\t".join((ch, en, tts, " ".join(words))))
    return groups


def ts_string(s: str) -> str:
    return json.dumps(s, ensure_ascii=False)


def main() -> None:
    nerd = nerd_sets()
    emoji = emoji_groups()
    lines = [
        "/**",
        " * 由 scripts/gen-glyph-catalog.py 生成，勿手改。",
        " * Nerd 字形每行「码点十六进制 空格 名称」；emoji 每行「字符 \\t 英文名 \\t 中文名 \\t 中文关键词」。",
        " */",
        "",
        "export const NERD_SETS = {",
    ]
    for cls, items in nerd.items():
        lines.append(f"\t{cls}: {ts_string(chr(10).join(f'{c} {n}' for c, n in items))},")
    lines += ["} as const;", "", "export const EMOJI_GROUPS = {"]
    for gid, items in emoji:
        lines.append(f"\t{gid}: {ts_string(chr(10).join(items))},")
    lines += ["} as const;", ""]
    OUT.write_text("\n".join(lines), encoding="utf-8")
    counts = {k: len(v) for k, v in nerd.items()} | {k: len(v) for k, v in emoji}
    print(f"写入 {OUT.relative_to(WEB)}：{counts}")


if __name__ == "__main__":
    main()
