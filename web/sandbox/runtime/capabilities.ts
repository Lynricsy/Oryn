// 能力的命名空间：`oryn.use(cap)` 按服务端给的操作表构造页面拿到的对象。
// 普通操作一律是「按位置传参、返回 Promise」的函数，个别需要改写返回值的在 CALL_ADAPTERS 里登记（例如
// `files.read(path, {as: "bytes"})` 把 base64 转成 Uint8Array）；流式操作没有统一的形状，各自在 STREAM_ADAPTERS
// 里登记怎么暴露（例如 `db.watch(collections, onChange, onError?)` 返回取消函数）。没登记的流式操作不暴露。

import {
	type Capability,
	capabilityError,
	createTransport,
	type Transport,
	type TransportNatives,
} from "./transport";

/** 构造命名空间要用的原生引用：传输层的那些，加上解 base64 用的 `atob` 与 `Uint8Array`。 */
export interface CapabilityNatives extends TransportNatives {
	atob(data: string): string;
	Uint8Array: Uint8ArrayConstructor;
}

type StreamAdapter = (
	t: Transport,
	n: CapabilityNatives,
	cap: string,
	op: string,
) => (...args: unknown[]) => unknown;

type CallAdapter = (
	t: Transport,
	n: CapabilityNatives,
	cap: string,
	op: string,
) => (...args: unknown[]) => Promise<unknown>;

/** `files.read(path, {as: "bytes"})`：服务端回 base64，这里转成 `Uint8Array`；按文本读时原样返回字符串。 */
const bytesReadAdapter: CallAdapter =
	(t, n, cap, op) =>
	async (...args) => {
		const opts = args[1];
		const bytes =
			typeof opts === "object" &&
			opts !== null &&
			"as" in opts &&
			opts.as === "bytes";
		const value = await t.call(cap, op, args);
		if (!bytes || typeof value !== "string") return value;
		const bin = n.atob(value);
		const out = new n.Uint8Array(bin.length);
		for (let i = 0; i < bin.length; i++) out[i] = bin.charCodeAt(i);
		return out;
	};

const CALL_ADAPTERS: Readonly<
	Record<string, Readonly<Record<string, CallAdapter>>>
> = Object.freeze({ files: Object.freeze({ read: bytesReadAdapter }) });

/** 在适配表里查 `cap.op`（只认自有属性，`constructor` 之类的名字查不到）。 */
function adapterOf<A>(
	table: Readonly<Record<string, Readonly<Record<string, A>>>>,
	cap: string,
	op: string,
): A | undefined {
	const ops = Object.hasOwn(table, cap) ? table[cap] : undefined;
	return ops && Object.hasOwn(ops, op) ? ops[op] : undefined;
}

/**
 * `db.watch(collections, onChange, onError?)`：collections 是集合名、集合名数组或 null（全部集合），也可以省略
 * 直接传 onChange。每条变化交给 onChange；流以错误结束（例如监听跟不上时的 `failed`）时把带 `code` 的错误交给
 * onError。返回取消函数，调用后中止请求，不再回调。
 */
const watchAdapter: StreamAdapter =
	(t, n, cap, op) =>
	(...args) => {
		const [collections, onChange, onError] =
			typeof args[0] === "function" ? [null, args[0], args[1]] : args;
		if (typeof onChange !== "function")
			throw new n.Error(`${cap}.${op} 需要一个 onChange 回调`);
		const ctl = new n.AbortController();
		t.stream(
			cap,
			op,
			[collections ?? null],
			(event) => {
				try {
					onChange(event);
				} catch {
					// 页面回调自己的异常不影响监听
				}
			},
			ctl.signal,
		).catch((e: unknown) => {
			if (ctl.signal.aborted || typeof onError !== "function") return;
			try {
				onError(e);
			} catch {
				// 同上
			}
		});
		return () => ctl.abort();
	};

