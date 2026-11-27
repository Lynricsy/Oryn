// artifact 页面运行时：在页面自己的脚本之前、在不透明源的沙箱里执行。
// 不 import `src/` 下任何依赖应用状态的模块；能共用的只有 `src/lib/artifacts/` 的纯函数与类型。

import { createUse } from "./capabilities";
import { captureDom } from "./dom";
import {
	type DesignFrameInfo,
	fitsMessageLimit,
	type PageMessageBody,
	PROTOCOL_VERSION,
	parseDesignInfo,
	parseParentMessage,
} from "./messages";
import { installParams, type PageParams } from "./params";
import { installSelection } from "./select";
import { createViewerBridge } from "./viewer";

/** 服务端随页面注入的配置：`__orynStart(<配置 JSON>)`。 */
export interface RuntimeConfig {
	v: typeof PROTOCOL_VERSION;
	/** 本次响应给 `data-oryn-src` 用的 8 位标记；查看器页不标源码，为空串。 */
	mark: string;
	/** 页面在 artifact 版本里的路径。 */
	path: string;
	/** 帧根（以 `/` 结尾）：运行时端点是 `<root>_oryn/v1`。缺省为空串，这时能力一律不可用。 */
	root: string;
	/** 跟随最新版本的帧为真；钉住旧版本时为假，只保留滚动与链接拦截。 */
	runtime: boolean;
	/** 设计画布 dev 帧的帧信息（引擎入口给出，静态产物没有）；不合法时丢弃。 */
	design?: DesignFrameInfo;
	/** 类型化 artifact 的查看器页：不装选择模式，改装 `window.__orynViewer` 评论桥。 */
	viewer?: boolean;
}

/** 设计画布静态产物的页面路径 `dist/<帧 id>.html`。 */
const DESIGN_PAGE = /^dist\/([a-z0-9][a-z0-9-]{0,63})\.html$/;

/** 页面所在的设计帧：dev 帧取配置里的 `design.frame`，静态产物从路径推出；不是设计画布的帧为 `null`。 */
export function designFrameOf(config: RuntimeConfig): string | null {
	if (config.design) return config.design.frame;
	return DESIGN_PAGE.exec(config.path)?.[1] ?? null;
}

/**
 * `navigate` 的目标地址（相对当前页）：dev 帧（配置带 `design`）由开发服务按 `/frames/<id>` 提供，是 `./<id>`；
 * 静态产物（`path` 是 `dist/<id>.html`）是同目录的 `./<id>.html`。不是设计画布的帧回 `null`（不跳）。
 */
export function navigateTarget(
	config: RuntimeConfig,
	frame: string,
): string | null {
	if (designFrameOf(config) === null) return null;
	return config.design ? `./${frame}` : `./${frame}.html`;
}

/**
 * 页面加载前缓存的原生引用：页面脚本之后改写这些全局不影响运行时。
 * `parse`、`fetch`、计时器、`AbortController` 与 `TextDecoder` 给能力调用的传输层用；`atob` 与 `Uint8Array`
 * 把 `files.read(path, {as: "bytes"})` 的 base64 转成字节。
 */
export const natives = Object.freeze({
	freeze: Object.freeze,
	defineProperty: Object.defineProperty,
	parse: JSON.parse,
	stringify: JSON.stringify,
	resolve: Promise.resolve.bind(Promise),
	URL,
	fetch: typeof fetch === "function" ? fetch.bind(globalThis) : undefined,
	setTimeout: window.setTimeout.bind(window),
	clearTimeout: window.clearTimeout.bind(window),
	AbortController,
	TextDecoder,
	Error,
	atob: window.atob.bind(window),
	Uint8Array,
});

const MARK_PATTERN = /^[a-z0-9]{8}$/;

/** 配置不合法时按旧版本处理（只保留滚动与链接拦截），不让页面因此打不开。 */
export function readConfig(raw: unknown): RuntimeConfig {
	const c = (typeof raw === "object" && raw !== null ? raw : {}) as Partial<
		Record<keyof RuntimeConfig, unknown>
	>;
	const valid =
		c.v === PROTOCOL_VERSION &&
		typeof c.mark === "string" &&
		(c.mark === "" || MARK_PATTERN.test(c.mark)) &&
		typeof c.path === "string";
	const design = valid ? parseDesignInfo(c.design) : null;
	return {
		v: PROTOCOL_VERSION,
		mark: valid ? (c.mark as string) : "",
		path: valid ? (c.path as string) : "",
		root:
			valid && typeof c.root === "string" && c.root.endsWith("/") ? c.root : "",
		runtime: valid && c.runtime === true,
		...(design ? { design } : {}),
		...(valid && c.viewer === true ? { viewer: true } : {}),
	};
}

/** 运行时端点的绝对地址；旧版本的帧或配置里没有帧根时为 `null`（能力一律不可用）。 */
function endpointOf(config: RuntimeConfig, base: string): string | null {
	if (!config.runtime || config.root === "") return null;
	try {
		return new natives.URL(`${config.root}_oryn/v1`, base).href;
	} catch {
		return null;
	}
}

export type { Capability } from "./transport";

