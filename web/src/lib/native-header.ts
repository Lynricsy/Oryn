/**
 * iOS 原生页头（iOS 26 Liquid Glass：侧栏圆钮、两行标题胶囊、视图菜单圆钮与它们的弹出菜单）的网页侧控制器。
 * 原生是哑执行器，策略全在这里（网页随服务器更新，App 发版要走 TestFlight）。
 * - 跟随卡片：卡片开合是 WKWebView 根滚动视图的横向滚动（`native-sink.ts`），原生按 contentOffset 在同一进程同一帧里
 *   平移整层、离开关闭位时只显示不接触摸，网页不发位移，也不需要对时；这里只管透明度（显示 / 让位）。
 * - 让位：网页内容盖住页头（对话框 / 命令面板 / 提示条、切到面板标签、页面被软键盘推走）时原生隐藏、网页页头顶上；
 *   遮挡只在卡片静止关闭时判定（卡片动着或打开时，露出的卡片条上的关闭按钮会盖住页头的位置，那不是遮挡）。
 *   回来时原生 120ms 淡入，网页三件再晚 HEADER_HANDOFF_MS 才撤（短暂重叠，不留空档）。
 * - 内容：各部件（`CompactThreadHeader`、`ThreadViewMenu`）每次渲染登记自己的一份（`useNativeHeaderPart`），
 *   这里合成一份声明式 spec，按 JSON 去重、在途至多一条。原生点击回传菜单项的路径 id，网页在最新登记的部件里找回调执行。
 * - 菜单的开合与子页切换是原生的本地状态，打开 / 关闭 / 选中再上报；网页只在 Esc、软键盘弹出时强制收起。
 * - 前提：根滚动跟随已接管（`SinkGate.engaged()`）；没接管时一律不要原生页头，接管后补发。
 * - 回退：宿主没有原生页头（浏览器、Android、桌面、iOS bridge < 8）、`set` 被拒（iOS < 26、没挂上）、或 `NATIVE_HEADER`
 *   为 false 时，本页一直用网页页头，行为与以前完全相同。改 `NATIVE_HEADER` 并升级服务器即可全量回退，不需要发版。
 */
import {
	type RefObject,
	useEffect,
	useLayoutEffect,
	useState,
	useSyncExternalStore,
} from "react";
import type { IconName } from "../components/ui/icons";
import {
	host,
	type NativeFrame,
	type NativeHeaderEvent,
	type NativeHeaderHost,
	type NativeHeaderShow,
	type NativeHeaderSpec,
	type NativeIcon,
	type NativeMark,
	type NativeMenu,
	type NativeMenuItem,
	type NativeMenuKind,
	type NativeSeg,
} from "./host";
import { toHexColor } from "./host-chrome";
import { latest, nativeSink, type SinkGate, sinkRest } from "./native-sink";
import { COVER_POLL_MS } from "./native-tab-bar";

/** 总开关：false = 所有 App 都用网页页头。 */
export const NATIVE_HEADER = true;
/** 原生页头淡入的时长；网页页头再晚 HEADER_HANDOFF_MS 才撤（两者短暂重叠，不留空档）。 */
export const HEADER_FADE_MS = 120;
/** 交接余量（毫秒）：原生淡入完、再过这么久网页三件才隐藏，抵消桥的送达延迟。 */
export const HEADER_HANDOFF_MS = 34;
/** 遮罩不透明度（= `--scrim` 的 alpha）与面板底色着色强度。 */
export const HEADER_TUNE = {
	scrimLight: 0.35,
	scrimDark: 0.6,
	tint: 0.55,
} as const;
/**
 * 与壳 `mobile_logic::valid_header` 的上限一致：超出的在这里截断，不让整份 spec 被拒。
 * 字符串按码点计（`copy` 按 UTF-8 字节）；`button` = 圆钮的读屏名，`menu` = 菜单名，`text` = 返回 / 关闭的读屏文案。
 */
