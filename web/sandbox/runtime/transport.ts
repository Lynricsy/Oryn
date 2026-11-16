// 页面运行时到控制面的传输层：帧根下的 `POST <root>_oryn/v1`，一条请求一条消息。
// 消息 `{v: 1, id, kind: "use" | "call" | "stream", cap, op?, args?}`；args 是位置参数数组。
// use / call 的应答是一个 JSON 对象 `{v: 1, id, ok, value | error{code, message}}`；
// stream 的应答是 NDJSON：逐行 `{v, id, event}`，正常结束是 `{v, id, done: true}`，出错时最后一行是
// `{v, id, ok: false, error}`。取消流就是中止请求（服务端在连接断开时取消）。
// 能力的命名空间（每个操作怎么暴露给页面）在 capabilities.ts。

/** `window.oryn.use` 的返回：能力的命名空间；没有声明、未获同意或不可用时为 `null`。 */
export type Capability = Readonly<
	Record<string, (...args: unknown[]) => unknown>
>;

/** 能力调用失败时抛出的错误：`code` 取运行时的错误码（`conflict`、`invalid`、`not_allowed` …）。 */
export interface CapabilityError extends Error {
	readonly code: string;
}

/** `use` 等应答的上限；超时返回 `null`。 */
export const USE_TIMEOUT_MS = 10_000;

const CAP_PATTERN = /^[a-z][a-z0-9_]{0,31}$/;
const OP_PATTERN = /^[a-z][A-Za-z0-9_]{0,31}$/;

/** 传输层用到的原生引用：由运行时在页面脚本之前缓存后传入，页面之后改写全局不影响它。 */
export interface TransportNatives {
	fetch: typeof fetch | undefined;
	parse: typeof JSON.parse;
	stringify: typeof JSON.stringify;
	freeze: typeof Object.freeze;
	setTimeout(cb: () => void, ms: number): number;
	clearTimeout(handle: number): void;
	AbortController: typeof AbortController;
	TextDecoder: typeof TextDecoder;
	Error: ErrorConstructor;
}

/** `use` 通过时服务端给的能力描述：全部操作名，其中流式的另列在 `streams` 里。 */
export interface CapabilityInfo {
	ops: string[];
	streams: string[];
}

/** 一个帧的传输：`use` 永不拒绝（任何失败都是 `null`）；`call` 与 `stream` 失败时抛 `CapabilityError`。 */
export interface Transport {
	use(cap: string): Promise<CapabilityInfo | null>;
	call(cap: string, op: string, args: unknown[]): Promise<unknown>;
	/**
	 * 发一次流式调用：每个事件交给 `onEvent`，收到 `done` 时兑现；`signal` 中止时也兑现（调用方自己取消的）。
	 * 末行是错误、连接中途断开或应答不合法时抛 `CapabilityError`。
	 */
	stream(
		cap: string,
		op: string,
		args: unknown[],
		onEvent: (event: unknown) => void,
		signal: AbortSignal,
	): Promise<void>;
}

type Reply =
	| { ok: true; value: unknown }
	| { ok: false; error: { code: string; message: string } };

/**
 * 构造帧的传输：`endpoint` 是运行时端点的绝对地址，为 `null` 时（旧版本的帧、配置不全）任何能力都不可用。
 */
