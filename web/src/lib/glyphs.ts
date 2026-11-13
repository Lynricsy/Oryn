/**
 * 精选字形：项目图标与 agent 头像的选择网格共用。
 * Nerd 字形的码点取自 Nerd Fonts 3.4 官方 glyphnames（oh-my-pi `utils/nerd-font-glyphs.txt`），按名字逐个核对，不要凭记忆改码点。
 */

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

/** agent 头像：12 个 emoji + 12 个 Nerd 字形。 */
export const AVATAR_GLYPHS: readonly Glyph[] = [
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