export const HEADER_LIMIT = {
	label: 200,
	title: 300,
	a11y: 600,
	error: 300,
	seg: 120,
	hint: 32,
	copy: 2048,
	button: 64,
	menu: 400,
	text: 32,
	quick: 4,
	status: 32,
	groups: 8,
	group: 24,
	children: 24,
	meta: 6,
	marks: 6,
} as const;

/** 网页图标 → SF Symbol；没列的用 circle。 */
export const SF_ICON: Partial<Record<IconName, string>> = {
	sidebar: "sidebar.left",
	views: "square.grid.2x2",
	archive: "archivebox",
	share: "square.and.arrow.up",
	tree: "point.3.connected.trianglepath.dotted",
	device: "laptopcomputer.and.iphone",
	box: "cube",
	info: "info.circle",
	pause: "pause",
	play: "play",
	trash: "trash",
	clock: "clock",
	repeat: "clock.arrow.2.circlepath",
	globe: "globe",
	pullRequest: "arrow.triangle.pull",
	merge: "arrow.triangle.merge",
	edit: "pencil",
	list: "checklist",
	target: "target",
	pin: "pin",
	rocket: "shippingbox",
	fork: "arrow.triangle.branch",
	file: "doc",
	chart: "chart.bar",
	copy: "doc.on.doc",
	bug: "ladybug",
	message: "bubble.left",
	diff: "plus.forwardslash.minus",
	folder: "folder",
	terminal: "terminal",
	shapes: "square.on.circle",
	monitor: "display",
};

/** 侧栏钮与视图钮的 SF Symbol。 */
const LEAD_SYMBOL = "sidebar.left";
const TRAIL_SYMBOL = "square.grid.2x2";

/** 菜单项里给原生用的补充（`MenuItem.native`）：label 不是字符串时必须给 `label`。 */
export interface NativeHint {
	label?: string;
	icon?: NativeIcon;
	sub?: NativeSeg;
	dot?: "accent" | "warn";
	pulse?: boolean;
	copy?: string;
}

/** `MenuItem` 里与原生菜单有关的字段（结构上兼容 `MenuItem`）。 */
export interface MenuSource {
	key: string;
	label: unknown;
	icon?: IconName;
	hint?: unknown;
	checked?: boolean;
	danger?: boolean;
	disabled?: boolean;
	children?: readonly MenuSource[];
	native?: NativeHint;
}

/** 按码点截断（与 Rust 的 `chars().count()` 同口径）。 */
export function clip(s: string, max: number): string {
	const cps = [...s];
	return cps.length > max ? cps.slice(0, max).join("") : s;
}

/** 按 UTF-8 字节截断，不切开码点（Rust 那边按 `len()` 校验）。 */
function clipBytes(s: string, max: number): string {
	let bytes = 0;
	let out = "";
	for (const cp of s) {
		const c = cp.codePointAt(0) ?? 0;
		const n = c < 0x80 ? 1 : c < 0x800 ? 2 : c < 0x10000 ? 3 : 4;
		if (bytes + n > max) break;
		bytes += n;
		out += cp;
	}
	return out;
}

/** oklch → `#rrggbb`（OKLab → 线性 sRGB → gamma，逐通道钳到 [0, 1]）。h 为角度。 */
export function oklchHex(l: number, c: number, h: number): string {
	const rad = (h * Math.PI) / 180;
	const a = c * Math.cos(rad);
	const b = c * Math.sin(rad);
	const l3 = (l + 0.3963377774 * a + 0.2158037573 * b) ** 3;
	const m3 = (l - 0.1055613458 * a - 0.0638541728 * b) ** 3;
	const s3 = (l - 0.0894841775 * a - 1.291485548 * b) ** 3;
	const linear = [
		4.0767416621 * l3 - 3.3077115913 * m3 + 0.2309699292 * s3,
		-1.2684380046 * l3 + 2.6097574011 * m3 - 0.3413193965 * s3,
		-0.0041960863 * l3 - 0.7034186147 * m3 + 1.707614701 * s3,
	];
	return `#${linear
		.map((v) => {
			const x = Math.min(1, Math.max(0, v));
			const g = x <= 0.0031308 ? 12.92 * x : 1.055 * x ** (1 / 2.4) - 0.055;
			return Math.round(g * 255)
				.toString(16)
				.padStart(2, "0");
		})
		.join("")}`;
}

