#!/usr/bin/env python3
"""生成 Oryn 各端 App 图标（浅色 / 深色 / 单色），唯一来源是下面的字形几何与配色。

用法（在 web/ 下）：python3 scripts/gen-icons.py [--preview 输出.png]
依赖：rsvg-convert（librsvg）与 Pillow。产物直接写进仓库，改了设计重跑一次再提交。

字形与 web/src/components/ui/data.tsx 的 BrandMark 相同（32 单位画布）：
左侧半圆盘 + 右侧光标竖条。默认图标只用黑白两色。

产物：
- src-tauri/icons/{32x32,128x128,128x128@2x,icon}.png、icon.ico：Windows / Linux（浅色圆角方块）
- src-tauri/icons/icon.icns：macOS 26 之前的回退（macOS 图标网格 824/1024 + 投影）
- src-tauri/icons/macos/AppIcon.icon：Icon Composer 包，macOS CI 用 actool 编进 Assets.car（浅色 / 深色 / 着色）
- src-tauri/icons/ios/AppIcon.appiconset：iOS 18+ 的 Any / Dark / Tinted 三种外观，CI 在 tauri ios init 后拷进工程
- src-tauri/gen/android/.../res：自适应图标（矢量前景 + 背景色，values-night 深色）与主题图标单色层
- public/：favicon.svg、PWA 图标（any / maskable）与 apple-touch-icon
"""

from __future__ import annotations

import argparse
import io
import json
import math
import shutil
import subprocess
from pathlib import Path

from PIL import Image

WEB = Path(__file__).resolve().parent.parent
ICONS = WEB / "src-tauri" / "icons"
ANDROID_RES = WEB / "src-tauri" / "gen" / "android" / "app" / "src" / "main" / "res"
PUBLIC = WEB / "public"

# ---------------------------------------------------------------- 字形（32 单位画布）
DISC = "M20.1 4.52A12 12 0 1 0 20.1 27.48Z"
CARET = "M24.5 4H25.9A1.1 1.1 0 0 1 27 5.1V26.9A1.1 1.1 0 0 1 25.9 28H24.5A1.1 1.1 0 0 1 23.4 26.9V5.1A1.1 1.1 0 0 1 24.5 4Z"
# 字形外接框：圆盘圆心 (16.61, 16)、半径 12，最左 x=4.61；竖条到 x=27；上下 4..28
GLYPH_X0, GLYPH_X1, GLYPH_Y0, GLYPH_Y1 = 4.61, 27.0, 4.0, 28.0
GLYPH_W, GLYPH_H = GLYPH_X1 - GLYPH_X0, GLYPH_Y1 - GLYPH_Y0
GLYPH_CX, GLYPH_CY = (GLYPH_X0 + GLYPH_X1) / 2, (GLYPH_Y0 + GLYPH_Y1) / 2


# ---------------------------------------------------------------- 配色（取自 web/src/index.css 的主题变量）
def oklch(l: float, c: float, h: float) -> tuple[int, int, int]:
    a, b = c * math.cos(math.radians(h)), c * math.sin(math.radians(h))
    l_ = (l + 0.3963377774 * a + 0.2158037573 * b) ** 3
    m_ = (l - 0.1055613458 * a - 0.0638541728 * b) ** 3
    s_ = (l - 0.0894841775 * a - 1.2914855480 * b) ** 3
    lin = (
        4.0767416621 * l_ - 3.3077115913 * m_ + 0.2309699292 * s_,
        -1.2684380046 * l_ + 2.6097574011 * m_ - 0.3413193965 * s_,
        -0.0041960863 * l_ - 0.7034186147 * m_ + 1.7076147010 * s_,
    )

    def enc(x: float) -> int:
        x = min(1.0, max(0.0, x))
        v = 12.92 * x if x <= 0.0031308 else 1.055 * x ** (1 / 2.4) - 0.055
        return round(v * 255)

    return enc(lin[0]), enc(lin[1]), enc(lin[2])


def hexc(rgb: tuple[int, int, int]) -> str:
    return "#{:02x}{:02x}{:02x}".format(*rgb)