/**
 * 在页面窗口 `win` 上安装运行时；`parent` 是页面加载前缓存的 `window.parent`。
 * 只能调用一次（`window.oryn` 定义后不可再定义）。
 */
export function install(win: Window, parent: Window, raw: unknown): void {
	const config = readConfig(raw);
	const doc = win.document;
	const scrollTo = win.scrollTo.bind(win);
	let channel: string | null = null;
	let domReady = doc.readyState !== "loading";
	// 调节参数在 DOM 就绪后读声明；旧版本的帧也设（只是显示），但不接受预览
	let params: PageParams | null = domReady
		? installParams(doc, natives.parse)
		: null;

	const send = (body: PageMessageBody) => {
		if (channel === null) return;
		const msg = { v: PROTOCOL_VERSION, channel, ...body };
		if (!fitsMessageLimit(msg)) return;
		parent.postMessage(msg, "*");
	};

	// 设计画布的帧：dev 帧取配置里的帧信息，静态产物从路径推出
	const designFrame = designFrameOf(config);

	// ready 要等 DOM 就绪且父页面已用 hello 定下通道（hello 在帧的 load 事件之后才到）。设计画布的帧随后报
	// `navigated`：帧内跳转后新页面重新 ready，父页面据此知道现在显示的是哪一帧
	const sendReady = () => {
		if (!config.runtime || !domReady) return;
		const decl = params?.decl ?? {};
		send(
			config.design
				? { kind: "ready", params: decl, design: config.design }
				: { kind: "ready", params: decl },
		);
		if (designFrame !== null) send({ kind: "navigated", frame: designFrame });
	};

	// 选择与评论模式只在跟随最新版本的帧上有；捕获阶段的指针监听要先于下面的链接拦截注册。
	// 查看器页不装选择模式，点选与图钉由查看器自己做，经 `__orynViewer` 桥与父页面往来
	const viewer = config.viewer
		? createViewerBridge(natives.freeze, send)
		: null;
	const selection =
		config.runtime && !viewer
			? installSelection(captureDom(win), config, send)
			: null;

	win.addEventListener(
		"message",
		(event: MessageEvent) => {
			if (event.source !== parent) return;
			const msg = parseParentMessage(event.data);
			if (!msg) return;
			if (channel === null) {
				if (msg.kind !== "hello") return;
				channel = msg.channel;
			} else if (msg.channel !== channel) {
				return;
			}
			// 协议消息不再交给页面自己的监听器
			event.stopImmediatePropagation();
			switch (msg.kind) {
				case "hello":
					sendReady();
					break;
				case "keep-scroll":
					send({ kind: "scroll", x: win.scrollX, y: win.scrollY });
					break;
				case "restore-scroll":
					scrollTo(msg.x, msg.y);
					break;
				case "params":
					if (config.runtime) params?.preview(msg.values);
					break;
				case "design-reload":
					if (designFrame !== null) win.location.reload();
					break;
				case "navigate": {
					const to = navigateTarget(config, msg.frame);
					if (to !== null && msg.frame !== designFrame)
						win.location.assign(new natives.URL(to, win.location.href).href);
					break;
				}
				default:
					if (viewer) {
						if (config.runtime) viewer.handle(msg);
					} else selection?.handle(msg);
					break;
			}
		},
		true,
	);

	if (!domReady) {
		doc.addEventListener(
			"DOMContentLoaded",
			() => {
				domReady = true;
				params = installParams(doc, natives.parse);
				if (channel !== null) sendReady();
			},
			{ once: true },
		);
	}

	// 跳到别的源的链接一律拦下，交给外壳提示用户在浏览器里打开
	win.addEventListener(
		"click",
		(event: MouseEvent) => {
			const target = event.target as Partial<Element> | null;
			if (typeof target?.closest !== "function") return;
			const link = target.closest("a[href]");
			if (!link) return;
			let url: URL;
			try {
				url = new natives.URL(link.getAttribute("href") ?? "", doc.baseURI);
			} catch {
				return;
			}
			if (url.protocol !== "http:" && url.protocol !== "https:") return;
			if (url.origin === win.location.origin) return;
			event.preventDefault();
			send({ kind: "blocked", url: url.href });
		},
		true,
	);

	// 旧版本的帧也定义它，页面按「不可用」处理而不是报错
	const use = createUse(endpointOf(config, win.location.href), natives);
	// 类型化 artifact 的查看器（与运行时同页）请父页面把帧铺满窗口或还原；不可枚举，不算对页面的公开接口
	natives.defineProperty(win, "__orynPresent", {
		value: (on: unknown) => send({ kind: "present", on: on === true }),
		writable: false,
		configurable: false,
		enumerable: false,
	});
	if (viewer) {
		// 查看器 bundle 的评论桥；冻结、不可枚举、不可改写
		natives.defineProperty(win, "__orynViewer", {
			value: viewer.bridge,
			writable: false,
			configurable: false,
			enumerable: false,
		});
	}
	natives.defineProperty(win, "oryn", {
		value: natives.freeze({ version: 1, use }),
		writable: false,
		configurable: false,
		enumerable: true,
	});
}