/** `.avatar` 的底色与字色（index.css：浅色 0.89/0.075 与 0.4/0.13，深色 0.34/0.08 与 0.87/0.1）。 */
export function avatarHex(
	hue: number,
	dark: boolean,
): { bg: string; fg: string } {
	return dark
		? { bg: oklchHex(0.34, 0.08, hue), fg: oklchHex(0.87, 0.1, hue) }
		: { bg: oklchHex(0.89, 0.075, hue), fg: oklchHex(0.4, 0.13, hue) };
}

/** `.thread-mark` 的颜色（浅色 0.64/0.15，深色 0.74/0.14）。 */
export function markHex(hue: number, dark: boolean): string {
	return dark ? oklchHex(0.74, 0.14, hue) : oklchHex(0.64, 0.15, hue);
}

/** `<html data-theme="dark">`。 */
export function isDark(): boolean {
	return (
		typeof document !== "undefined" &&
		document.documentElement.dataset.theme === "dark"
	);
}

/** 一段文字截断；空文字整段丢弃（原生要求 1 个字符起）。 */
function nativeSeg(s: NativeSeg): NativeSeg | null {
	const text = clip(s.text, HEADER_LIMIT.seg);
	if (!text) return null;
	const out: NativeSeg = { text };
	if (s.icon) out.icon = s.icon;
	if (s.tone) out.tone = s.tone;
	return out;
}

function nativeList(
	items: readonly MenuSource[],
	prefix: string,
	top: boolean,
): NativeMenuItem[] {
	const out: NativeMenuItem[] = [];
	for (const src of items) {
		const id = prefix + src.key;
		const n = src.native;
		const raw = n?.label ?? (typeof src.label === "string" ? src.label : "");
		if (!raw) {
			console.warn("[host] native header: 菜单项没有纯文本名字，已略去", id);
			continue;
		}
		const item: NativeMenuItem = { id, label: clip(raw, HEADER_LIMIT.label) };
		const icon =
			n?.icon ??
			(src.icon ? { symbol: SF_ICON[src.icon] ?? "circle" } : undefined);
		if (icon) item.icon = icon;
		const sub = n?.sub && nativeSeg(n.sub);
		if (sub) item.sub = sub;
		if (typeof src.hint === "string" && src.hint)
			item.hint = clip(src.hint, HEADER_LIMIT.hint);
		if (n?.dot) item.dot = n.dot;
		if (n?.pulse) item.pulse = true;
		if (src.checked !== undefined) item.checked = src.checked;
		if (src.danger) item.danger = true;
		if (src.disabled) item.disabled = true;
		if (n?.copy) item.copy = clipBytes(n.copy, HEADER_LIMIT.copy);
		if (top && src.children?.length) {
			const kids = nativeList(src.children, `${id}/`, false).slice(
				0,
				HEADER_LIMIT.children,
			);
			if (kids.length > 0) item.children = kids;
		}
		out.push(item);
	}
	return out;
}

/**
 * 菜单源 → 原生菜单项：id = prefix + key；label 取 `native.label`，否则取字符串 label（都没有的项丢弃并 console.warn）；
 * 图标取 `native.icon`，否则 `{symbol: SF_ICON[icon] ?? "circle"}`（源没有图标就不带）；hint 只认字符串；
 * 只展开一级子项（子项 id = `<id>/<key>`）；布尔字段只在为 true 时输出（`checked` 例外：给出就输出）；
 * 各字符串按 HEADER_LIMIT 截断，子项超过 24 个截掉。
 */
export function nativeItems(
	items: readonly MenuSource[],
	prefix = "",
): NativeMenuItem[] {
	return nativeList(items, prefix, true);
}

