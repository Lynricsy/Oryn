/**
 * 字形：项目图标与 agent 头像选择弹层的「常用」精选，以及按需加载的完整目录（`glyph-catalog.gen.ts`）与模糊搜索。
 * Nerd 字形的码点取自 Nerd Fonts 3.4 官方 glyphnames（oh-my-pi `utils/nerd-font-glyphs.txt`），按名字逐个核对，不要凭记忆改码点。
 */

import type * as CatalogGen from "./glyph-catalog.gen";

export interface Glyph {
	/** 无障碍名与悬停提示：Nerd 字形为去掉类前缀的英文名，emoji 为其本身 */
	label: string;
	/** 单个字形 */
	char: string;
}

/** `md-language_go` + `f07d3` → `{ label: "language go", char: "\u{f07d3}" }` */
function nf(name: string, hex: string): Glyph {
	return {
		label: name.slice(name.indexOf("-") + 1).replaceAll("_", " "),
		char: String.fromCodePoint(Number.parseInt(hex, 16)),
	};
}

function emoji(char: string): Glyph {
	return { label: char, char };
}

/** 项目图标：语言、平台、开发主题，共 48 个。 */
export const PROJECT_GLYPHS: readonly Glyph[] = [
	nf("md-language_go", "f07d3"),
	nf("md-language_rust", "f1617"),
	nf("md-language_python", "f0320"),
	nf("md-language_typescript", "f06e6"),
	nf("md-language_javascript", "f031e"),
	nf("md-nodejs", "f0399"),
	nf("md-react", "f0708"),
	nf("md-vuejs", "f0844"),
	nf("md-angular", "f06b2"),
	nf("md-language_html5", "f031d"),
	nf("md-language_css3", "f031c"),
	nf("md-tailwind", "f13ff"),
	nf("md-language_swift", "f06e5"),
	nf("md-language_kotlin", "f1219"),
	nf("md-language_java", "f0b37"),
	nf("md-language_c", "f0671"),
	nf("md-language_cpp", "f0672"),
	nf("md-language_csharp", "f031b"),
	nf("md-language_ruby", "f0d2d"),
	nf("md-language_php", "f031f"),
	nf("md-language_lua", "f08b1"),
	nf("md-language_haskell", "f0c92"),
	nf("dev-zig", "e8ef"),
	nf("dev-dart", "e798"),
	nf("md-nix", "f1105"),
	nf("md-docker", "f0868"),
	nf("md-kubernetes", "f10fe"),
	nf("md-linux", "f033d"),
	nf("md-apple", "f0035"),
	nf("md-android", "f0032"),
	nf("md-microsoft_windows", "f05b3"),
	nf("md-git", "f02a2"),
	nf("md-github", "f02a4"),
	nf("md-console", "f018d"),
	nf("md-database", "f01bc"),
	nf("md-server", "f048b"),
	nf("md-cloud", "f015f"),
	nf("md-web", "f059f"),
	nf("md-api", "f109b"),
	nf("md-robot", "f06a9"),
	nf("md-brain", "f09d1"),
	nf("md-flask", "f0093"),
	nf("md-bug", "f00e4"),
	nf("md-tools", "f1064"),
	nf("md-shield", "f0498"),
	nf("md-rocket_launch", "f14de"),
	nf("md-book_open_variant", "f14f7"),
	nf("md-gamepad_variant", "f0297"),
];

/** agent 头像·彩色 emoji，12 个。 */
export const AVATAR_EMOJI: readonly Glyph[] = [
	emoji("🦊"),
	emoji("🤖"),
	emoji("🐱"),
	emoji("🧠"),
	emoji("⚡"),
	emoji("🌙"),
	emoji("🦉"),
	emoji("🐙"),
	emoji("🐧"),
	emoji("👾"),
	emoji("✨"),
	emoji("🔮"),
];

/** agent 头像·单色 Nerd 字形，12 个；和 emoji 分组显示，避免彩色与单色交替混排。 */
export const AVATAR_NERD: readonly Glyph[] = [
	nf("md-robot", "f06a9"),
	nf("md-face_agent", "f0d70"),
	nf("md-head_cog", "f133c"),
	nf("md-brain", "f09d1"),
	nf("md-cat", "f011b"),
	nf("md-owl", "f03d2"),
	nf("md-penguin", "f0ec0"),
	nf("md-ghost", "f02a0"),
	nf("md-alien", "f089a"),
	nf("md-wizard_hat", "f1477"),
	nf("md-auto_fix", "f0068"),
	nf("md-rocket", "f0463"),
];

