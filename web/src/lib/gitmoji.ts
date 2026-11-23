/**
 * 提交标题里的 gitmoji（https://gitmoji.dev）：`:sparkles:` 这类短码换成 emoji（GitHub 也这样显示），
 * 并把标题开头（或 Conventional Commits 前缀 `type(scope): ` 之后）的那个 gitmoji 拆出来，给提交行当前导图标。
 */

/** gitmoji 规范里的全部短码（含 GitHub 的同义短码 `:pencil:`），短码 → emoji。 */
const SHORTCODES: Readonly<Record<string, string>> = {
	art: "🎨",
	zap: "⚡️",
	fire: "🔥",
	bug: "🐛",
	ambulance: "🚑️",
	sparkles: "✨",
	memo: "📝",
	pencil: "📝",
	rocket: "🚀",
	lipstick: "💄",
	tada: "🎉",
	white_check_mark: "✅",
	lock: "🔒️",
	closed_lock_with_key: "🔐",
	bookmark: "🔖",
	rotating_light: "🚨",
	construction: "🚧",
	green_heart: "💚",
	arrow_down: "⬇️",
	arrow_up: "⬆️",
	pushpin: "📌",
	construction_worker: "👷",
	chart_with_upwards_trend: "📈",
	recycle: "♻️",
	heavy_plus_sign: "➕",
	heavy_minus_sign: "➖",
	wrench: "🔧",
	hammer: "🔨",
	globe_with_meridians: "🌐",
	pencil2: "✏️",
	poop: "💩",
	rewind: "⏪️",
	twisted_rightwards_arrows: "🔀",
	package: "📦️",
	alien: "👽️",
	truck: "🚚",
	page_facing_up: "📄",
	boom: "💥",
	bento: "🍱",
	wheelchair: "♿️",
	bulb: "💡",
	beers: "🍻",
	speech_balloon: "💬",
	card_file_box: "🗃️",
	loud_sound: "🔊",
	mute: "🔇",
	busts_in_silhouette: "👥",
	children_crossing: "🚸",
	building_construction: "🏗️",
	iphone: "📱",
	clown_face: "🤡",
	egg: "🥚",
	see_no_evil: "🙈",
	camera_flash: "📸",
	alembic: "⚗️",
	mag: "🔍️",
	label: "🏷️",
	seedling: "🌱",
	triangular_flag_on_post: "🚩",
	goal_net: "🥅",
	dizzy: "💫",
	wastebasket: "🗑️",
	passport_control: "🛂",
	adhesive_bandage: "🩹",
	monocle_face: "🧐",
	coffin: "⚰️",
	test_tube: "🧪",
	necktie: "👔",
	stethoscope: "🩺",
	bricks: "🧱",
	technologist: "🧑‍💻",
	money_with_wings: "💸",
	thread: "🧵",
	safety_vest: "🦺",
	airplane: "✈️",
	"t-rex": "🦖",
};

const SHORTCODE_RE = /:([a-z0-9_+-]+):/g;
/** 开头的可选 Conventional Commits 前缀（`type(scope)!: `），随后是一个 emoji 或短码，再是空白与正文 */
const LEAD_RE =
	/^([A-Za-z]+(?:\([^)]*\))?!?:\s*)?(\p{Extended_Pictographic}(?:\uFE0F|\u200D\p{Extended_Pictographic})*\uFE0F?|:[a-z0-9_+-]+:)\s+(\S.*)$/su;

/** 把已知的 gitmoji 短码换成 emoji；不认识的短码原样保留。 */
export function emojify(text: string): string {
	if (!text.includes(":")) return text;
	return text.replace(SHORTCODE_RE, (m, code: string) => SHORTCODES[code] ?? m);
}

export interface CommitSubject {
	/** 拆出来的 gitmoji（emoji 形式）；没有时缺省 */
	glyph?: string;
	/** 去掉 gitmoji 后的标题（短码已换成 emoji），Conventional Commits 前缀保留 */
	text: string;
}

export function parseCommitSubject(subject: string): CommitSubject {
	const m = LEAD_RE.exec(subject);
	if (m) {
		const [, prefix = "", lead = "", rest = ""] = m;
		const glyph = lead.startsWith(":") ? SHORTCODES[lead.slice(1, -1)] : lead;
		if (glyph) return { glyph, text: emojify(prefix + rest) };
	}
	return { text: emojify(subject) };
}