/** 按 `a/b` 路径在菜单源里找项（只有一级子项）。 */
export function findMenuItem<
	T extends { key: string; children?: readonly T[] },
>(items: readonly T[], id: string): T | undefined {
	const at = id.indexOf("/");
	if (at < 0) return items.find((x) => x.key === id);
	const parent = items.find((x) => x.key === id.slice(0, at));
	return parent?.children?.find((x) => x.key === id.slice(at + 1));
}

export interface HeaderParts {
	theme: {
		dark: boolean;
		palette: NativeHeaderSpec["palette"];
		text: NativeHeaderSpec["text"];
	};
	lead: { frame: NativeFrame; label: string; onTap(): void };
	title: {
		frame: NativeFrame;
		key: string;
		text: string;
		icon?: NativeIcon;
		error?: string;
		meta: NativeSeg[];
		marks: NativeMark[];
		label: string;
		menu: NativeMenu;
		onSelect(id: string): void;
	};
	trail: {
		frame: NativeFrame;
		label: string;
		dot?: "accent" | "warn";
		menu: NativeMenu;
		onSelect(id: string): void;
	};
}

function specMenu(m: NativeMenu): NativeMenu {
	return {
		label: clip(m.label, HEADER_LIMIT.menu),
		dim: m.dim,
		quick: m.quick.slice(0, HEADER_LIMIT.quick),
		status: m.status.slice(0, HEADER_LIMIT.status),
		groups: m.groups
			.filter((g) => g.length > 0)
			.slice(0, HEADER_LIMIT.groups)
			.map((g) => g.slice(0, HEADER_LIMIT.group)),
	};
}

/**
 * 合成整份 spec：去掉回调；lead.symbol = "sidebar.left"、trail.symbol = "square.grid.2x2"；
 * tune = {scrim: dark ? scrimDark : scrimLight, tint}；title.text / label / error / 各段文字按 HEADER_LIMIT 截断，
 * quick / status / groups / 每组 / meta / marks 按上限截掉多的（空组去掉）；没有 trail 时 `trail: null`。
 */
export function buildHeaderSpec(
	p: Pick<HeaderParts, "theme" | "lead" | "title"> & {
		trail: HeaderParts["trail"] | null;
	},
): NativeHeaderSpec {
	const { theme, lead, title, trail } = p;
	const t: NativeHeaderSpec["title"] = {
		frame: { ...title.frame },
		text: clip(title.text, HEADER_LIMIT.title),
		meta: title.meta
			.flatMap((s) => nativeSeg(s) ?? [])
			.slice(0, HEADER_LIMIT.meta),
		marks: title.marks
			.slice(0, HEADER_LIMIT.marks)
			.map((m) =>
				m.dim ? { symbol: m.symbol, dim: true } : { symbol: m.symbol },
			),
		label: clip(title.label, HEADER_LIMIT.a11y),
		menu: specMenu(title.menu),
	};
	if (title.icon) t.icon = title.icon;
	if (title.error) t.error = clip(title.error, HEADER_LIMIT.error);
	return {
		key: title.key,
		dark: theme.dark,
		palette: { ...theme.palette },
		tune: {
			scrim: theme.dark ? HEADER_TUNE.scrimDark : HEADER_TUNE.scrimLight,
			tint: HEADER_TUNE.tint,
		},
		text: {
			back: clip(theme.text.back, HEADER_LIMIT.text),
			close: clip(theme.text.close, HEADER_LIMIT.text),
		},
		lead: {
			frame: { ...lead.frame },
			symbol: LEAD_SYMBOL,
			label: clip(lead.label, HEADER_LIMIT.button),
		},
		title: t,
		trail: trail
			? {
					frame: { ...trail.frame },
					symbol: TRAIL_SYMBOL,
					label: clip(trail.label, HEADER_LIMIT.button),
					...(trail.dot && { dot: trail.dot }),
					menu: specMenu(trail.menu),
				}
			: null,
	};
}