TINT_H = 235
# 默认图标只用黑白：竖条与圆盘同色
PALETTE = {
    # 浅色：panel → bg 的竖向渐变，字形用正文色
    "light": {
        "top": oklch(0.995, 0.003, TINT_H),
        "bottom": oklch(0.925, 0.012, TINT_H),
        "ink": oklch(0.21, 0.015, TINT_H),
        "caret": oklch(0.21, 0.015, TINT_H),
    },
    # 深色：比页面底色略亮的竖向渐变，字形用深色主题正文色
    "dark": {
        "top": oklch(0.27, 0.014, TINT_H),
        "bottom": oklch(0.165, 0.010, TINT_H),
        "ink": oklch(0.94, 0.006, TINT_H),
        "caret": oklch(0.94, 0.006, TINT_H),
    },
    # iOS 着色（Tinted）：系统按亮度上色，交灰度图
    "tinted": {
        "top": (0, 0, 0),
        "bottom": (0, 0, 0),
        "ink": (255, 255, 255),
        "caret": (255, 255, 255),
    },
}


# ---------------------------------------------------------------- SVG 拼装
def squircle(cx: float, cy: float, r: float, n: float = 5.0, steps: int = 256) -> str:
    """超椭圆 |x|^n + |y|^n = r^n 的闭合路径（近似 Apple 的连续圆角方块）。"""
    pts = []
    for i in range(steps):
        t = 2 * math.pi * i / steps
        c, s = math.cos(t), math.sin(t)
        x = cx + r * math.copysign(abs(c) ** (2 / n), c)
        y = cy + r * math.copysign(abs(s) ** (2 / n), s)
        pts.append(f"{x:.2f} {y:.2f}")
    return "M" + "L".join(pts) + "Z"


def glyph(
    size: float, cx: float, cy: float, height: float, ink: str, caret: str
) -> str:
    """把 32 单位字形缩放到给定高度、外接框中心放到 (cx, cy)。"""
    k = height / GLYPH_H
    tx, ty = cx - GLYPH_CX * k, cy - GLYPH_CY * k
    return (
        f'<g transform="translate({tx:.3f} {ty:.3f}) scale({k:.5f})">'
        f'<path d="{DISC}" fill="{ink}"/><path d="{CARET}" fill="{caret}"/></g>'
    )


def tile_svg(
    variant: str,
    size: int = 1024,
    *,
    shape: str = "full",
    glyph_ratio: float = 0.50,
    shadow: bool = False,
    caret: tuple[int, int, int] | None = None,
) -> str:
    """shape：full 满幅方块（iOS / maskable，由系统裁形）；rounded 桌面圆角方块；macos 图标网格；none 透明底。
    caret：可替换图标给竖条换色（缺省与字形同色）。"""
    p = PALETTE[variant]
    s = size
    defs = (
        f'<linearGradient id="bg" x1="0" y1="0" x2="0" y2="1">'
        f'<stop offset="0" stop-color="{hexc(p["top"])}"/><stop offset="1" stop-color="{hexc(p["bottom"])}"/>'
        f"</linearGradient>"
    )
    body = ""
    if shape == "full":
        body_box = (0, 0, s)
        body = f'<rect width="{s}" height="{s}" fill="url(#bg)"/>'
    elif shape == "rounded":
        m = s * 0.04
        body_box = (m, m, s - 2 * m)
        body = f'<path d="{squircle(s / 2, s / 2, s / 2 - m)}" fill="url(#bg)"/>'
    elif shape == "macos":
        # macOS 图标网格：1024 画布里主体 824、居中略偏上，下方留投影空间
        inner = s * 824 / 1024
        off = (s - inner) / 2
        body_box = (off, off - s * 0.01, inner)
        cy = off + inner / 2 - s * 0.01
        path = squircle(s / 2, cy, inner / 2)
        if shadow:
            defs += (
                '<filter id="sh" x="-20%" y="-20%" width="140%" height="140%">'
                f'<feGaussianBlur stdDeviation="{s * 0.012:.2f}"/></filter>'
            )
            body += f'<path d="{path}" fill="#000" opacity="0.28" transform="translate(0 {s * 0.012:.2f})" filter="url(#sh)"/>'
        body += f'<path d="{path}" fill="url(#bg)"/>'
        # 浅色主体在浅色桌面上需要一圈极淡的边
        if variant == "light":
            body += f'<path d="{path}" fill="none" stroke="#000" stroke-opacity="0.08" stroke-width="{s / 512:.2f}"/>'
    elif shape == "none":
        body_box = (0, 0, s)
    else:
        raise ValueError(shape)
    bx, by, bs = body_box
    g = glyph(
        s,
        bx + bs / 2,
        by + bs / 2,
        bs * glyph_ratio,
        hexc(p["ink"]),
        hexc(caret or p["caret"]),
    )
    return (
        f'<svg xmlns="http://www.w3.org/2000/svg" width="{s}" height="{s}" viewBox="0 0 {s} {s}">'
        f"<defs>{defs}</defs>{body}{g}</svg>"
    )


