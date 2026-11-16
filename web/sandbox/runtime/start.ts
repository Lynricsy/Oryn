// artifact 页面运行时：在页面自己的脚本之前、在不透明源的沙箱里执行。
// 不 import `src/` 下任何依赖应用状态的模块；能共用的只有 `src/lib/artifacts/` 的纯函数与类型。

import { createUse } from "./capabilities";
import { captureDom } from "./dom";
import {
	fitsMessageLimit,
	type PageMessageBody,
	PROTOCOL_VERSION,
	parseParentMessage,
} from "./messages";
import { installSelection } from "./select";

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
}

/**
 * 页面加载前缓存的原生引用：页面脚本之后改写这些全局不影响运行时。
 * `parse`、`fetch`、计时器、`AbortController` 与 `TextDecoder` 给能力调用的传输层用。
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
	return {
		v: PROTOCOL_VERSION,
		mark: valid ? (c.mark as string) : "",
		path: valid ? (c.path as string) : "",
		root:
			valid && typeof c.root === "string" && c.root.endsWith("/") ? c.root : "",
		runtime: valid && c.runtime === true,
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

	const send = (body: PageMessageBody) => {
		if (channel === null) return;
		const msg = { v: PROTOCOL_VERSION, channel, ...body };
		if (!fitsMessageLimit(msg)) return;
		parent.postMessage(msg, "*");
	};

	// ready 要等 DOM 就绪且父页面已用 hello 定下通道（hello 在帧的 load 事件之后才到）
	const sendReady = () => {
		if (config.runtime && domReady) send({ kind: "ready", params: {} });
	};

	// 选择与评论模式只在跟随最新版本的帧上有；捕获阶段的指针监听要先于下面的链接拦截注册
	const selection = config.runtime
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
				default:
					selection?.handle(msg);
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
	natives.defineProperty(win, "oryn", {
		value: natives.freeze({ version: 1, use }),
		writable: false,
		configurable: false,
		enumerable: true,
	});
}