/**
 * 页头位置上最上面的元素不是它自己的某一件（弹层、提示、侧栏的关闭区、切到面板标签），
 * 或布局坐标与屏幕坐标对不上（页面被软键盘推走、缩放）。
 * 注意：软键盘弹出本身不算被盖住（与底栏的 slotCovered 不同）——打字时页头照常是原生的。
 */
export function slotsCovered(slots: Iterable<Element>, win: Window): boolean {
	const vv = win.visualViewport;
	if (
		vv &&
		(Math.abs(vv.offsetTop) > 0.5 ||
			Math.abs(vv.offsetLeft) > 0.5 ||
			Math.abs(vv.scale - 1) > 0.01)
	)
		return true;
	if (Math.abs(win.scrollY) > 0.5) return true;
	let n = 0;
	for (const slot of slots) {
		n++;
		const r = slot.getBoundingClientRect();
		if (r.width <= 0 || r.height <= 0) return true;
		const y = r.top + r.height / 2;
		for (const f of r.width > 60 ? [0.15, 0.5, 0.85] : [0.5]) {
			const hit = win.document.elementFromPoint(r.left + r.width * f, y);
			if (!hit || !slot.contains(hit)) return true;
		}
	}
	return n === 0;
}

export interface NativeHeader {
	/** 原生页头已经显示稳了：网页三件应隐藏（开合途中也一直为真）。 */
	active(): boolean;
	/** 已接管（可能暂时让位）。 */
	engaged(): boolean;
	/** 原生菜单开着哪一个。 */
	menu(): NativeMenuKind | null;
	subscribe(cb: () => void): () => void;
	/** 各部件登记自己的那份（null = 撤掉）；theme、lead、title 到齐才发 spec，缺一则撤掉原生页头。 */
	setPart<K extends keyof HeaderParts>(
		part: K,
		value: HeaderParts[K] | null,
	): void;
	setCovered(covered: boolean): void;
	/** 网页强制收起原生菜单（Esc、软键盘弹出）。 */
	closeMenu(): void;
}

export interface HeaderEnv {
	setTimeout(cb: () => void, ms: number): number;
	clearTimeout(id: number): void;
	microtask(cb: () => void): void;
	/** 让输入框失焦（收起软键盘）。 */
	blur(): void;
	reduce(): boolean;
}

const browserEnv: HeaderEnv = {
	setTimeout: (cb, ms) => window.setTimeout(cb, ms),
	clearTimeout: (id) => window.clearTimeout(id),
	microtask: (cb) => queueMicrotask(cb),
	blur: () => (document.activeElement as HTMLElement | null)?.blur?.(),
	reduce: () =>
		window.matchMedia?.("(prefers-reduced-motion: reduce)").matches ?? false,
};