def render(svg: str, size: int) -> Image.Image:
    png = subprocess.run(
        ["rsvg-convert", "-w", str(size), "-h", str(size)],
        input=svg.encode(),
        capture_output=True,
        check=True,
    ).stdout
    return Image.open(io.BytesIO(png)).convert("RGBA")


def save_png(img: Image.Image, path: Path, *, opaque: bool = False) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    (img.convert("RGB") if opaque else img).save(path, optimize=True)


# ---------------------------------------------------------------- 各端
def desktop() -> dict[str, Image.Image]:
    """Windows / Linux：浅色圆角方块（这两端没有随系统深浅切换的应用图标）。macOS 26 之前用 icns 回退。"""
    rounded = tile_svg("light", shape="rounded")
    for name, px in (
        ("32x32.png", 32),
        ("128x128.png", 128),
        ("128x128@2x.png", 256),
        ("icon.png", 512),
    ):
        save_png(render(rounded, px), ICONS / name)
    sizes = [16, 24, 32, 48, 64, 128, 256]
    big = render(rounded, 256)
    big.save(ICONS / "icon.ico", sizes=[(n, n) for n in sizes])

    mac = tile_svg("light", shape="macos", shadow=True)
    mac_img = render(mac, 1024)
    mac_img.save(
        ICONS / "icon.icns", sizes=[(n, n) for n in (16, 32, 64, 128, 256, 512, 1024)]
    )
    return {"desktop": render(rounded, 512), "macos-legacy": mac_img}


def macos_icon_composer() -> None:
    """Icon Composer 包：背景用 fill 的浅 / 深两种渐变，字形两层各自带浅 / 深填充；着色外观由系统按亮度生成。"""
    root = ICONS / "macos" / "AppIcon.icon"
    if root.exists():
        shutil.rmtree(root)
    (root / "Assets").mkdir(parents=True)
    s = 1024
    # 图层是满幅 1024 画布、字形已放在中心，图层本身不再平移缩放
    for name, d in (("disc", DISC), ("caret", CARET)):
        k = s * 0.50 / GLYPH_H
        tx, ty = s / 2 - GLYPH_CX * k, s / 2 - GLYPH_CY * k
        (root / "Assets" / f"{name}.svg").write_text(
            f'<svg xmlns="http://www.w3.org/2000/svg" width="{s}" height="{s}" viewBox="0 0 {s} {s}">'
            f'<path transform="translate({tx:.3f} {ty:.3f}) scale({k:.5f})" d="{d}" fill="#000"/></svg>\n'
        )

    def srgb(rgb: tuple[int, int, int]) -> str:
        return "srgb:" + ",".join(f"{v / 255:.5f}" for v in rgb) + ",1.00000"

    def layer(name: str, role: str) -> dict:
        return {
            "name": name,
            "image-name": f"{name}.svg",
            "glass": True,
            "hidden": False,
            "opacity": 1,
            "position": {"scale": 1, "translation-in-points": [0, 0]},
            "fill-specializations": [
                {"value": {"solid": srgb(PALETTE["light"][role])}},
                {"appearance": "dark", "value": {"solid": srgb(PALETTE["dark"][role])}},
            ],
        }

    manifest = {
        "fill-specializations": [
            {
                "value": {
                    "linear-gradient": [
                        srgb(PALETTE["light"]["top"]),
                        srgb(PALETTE["light"]["bottom"]),
                    ]
                }
            },
            {
                "appearance": "dark",
                "value": {
                    "linear-gradient": [
                        srgb(PALETTE["dark"]["top"]),
                        srgb(PALETTE["dark"]["bottom"]),
                    ]
                },
            },
        ],
        "groups": [
            {
                "layers": [layer("caret", "caret"), layer("disc", "ink")],
                "lighting": "individual",
                "shadow": {"kind": "neutral", "opacity": 0.5},
                "specular": True,
                "translucency": {"enabled": True, "value": 0.5},
            }
        ],
        "supported-platforms": {"squares": "shared"},
    }
    (root / "icon.json").write_text(json.dumps(manifest, indent=2) + "\n")