/** 恰为一个非 ASCII 字素（emoji 或 Nerd 字形）；用于校验手动输入的图标。 */
export function isSingleGlyph(raw: string): boolean {
	const s = raw.trim();
	if (!s || /^[\x20-\x7e]/.test(s)) return false;
	if (typeof Intl !== "undefined" && "Segmenter" in Intl) {
		const seg = new Intl.Segmenter(undefined, { granularity: "grapheme" });
		let n = 0;
		for (const _ of seg.segment(s)) if (++n > 1) return false;
		return n === 1;
	}
	return [...s].length <= 2;
}

/** 完整目录里的字形：带搜索用的全文。 */
export interface CatalogGlyph extends Glyph {
	/** emoji 的中文名（CLDR）；Nerd 字形没有 */
	zh?: string;
	/** 小写的搜索全文：英文名、中文名、中文关键词，空格分隔，英文名打头 */
	terms: string;
}

/** 目录分组：Nerd 按字体类（dev / linux / md），emoji 按 Unicode 分组（smileys、people…）。 */
export type GlyphSectionId =
	| keyof typeof CatalogGen.NERD_SETS
	| keyof typeof CatalogGen.EMOJI_GROUPS;

export interface GlyphSection {
	id: GlyphSectionId;
	glyphs: readonly CatalogGlyph[];
}

export interface GlyphCatalog {
	nerd: readonly GlyphSection[];
	emoji: readonly GlyphSection[];
	byChar: ReadonlyMap<string, CatalogGlyph>;
}

function parseCatalog(gen: typeof CatalogGen): GlyphCatalog {
	const byChar = new Map<string, CatalogGlyph>();
	const keep = (g: CatalogGlyph) => {
		byChar.set(g.char, g);
		return g;
	};
	const nerd = (
		Object.keys(gen.NERD_SETS) as (keyof typeof gen.NERD_SETS)[]
	).map((id) => ({
		id,
		glyphs: gen.NERD_SETS[id].split("\n").map((line) => {
			const sp = line.indexOf(" ");
			const label = line.slice(sp + 1);
			return keep({
				label,
				char: String.fromCodePoint(Number.parseInt(line.slice(0, sp), 16)),
				terms: label,
			});
		}),
	}));
	const emoji = (
		Object.keys(gen.EMOJI_GROUPS) as (keyof typeof gen.EMOJI_GROUPS)[]
	).map((id) => ({
		id,
		glyphs: gen.EMOJI_GROUPS[id].split("\n").map((line) => {
			const [char = "", label = "", zh = "", words = ""] = line.split("\t");
			return keep({
				label,
				char,
				zh: zh || undefined,
				terms: `${label} ${zh} ${words}`.toLowerCase(),
			});
		}),
	}));
	return { nerd, emoji, byChar };
}

let catalog: Promise<GlyphCatalog> | undefined;

/** 完整目录（约 9400 个字形、gzip 后约 100 KB）：首次打开选择弹层时才下载，之后复用；失败后下次重试。 */
export function loadGlyphCatalog(): Promise<GlyphCatalog> {
	catalog ??= import("./glyph-catalog.gen").then(parseCatalog, (err) => {
		catalog = undefined;
		throw err;
	});
	return catalog;
}

/** `q` 的字符按顺序出现在 `s` 里（可不连续）。 */
function isSubsequence(q: string, s: string): boolean {
	let i = 0;
	for (const c of s) if (c === q[i] && ++i === q.length) return true;
	return false;
}

/**
 * 模糊匹配：查询按空白切词，每个词都要命中，得分相加；有词没命中返回 0。
 * 单词得分：英文名或中文名开头 4 > 某个词开头 3 > 全文任意位置 2 > 英文名里按序出现（如 `ts` → `language typescript`）1。
 */
export function glyphScore(
	g: Glyph & { zh?: string; terms?: string },
	query: string,
): number {
	const words = query.toLowerCase().split(/\s+/).filter(Boolean);
	const name = g.label.toLowerCase();
	const terms = g.terms ?? name;
	let total = 0;
	for (const w of words) {
		const s =
			terms.startsWith(w) || g.zh?.startsWith(w)
				? 4
				: terms.includes(` ${w}`)
					? 3
					: terms.includes(w)
						? 2
						: isSubsequence(w, name)
							? 1
							: 0;
		if (!s) return 0;
		total += s;
	}
	return total;
}

/** 按 `glyphScore` 筛选并排序；同分时名字短的在前，再按原顺序。 */
export function searchGlyphs<G extends Glyph & { zh?: string; terms?: string }>(
	glyphs: readonly G[],
	query: string,
): G[] {
	return glyphs
		.map((g, i) => ({ g, i, s: glyphScore(g, query) }))
		.filter((x) => x.s > 0)
		.sort(
			(a, b) => b.s - a.s || a.g.label.length - b.g.label.length || a.i - b.i,
		)
		.map((x) => x.g);
}