export function createNativeHeader(
	api: NativeHeaderHost | null,
	listen: (cb: (e: NativeHeaderEvent) => void) => () => void,
	sink: SinkGate,
	env: HeaderEnv = browserEnv,
): NativeHeader {
	let disabled = api === null; // set 被拒后本页不再尝试
	const parts: Partial<HeaderParts> = {};
	let wanted = ""; // 想要的 spec 的 JSON；"" = 不要原生页头
	let key: string | null = null;
	let engaged = false;
	let covered = false; // 生效中的遮挡
	let coveredRaw = false; // 最近一次探测结果（菜单开着时先不生效）
	let want = false; // 已要求原生显示
	let shown = false; // 原生显示稳了（网页该隐藏）
	let menu: NativeMenuKind | null = null;
	let fadeTimer = 0;
	let queued = false;
	const subs = new Set<() => void>();
	const notify = () => {
		for (const cb of [...subs]) cb();
	};
	const sendShow = latest<NativeHeaderShow>(
		(s) => (api as NativeHeaderHost).show(s),
		"native header",
	);

	const evaluate = () => {
		const next = engaged && !covered;
		if (next === want) return;
		want = next;
		env.clearTimeout(fadeTimer);
		if (next) {
			const ms = env.reduce() ? 0 : HEADER_FADE_MS;
			sendShow({ alpha: 1, ms });
			// 网页页头晚撤：等原生淡入完（再加一段桥的延迟），两者重叠而不是留空档
			fadeTimer = env.setTimeout(() => {
				shown = true;
				notify();
			}, ms + HEADER_HANDOFF_MS);
			return;
		}
		// 让位：网页页头立刻回来，原生立刻隐藏（它会自己收起菜单）
		const changed = shown || menu !== null;
		shown = false;
		menu = null;
		if (changed) notify();
		sendShow({ alpha: 0, ms: 0 });
	};

	const drop = () => {
		// 原生页头没了（撤掉 / 被拒）：全部状态归零，不再发 show
		env.clearTimeout(fadeTimer);
		const changed = engaged || shown || menu !== null;
		engaged = false;
		want = false;
		shown = false;
		menu = null;
		if (changed) notify();
	};

	const sendSpec = latest<NativeHeaderSpec | null>(async (spec) => {
		// 已回退：排在被拒那条之后的 spec 不再发（撤掉照发）
		if (disabled && spec !== null) return;
		try {
			await (api as NativeHeaderHost).set(spec);
		} catch (e) {
			if (spec === null) return;
			// 原生不可用（iOS < 26、插件缺失、没挂上）：本页回到网页页头，不再尝试
			disabled = true;
			wanted = "";
			drop();
			throw e;
		}
		if (spec === null || wanted === "" || engaged) return;
		engaged = true;
		notify();
		evaluate();
	}, "native header");

	const sync = () => {
		queued = false;
		if (disabled) return;
		const { theme, lead, title } = parts;
		const spec =
			sink.engaged() && theme && lead && title
				? buildHeaderSpec({ theme, lead, title, trail: parts.trail ?? null })
				: null;
		const next = spec ? JSON.stringify(spec) : "";
		if (next === wanted) return;
		wanted = next;
		if (!spec) drop();
		else if (spec.key !== key) {
			// 换了线程：原生会自己收起菜单
			key = spec.key;
			if (menu !== null) {
				menu = null;
				notify();
			}
		}
		sendSpec(spec);
	};
	// 同一轮里几个部件先后登记、或根滚动跟随接管 / 撤掉：合成一次
	const schedule = () => {
		if (queued) return;
		queued = true;
		env.microtask(sync);
	};
	sink.subscribe(schedule);

	const settleCover = () => {
		if (covered === coveredRaw) return;
		covered = coveredRaw;
		evaluate();
	};

	listen((e) => {
		// 上一个线程的菜单晚到的事件不执行
		if (!engaged || e.key !== key) return;
		if (e.op === "lead") parts.lead?.onTap();
		else if (e.op === "menu") {
			if (e.menu === menu) return;
			menu = e.menu;
			notify();
			if (menu) env.blur();
			else settleCover();
		} else {
			if (menu !== null) {
				menu = null;
				notify();
			}
			settleCover();
			(e.menu === "title" ? parts.title : parts.trail)?.onSelect(e.id);
		}
	});

	return {
		active: () => shown,
		engaged: () => engaged,
		menu: () => menu,
		subscribe(cb) {
			subs.add(cb);
			return () => {
				subs.delete(cb);
			};
		},
		setPart(part, value) {
			if (value) parts[part] = value;
			else delete parts[part];
			schedule();
		},
		setCovered(next) {
			coveredRaw = next;
			// 菜单开着时不因为盖住而让位（提示条冒出来不该把菜单关掉）；菜单关了再生效
			if (next && menu !== null) return;
			if (next === covered) return;
			covered = next;
			evaluate();
		},
		closeMenu() {
			if (menu === null) return;
			menu = null;
			notify();
			settleCover();
			void (api as NativeHeaderHost)
				.menu({ open: null })
				.catch((e) => console.warn("[host] native header", e));
		},
	};
}

/** 本页的原生页头控制器；没有原生能力时一切空操作。 */
export const nativeHeader: NativeHeader = createNativeHeader(
	NATIVE_HEADER ? (host.mobile?.header ?? null) : null,
	(cb) => host.onHeader(cb),
	nativeSink,
);

