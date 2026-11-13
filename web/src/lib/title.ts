/**
 * omp 标题卡解析：自动标题形如 `<图标> <代码>: <标题>`
 * （oh-my-pi `utils/title-card.ts`：图标为单个 emoji 或 Nerd Font 私有区字形，代码为 1–6 位大写字母或数字）。
 */

export interface TitleCard {
	/** 单个字形（emoji 或 Nerd Font 私有区字符） */
	icon?: string;
	/** 1–6 位大写代码，如 `FLAKY` */
	code?: string;
	/** 去掉卡片前缀后的标题 */
	text: string;
}

const CARD = /^(\S+) ([A-Z0-9]{1,6}): (\S.*)$/su;

function isSingleGlyph(s: string): boolean {
	// ASCII 开头的不是图标（如「Fix BUG: x」）
	if (/^[\x20-\x7e]/.test(s)) return false;
	if (typeof Intl !== "undefined" && "Segmenter" in Intl) {
		const seg = new Intl.Segmenter(undefined, { granularity: "grapheme" });
		let n = 0;
		for (const _ of seg.segment(s)) if (++n > 1) return false;
		return n === 1;
	}
	return [...s].length <= 2;
}

export function parseTitleCard(raw: string): TitleCard {
	const m = CARD.exec(raw);
	if (!m) return { text: raw };
	const [, icon = "", code, text = raw] = m;
	if (!isSingleGlyph(icon)) return { text: raw };
	return { icon, code, text };
}

/** 用新标题文字重建标题：原标题带卡片时保留 `<图标> <代码>: ` 前缀。 */
export function withTitleCard(raw: string, text: string): string {
	const card = parseTitleCard(raw);
	if (!card.icon || !card.code) return text;
	return `${card.icon} ${card.code}: ${text}`;
}

/** Nerd Font 私有区字形（需用符号字体渲染、可着色）；emoji 返回 false。 */
export function isNerdGlyph(icon: string): boolean {
	const cp = icon.codePointAt(0) ?? 0;
	return (cp >= 0xe000 && cp <= 0xf8ff) || (cp >= 0xf0000 && cp <= 0xffffd);
}
