/**
 * iOS 原生页头里的 Nerd Font 私有区字形：系统字体没有这些码点，原生直接画会显示成方框。所以先在 WebView 里
 * 用页面自带的 Symbols Nerd Font 画成「白色字形 + 透明底」的 PNG，原生把它当模板图，按图标的 `fg` 着色，
 * 再叠在 `bg` 方块上（`NativeIcon.mask`）。字形按墨迹框居中，字号与网页 `.title-glyph` 一样取方块边长 × 0.68。
 */
import { useEffect, useState } from "react";
import type { NativeIcon } from "./host";
import { isNerdGlyph } from "./title";

/** 方块边长的像素数：页头里最大的方块是 18pt，按 3 倍屏画。 */
export const NERD_MASK_PX = 54;
const FAMILY = '"Symbols Nerd Font Mono"';
const PNG_PREFIX = "data:image/png;base64,";
const masks = new Map<string, Promise<string | null>>();

async function draw(glyph: string, px: number): Promise<string | null> {
	const font = `${Math.round(px * 0.68)}px ${FAMILY}`;
	// 按需下载的字体（unicode-range 只覆盖私有区）：没有能画这个字形的字面就不画，免得画出方框
	if ((await document.fonts.load(font, glyph)).length === 0) return null;
	const canvas = document.createElement("canvas");
	canvas.width = px;
	canvas.height = px;
	const ctx = canvas.getContext("2d");
	if (!ctx) return null;
	ctx.font = font;
	ctx.fillStyle = "#fff";
	const m = ctx.measureText(glyph);
	const w = m.actualBoundingBoxLeft + m.actualBoundingBoxRight;
	const h = m.actualBoundingBoxAscent + m.actualBoundingBoxDescent;
	if (!(w > 0 && h > 0)) return null;
	ctx.fillText(
		glyph,
		(px - w) / 2 + m.actualBoundingBoxLeft,
		(px - h) / 2 + m.actualBoundingBoxAscent,
	);
	const url = canvas.toDataURL("image/png");
	return url.startsWith(PNG_PREFIX) ? url.slice(PNG_PREFIX.length) : null;
}

/** Nerd 字形的模板 PNG（base64，不带 data: 前缀），同一字形只画一次；字体加载不上或画布不可用为 null。 */
export function nerdMask(glyph: string): Promise<string | null> {
	let p = masks.get(glyph);
	if (!p) {
		p = draw(glyph, NERD_MASK_PX).catch(() => null);
		masks.set(glyph, p);
	}
	return p;
}

/** `on` 且 `glyph` 是 Nerd 字形时取它的模板 PNG；画好之前、失败或不是 Nerd 字形都是 null。 */
export function useNerdMask(
	glyph: string | undefined,
	on: boolean,
): string | null {
	const want = on && glyph && isNerdGlyph(glyph) ? glyph : undefined;
	const [done, setDone] = useState<{ glyph: string; png: string | null }>();
	useEffect(() => {
		if (!want) return;
		let live = true;
		void nerdMask(want).then((png) => {
			if (live) setDone({ glyph: want, png });
		});
		return () => {
			live = false;
		};
	}, [want]);
	return want && done?.glyph === want ? done.png : null;
}

/**
 * 字形方块图标：Nerd 字形交模板图（`mask` 还没画好或画不出时为 undefined，调用方自己退路），
 * 其它字形（emoji、首字母）照旧交给原生字体。
 */
export function glyphTile(
	glyph: string,
	mask: string | null,
	colors: { bg: string; fg: string },
): NativeIcon | undefined {
	if (!isNerdGlyph(glyph)) return { glyph, ...colors };
	return mask ? { mask, ...colors } : undefined;
}