export function createTransport(
	endpoint: string | null,
	n: TransportNatives,
): Transport {
	const doFetch = n.fetch;
	let seq = 0;
	const prefix = Math.random().toString(36).slice(2, 10);
	const nextId = () => `${prefix}.${++seq}`;

	const send = (
		msg: Record<string, unknown>,
		signal: AbortSignal,
	): Promise<Response> => {
		if (!endpoint || !doFetch) throw new n.Error("运行时不可用");
		return doFetch(endpoint, {
			method: "POST",
			headers: { "Content-Type": "application/json" },
			body: n.stringify({ v: 1, id: nextId(), ...msg }),
			credentials: "omit",
			signal,
		});
	};

	const post = async (
		msg: Record<string, unknown>,
		timeoutMs: number | null,
	): Promise<Reply> => {
		const ctl = new n.AbortController();
		const timer =
			timeoutMs === null
				? undefined
				: n.setTimeout(() => ctl.abort(), timeoutMs);
		try {
			const res = await send(msg, ctl.signal);
			return readReply(n.parse(await res.text()), n);
		} finally {
			if (timer !== undefined) n.clearTimeout(timer);
		}
	};

	const use = async (cap: string): Promise<CapabilityInfo | null> => {
		if (typeof cap !== "string" || !CAP_PATTERN.test(cap)) return null;
		let reply: Reply;
		try {
			reply = await post({ kind: "use", cap }, USE_TIMEOUT_MS);
		} catch {
			return null;
		}
		if (!reply.ok) return null;
		const value = reply.value as { ops?: unknown; streams?: unknown } | null;
		if (!value || !Array.isArray(value.ops)) return null;
		const ops: string[] = [];
		for (const op of value.ops)
			if (typeof op === "string" && OP_PATTERN.test(op)) ops.push(op);
		const streams: string[] = [];
		if (Array.isArray(value.streams))
			for (const op of value.streams)
				if (typeof op === "string" && ops.indexOf(op) >= 0) streams.push(op);
		return { ops, streams };
	};

	const call = async (cap: string, op: string, args: unknown[]) => {
		let reply: Reply;
		try {
			reply = await post({ kind: "call", cap, op, args }, null);
		} catch (e) {
			throw capabilityError(n, "failed", errorText(e, "调用失败"));
		}
		if (!reply.ok)
			throw capabilityError(n, reply.error.code, reply.error.message);
		return reply.value;
	};

	const stream = async (
		cap: string,
		op: string,
		args: unknown[],
		onEvent: (event: unknown) => void,
		signal: AbortSignal,
	): Promise<void> => {
		if (signal.aborted) return;
		// 一行处理完：true 表示流已正常结束；错误行抛出
		const handle = (line: string): boolean => {
			let data: unknown;
			try {
				data = n.parse(line);
			} catch {
				throw capabilityError(n, "failed", "应答不是合法的 JSON 行");
			}
			if (typeof data !== "object" || data === null)
				throw capabilityError(n, "failed", "应答不是合法的 JSON 行");
			const r = data as { event?: unknown; done?: unknown; ok?: unknown };
			if ("event" in r) {
				onEvent(r.event);
				return false;
			}
			if (r.done === true) return true;
			const reply = readReply(data, n);
			if (!reply.ok)
				throw capabilityError(n, reply.error.code, reply.error.message);
			return false;
		};
		let reader: ReadableStreamDefaultReader<Uint8Array>;
		try {
			const res = await send({ kind: "stream", cap, op, args }, signal);
			if (!res.body) throw new n.Error("应答没有内容");
			reader = res.body.getReader();
		} catch (e) {
			if (signal.aborted) return;
			throw capabilityError(n, "failed", errorText(e, "调用失败"));
		}
		const decoder = new n.TextDecoder();
		let buf = "";
		try {
			for (;;) {
				const chunk = await reader.read();
				buf += chunk.done
					? decoder.decode()
					: decoder.decode(chunk.value, { stream: true });
				let nl = buf.indexOf("\n");
				while (nl >= 0) {
					const line = buf.slice(0, nl).trim();
					buf = buf.slice(nl + 1);
					if (line !== "" && handle(line)) {
						void reader.cancel().catch(() => {});
						return;
					}
					nl = buf.indexOf("\n");
				}
				if (chunk.done) break;
			}
			if (buf.trim() !== "" && handle(buf.trim())) return;
		} catch (e) {
			if (signal.aborted) return;
			if (isCapabilityError(e)) throw e;
			throw capabilityError(n, "failed", errorText(e, "连接中断"));
		}
		if (signal.aborted) return;
		throw capabilityError(n, "failed", "流意外结束");
	};

	return { use, call, stream };
}

function readReply(data: unknown, n: TransportNatives): Reply {
	if (typeof data !== "object" || data === null)
		throw new n.Error("应答不是对象");
	const r = data as { ok?: unknown; value?: unknown; error?: unknown };
	if (r.ok === true) return { ok: true, value: r.value ?? null };
	const err = (r.error ?? {}) as { code?: unknown; message?: unknown };
	return {
		ok: false,
		error: {
			code: typeof err.code === "string" ? err.code : "failed",
			message: typeof err.message === "string" ? err.message : "调用失败",
		},
	};
}

function errorText(e: unknown, fallback: string): string {
	return e instanceof Error && e.message !== "" ? e.message : fallback;
}

/** 构造带 `code` 的错误（页面拿到的就是它）。 */
export function capabilityError(
	n: TransportNatives,
	code: string,
	message: string,
): CapabilityError {
	const e = new n.Error(message) as Error & { code: string };
	e.name = "OrynError";
	e.code = code;
	return e;
}

function isCapabilityError(e: unknown): e is CapabilityError {
	return (
		e instanceof Error &&
		e.name === "OrynError" &&
		"code" in e &&
		typeof e.code === "string"
	);
}