def appiconset(
    out: Path, carets: dict[str, tuple[int, int, int]] | None = None
) -> dict[str, Image.Image]:
    """单尺寸 1024 的 Any / Dark / Tinted 三种外观（iOS 18+）；Any 必须不透明（App Store 校验）。"""
    imgs = {}
    stem = out.name.removesuffix(".appiconset")
    images = []
    for variant, suffix, appearance in (
        ("light", "", None),
        ("dark", "-dark", "dark"),
        ("tinted", "-tinted", "tinted"),
    ):
        fname = f"{stem}{suffix}.png"
        img = render(
            tile_svg(variant, shape="full", caret=(carets or {}).get(variant)), 1024
        )
        save_png(img, out / fname, opaque=True)
        imgs[f"{stem}-{variant}"] = img
        entry: dict = {
            "filename": fname,
            "idiom": "universal",
            "platform": "ios",
            "size": "1024x1024",
        }
        if appearance:
            entry = {
                "appearances": [{"appearance": "luminosity", "value": appearance}],
                **entry,
            }
        images.append(entry)
    (out / "Contents.json").write_text(
        json.dumps(
            {"images": images, "info": {"author": "xcode", "version": 1}}, indent=2
        )
        + "\n"
    )
    return imgs


# iOS 可替换图标：资源名 → 竖条在浅 / 深外观下的颜色（着色外观仍是灰度，与默认相同）。
# 资源名与壳里的 id 对应表在 src-tauri/src/mobile_logic.rs 的 APP_ICONS，两边一起改。
ACCENT_H, ACCENT_C = 205, 0.105
ALTERNATES = {
    # 品牌色：浅 / 深两套主题的 --brand
    "AppIconAccent": {
        "light": oklch(0.53, ACCENT_C, ACCENT_H),
        "dark": oklch(0.78, ACCENT_C, ACCENT_H),
    },
}


def ios() -> dict[str, Image.Image]:
    """默认图标 AppIcon 与各可替换图标；ci/ios-appicon.sh 把整个目录装进工程并让 actool 编入全部图标。"""
    root = ICONS / "ios"
    if root.exists():
        shutil.rmtree(root)
    imgs = appiconset(root / "AppIcon.appiconset")
    for name, carets in ALTERNATES.items():
        imgs |= appiconset(root / f"{name}.appiconset", carets)
    return imgs


def android() -> None:
    """自适应图标：108dp 画布，字形放在 66dp 安全圆内；颜色走资源，values-night 给深色。"""
    res = ANDROID_RES
    # tauri 模板留下的位图与默认前景 / 背景（minSdk 33，只用 anydpi 自适应图标）
    for d in res.glob("mipmap-*dpi"):
        shutil.rmtree(d)
    for f in (
        res / "drawable-v24" / "ic_launcher_foreground.xml",
        res / "drawable" / "ic_launcher_background.xml",
    ):
        f.unlink(missing_ok=True)
    if (res / "drawable-v24").exists() and not any((res / "drawable-v24").iterdir()):
        (res / "drawable-v24").rmdir()

    k = (
        40 / GLYPH_H
    )  # 字形高 40dp：外接框对角线一半约 26dp，落在 33dp 安全半径内，圆形遮罩下竖条不贴边
    tx, ty = 54 - GLYPH_CX * k, 54 - GLYPH_CY * k

    def vector(paths: str) -> str:
        return (
            '<?xml version="1.0" encoding="utf-8"?>\n'
            "<!-- 由 web/scripts/gen-icons.py 生成，勿手改 -->\n"
            '<vector xmlns:android="http://schemas.android.com/apk/res/android"\n'
            '    android:width="108dp"\n    android:height="108dp"\n'
            '    android:viewportWidth="108"\n    android:viewportHeight="108">\n'
            f'    <group android:translateX="{tx:.4f}" android:translateY="{ty:.4f}"\n'
            f'        android:scaleX="{k:.5f}" android:scaleY="{k:.5f}">\n'
            f"{paths}"
            "    </group>\n</vector>\n"
        )

    (res / "drawable").mkdir(parents=True, exist_ok=True)
    (res / "drawable" / "ic_launcher_foreground.xml").write_text(
        vector(
            f'        <path android:fillColor="@color/ic_launcher_ink" android:pathData="{DISC}"/>\n'
            f'        <path android:fillColor="@color/ic_launcher_caret" android:pathData="{CARET}"/>\n'
        )
    )
    # 主题图标（Android 13+）：系统只取单色层的形状、按壁纸配色
    (res / "drawable" / "ic_launcher_monochrome.xml").write_text(
        vector(
            f'        <path android:fillColor="#FFFFFFFF" android:pathData="{DISC}"/>\n'
            f'        <path android:fillColor="#FFFFFFFF" android:pathData="{CARET}"/>\n'
        )
    )
    adaptive = (
        '<?xml version="1.0" encoding="utf-8"?>\n'
        "<!-- 由 web/scripts/gen-icons.py 生成，勿手改 -->\n"
        '<adaptive-icon xmlns:android="http://schemas.android.com/apk/res/android">\n'
        '    <background android:drawable="@color/ic_launcher_background"/>\n'
        '    <foreground android:drawable="@drawable/ic_launcher_foreground"/>\n'
        '    <monochrome android:drawable="@drawable/ic_launcher_monochrome"/>\n'
        "</adaptive-icon>\n"
    )
    (res / "mipmap-anydpi").mkdir(exist_ok=True)
    for name in ("ic_launcher.xml", "ic_launcher_round.xml"):
        (res / "mipmap-anydpi" / name).write_text(adaptive)

    def argb(rgb: tuple[int, ...]) -> str:
        return "#FF" + hexc(rgb)[1:].upper()

    def colors(variant: str, note: str) -> str:
        p = PALETTE[variant]
        mid = tuple(round((a + b) / 2) for a, b in zip(p["top"], p["bottom"]))
        return (
            '<?xml version="1.0" encoding="utf-8"?>\n'
            f"<!-- 由 web/scripts/gen-icons.py 生成，勿手改。{note} -->\n"
            "<resources>\n"
            f'    <color name="ic_launcher_background">{argb(mid)}</color>\n'
            f'    <color name="ic_launcher_ink">{argb(p["ink"])}</color>\n'
            f'    <color name="ic_launcher_caret">{argb(p["caret"])}</color>\n'
            "</resources>\n"
        )

    (res / "values" / "ic_launcher.xml").write_text(colors("light", "浅色图标"))
    (res / "values-night" / "ic_launcher.xml").write_text(
        colors("dark", "深色图标：启动器按深色配置解析资源时生效（取决于启动器）")
    )