/** 像 AbortSignal 的对象（页面传来的值不可信，只做鸭子类型判断）。 */
function asSignal(v: unknown): AbortSignal | null {
	if (typeof v !== "object" || v === null) return null;
	const s = v as Partial<AbortSignal>;
	return typeof s.aborted === "boolean" &&
		typeof s.addEventListener === "function" &&
		typeof s.removeEventListener === "function"
		? (s as AbortSignal)
		: null;
}

/**
 * `sample.ask(prompt, {format, images, onText, signal}?)`：返回 `{text, json?}`。只有 format 与 images 发给服务端；
 * 回答的增量逐段交给 onText（页面回调自己的异常不影响提问）；signal 中止时中止请求，并以 `signal.reason` 拒绝。
 */
const askAdapter: StreamAdapter =
	(t, n, cap, op) =>
	async (...args) => {
		const [prompt, opts] = args;
		const o =
			typeof opts === "object" && opts !== null
				? (opts as Record<string, unknown>)
				: {};
		const onText = o.onText;
		if (onText !== undefined && typeof onText !== "function")
			throw new n.Error(`${cap}.${op} 的 onText 要是函数`);
		const signal = o.signal === undefined ? null : asSignal(o.signal);
		if (o.signal !== undefined && !signal)
			throw new n.Error(`${cap}.${op} 的 signal 要是 AbortSignal`);
		const wire: Record<string, unknown> = {};
		if (o.format !== undefined) wire.format = o.format;
		if (o.images !== undefined) wire.images = o.images;
		const aborted = () =>
			signal?.reason ?? capabilityError(n, "failed", "提问已取消");
		if (signal?.aborted) throw aborted();
		const ctl = new n.AbortController();
		const onAbort = () => ctl.abort();
		signal?.addEventListener("abort", onAbort);
		let answer: { text: string; json?: unknown } | null = null;
		try {
			await t.stream(
				cap,
				op,
				[prompt, wire],
				(event) => {
					if (typeof event !== "object" || event === null) return;
					const ev = event as {
						type?: unknown;
						text?: unknown;
						json?: unknown;
					};
					if (typeof ev.text !== "string") return;
					if (ev.type === "answer") {
						answer =
							"json" in ev
								? { text: ev.text, json: ev.json }
								: { text: ev.text };
					} else if (ev.type === "text" && typeof onText === "function") {
						try {
							onText(ev.text);
						} catch {
							// 页面回调自己的异常不影响提问
						}
					}
				},
				ctl.signal,
			);
		} finally {
			signal?.removeEventListener("abort", onAbort);
		}
		if (ctl.signal.aborted) throw aborted();
		if (!answer) throw capabilityError(n, "failed", "没有收到回答");
		return n.freeze(answer);
	};

const STREAM_ADAPTERS: Readonly<
	Record<string, Readonly<Record<string, StreamAdapter>>>
> = Object.freeze({
	db: Object.freeze({ watch: watchAdapter }),
	sample: Object.freeze({ ask: askAdapter }),
});

/**
 * 构造 `oryn.use`：`endpoint` 是运行时端点的绝对地址，为 `null` 时（旧版本的帧、配置不全）任何能力都不可用。
 * 返回的函数永不拒绝：失败、超时、能力不可用都得到 `null`；可用时得到冻结的命名空间。
 */
export function createUse(
	endpoint: string | null,
	n: CapabilityNatives,
): (cap: string) => Promise<Capability | null> {
	const t = createTransport(endpoint, n);
	return async (cap: string) => {
		const info = await t.use(cap);
		if (!info) return null;
		const ns: Record<string, (...args: unknown[]) => unknown> = {};
		for (const op of info.ops) {
			if (info.streams.indexOf(op) < 0) {
				const adapt = adapterOf(CALL_ADAPTERS, cap, op);
				ns[op] = adapt
					? adapt(t, n, cap, op)
					: (...args: unknown[]) => t.call(cap, op, args);
				continue;
			}
			const adapt = adapterOf(STREAM_ADAPTERS, cap, op);
			if (adapt) ns[op] = adapt(t, n, cap, op);
		}
		return n.freeze(ns);
	};
}