/** 原生页头显示稳了（网页三件应隐藏）。 */
export function useNativeHeaderActive(): boolean {
	return useSyncExternalStore(
		nativeHeader.subscribe,
		nativeHeader.active,
		() => false,
	);
}

/** 原生页头已接管（可能暂时让位）。 */
export function useNativeHeaderEngaged(): boolean {
	return useSyncExternalStore(
		nativeHeader.subscribe,
		nativeHeader.engaged,
		() => false,
	);
}

/** 原生菜单开着哪一个。 */
export function useNativeHeaderMenu(): NativeMenuKind | null {
	return useSyncExternalStore(
		nativeHeader.subscribe,
		nativeHeader.menu,
		() => null,
	);
}

/** 每次渲染后把这一份交给控制器（内容没变时控制器不会重发）；卸载或 `on` 变假时撤掉。`on` 为假时什么也不做。 */
export function useNativeHeaderPart<K extends keyof HeaderParts>(
	part: K,
	value: HeaderParts[K] | null,
	on = true,
): void {
	// 不带依赖：回调（onSelect / onTap）每次渲染都换成最新的
	useEffect(() => {
		if (on) nativeHeader.setPart(part, value);
	});
	useEffect(
		() => (on ? () => nativeHeader.setPart(part, null) : undefined),
		[part, on],
	);
}

const round2 = (v: number) => Math.round(v * 100) / 100;

/**
 * 控件相对所在下沉卡片（`.sink-card`，没有就相对视口）左上角的矩形，各保留两位小数：卡片右移时这个值不变，
 * 而卡片静止关闭时它就是视口坐标。`resize` 与 ResizeObserver 触发重量，值没变不更新。还没量到或宽高为 0 时为 null。
 */
export function useSlotFrame(
	ref: RefObject<Element | null>,
	on = true,
): NativeFrame | null {
	const [frame, setFrame] = useState<NativeFrame | null>(null);
	useLayoutEffect(() => {
		const el = ref.current;
		if (!on || !el) {
			setFrame(null);
			return;
		}
		const measure = () => {
			const r = el.getBoundingClientRect();
			const o = el.closest(".sink-card")?.getBoundingClientRect();
			const next =
				r.width > 0 && r.height > 0
					? {
							x: round2(r.left - (o?.left ?? 0)),
							y: round2(r.top - (o?.top ?? 0)),
							width: round2(r.width),
							height: round2(r.height),
						}
					: null;
			setFrame((prev) =>
				prev &&
				next &&
				prev.x === next.x &&
				prev.y === next.y &&
				prev.width === next.width &&
				prev.height === next.height
					? prev
					: next,
			);
		};
		measure();
		window.addEventListener("resize", measure);
		const ro =
			typeof ResizeObserver === "undefined"
				? null
				: new ResizeObserver(measure);
		ro?.observe(el);
		return () => {
			window.removeEventListener("resize", measure);
			ro?.disconnect();
		};
	}, [ref, on]);
	return on ? frame : null;
}

const PALETTE_KEYS = [
	"accent",
	"brand",
	"err",
	"warn",
	"good",
	"frost",
] as const;

/**
 * 深浅色与调色板：读 `probe` 下六个 `[data-k]` 子元素的计算 color（`toHexColor`），跟随 `<html>` 属性变化与系统明暗。
 * 任何一个换算不出来（jsdom）时为 null。
 */