def web() -> dict[str, Image.Image]:
    light, dark = PALETTE["light"], PALETTE["dark"]
    (PUBLIC / "favicon.svg").write_text(
        '<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 32 32"><title>Oryn</title>'
        f"<style>.i{{fill:{hexc(light['ink'])}}}.c{{fill:{hexc(light['caret'])}}}"
        f"@media (prefers-color-scheme: dark){{.i{{fill:{hexc(dark['ink'])}}}.c{{fill:{hexc(dark['caret'])}}}}}</style>"
        f'<path class="i" d="{DISC}"/><path class="c" d="{CARET}"/></svg>\n'
    )
    rounded = tile_svg("light", shape="rounded")
    save_png(render(rounded, 192), PUBLIC / "icon-192.png")
    save_png(render(rounded, 512), PUBLIC / "icon-512.png")
    # maskable：满幅，字形在 80% 安全圆内
    maskable = render(tile_svg("light", shape="full", glyph_ratio=0.40), 512)
    save_png(maskable, PUBLIC / "icon-maskable-512.png", opaque=True)
    save_png(
        render(tile_svg("light", shape="full"), 180),
        PUBLIC / "apple-touch-icon.png",
        opaque=True,
    )
    return {"maskable": maskable}


def preview(imgs: dict[str, Image.Image], path: Path) -> None:
    """总览：每张图放在浅 / 深两种底上，方便目视检查。"""
    names = list(imgs)
    cell, pad = 256, 24
    sheet = Image.new(
        "RGB",
        (len(names) * (cell + pad) + pad, 2 * (cell + pad) + pad),
        (128, 128, 128),
    )
    for row, bg in enumerate(((236, 238, 242), (24, 26, 30))):
        y = pad + row * (cell + pad)
        for i, n in enumerate(names):
            x = pad + i * (cell + pad)
            tile = Image.new("RGBA", (cell, cell), bg + (255,))
            tile.alpha_composite(imgs[n].resize((cell, cell), Image.LANCZOS))
            sheet.paste(tile.convert("RGB"), (x, y))
    sheet.save(path)


def main() -> None:
    ap = argparse.ArgumentParser()
    ap.add_argument("--preview", type=Path, help="另存一张各变体总览图")
    args = ap.parse_args()
    imgs: dict[str, Image.Image] = {}
    imgs |= desktop()
    macos_icon_composer()
    imgs |= ios()
    android()
    imgs |= web()
    if args.preview:
        preview(imgs, args.preview)


if __name__ == "__main__":
    main()