export function useHeaderTheme(
	probe: RefObject<HTMLElement | null>,
): { dark: boolean; palette: NativeHeaderSpec["palette"] } | null {
	const [theme, setTheme] = useState<{
		dark: boolean;
		palette: NativeHeaderSpec["palette"];
	} | null>(null);
	useLayoutEffect(() => {
		let last = "";
		const read = () => {
			const root = probe.current;
			const palette: Partial<NativeHeaderSpec["palette"]> = {};
			for (const k of PALETTE_KEYS) {
				const el = root?.querySelector(`[data-k="${k}"]`);
				const css = el ? getComputedStyle(el).color : "";
				const hex = css ? toHexColor(css) : null;
				if (!hex) break;
				palette[k] = hex;
			}
			const next =
				Object.keys(palette).length === PALETTE_KEYS.length
					? {
							dark: isDark(),
							palette: palette as NativeHeaderSpec["palette"],
						}
					: null;
			const id = JSON.stringify(next);
			if (id === last) return;
			last = id;
			setTheme(next);
		};
		read();
		// 主题切换写 `<html>` 的 class / data-* / style
		const mo = new MutationObserver(read);
		mo.observe(document.documentElement, { attributes: true });
		const mq = window.matchMedia?.("(prefers-color-scheme: dark)");
		mq?.addEventListener("change", read);
		return () => {
			mo.disconnect();
			mq?.removeEventListener("change", read);
		};
	}, [probe]);
	return theme;
}

/**
 * 遮挡探测与菜单的强制收起，只在原生已接管时生效。`visible` 为假（转录纸面被面板标签盖住）时直接算被盖住。
 * 探测时机：生效时立即一次；`document.body` 的直接子节点增删（对话框、命令面板的 portal）、抬手、松键、
 * visualViewport 的 resize / scroll 之后下一帧；另每 COVER_POLL_MS 一次兜底（文档不可见时跳过）。
 * 探测对象是 `root` 里所有 `[data-native-slot]`。
 * 原生菜单开着时：Esc → closeMenu()；软键盘弹出（innerHeight − vv.height − vv.offsetTop > 1）→ closeMenu()。
 */
export function useHeaderCover(
	root: RefObject<HTMLElement | null>,
	visible: boolean,
): void {
	const engaged = useNativeHeaderEngaged();
	const menu = useNativeHeaderMenu();

	useLayoutEffect(() => {
		if (!engaged) return;
		if (!visible) {
			nativeHeader.setCovered(true);
			return;
		}
		let frame = 0;
		const check = () => {
			// 卡片不在关闭位（开着或正在开合）时不判定：露出的卡片条上的关闭按钮压在页头位置上，那不是遮挡；
			// 回到关闭位后由根滚动的 scroll 事件补探
			if (sinkRest(window) !== "closed") return;
			nativeHeader.setCovered(
				slotsCovered(
					root.current?.querySelectorAll("[data-native-slot]") ?? [],
					window,
				),
			);
		};
		const soon = () => {
			if (frame) return;
			frame = requestAnimationFrame(() => {
				frame = 0;
				check();
			});
		};
		check();
		const poll = window.setInterval(() => {
			if (document.visibilityState !== "hidden") check();
		}, COVER_POLL_MS);
		const mo = new MutationObserver(soon);
		mo.observe(document.body, { childList: true });
		window.addEventListener("scroll", soon, { passive: true });
		const vv = window.visualViewport;
		document.addEventListener("pointerup", soon, true);
		document.addEventListener("keyup", soon, true);
		vv?.addEventListener("resize", soon);
		vv?.addEventListener("scroll", soon);
		return () => {
			window.clearInterval(poll);
			cancelAnimationFrame(frame);
			mo.disconnect();
			window.removeEventListener("scroll", soon);
			document.removeEventListener("pointerup", soon, true);
			document.removeEventListener("keyup", soon, true);
			vv?.removeEventListener("resize", soon);
			vv?.removeEventListener("scroll", soon);
		};
	}, [engaged, visible, root]);

	useLayoutEffect(() => {
		if (menu === null) return;
		const onKey = (e: KeyboardEvent) => {
			if (e.key === "Escape") nativeHeader.closeMenu();
		};
		const vv = window.visualViewport;
		const onResize = () => {
			if (vv && window.innerHeight - vv.height - vv.offsetTop > 1)
				nativeHeader.closeMenu();
		};
		window.addEventListener("keydown", onKey);
		vv?.addEventListener("resize", onResize);
		return () => {
			window.removeEventListener("keydown", onKey);
			vv?.removeEventListener("resize", onResize);
		};
	}, [menu]);
}
