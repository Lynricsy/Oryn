// 页面运行时到控制面的传输层：帧根下的 `POST <root>_oryn/v1`，一条请求一条消息。
// 消息 `{v: 1, id, kind: "use" | "call", cap, op?, args?}`，应答 `{v: 1, id, ok, value | error{code, message}}`；
// args 是位置参数数组。阶段 7 在这里追加流式调用（`kind: "stream"`，NDJSON 应答）与取消。

/** `window.oryn.use` 的返回：能力的命名空间；没有声明、未获同意或不可用时为 `null`。 */
export type Capability = Readonly<
	Record<string, (...args: unknown[]) => Promise<unknown>>
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
	Error: ErrorConstructor;
}

type Reply =
	| { ok: true; value: unknown }
	| { ok: false; error: { code: string; message: string } };

/**
 * 构造 `oryn.use`：`endpoint` 是运行时端点的绝对地址，为 `null` 时（旧版本的帧、配置不全）任何能力都不可用。
 * 返回的函数永不拒绝：失败、超时、能力不可用都得到 `null`。
 */
export function createUse(
	endpoint: string | null,
	n: TransportNatives,
): (cap: string) => Promise<Capability | null> {
	const doFetch = n.fetch;
	let seq = 0;
	const prefix = Math.random().toString(36).slice(2, 10);
	const nextId = () => `${prefix}.${++seq}`;

	const post = async (
		msg: Record<string, unknown>,
		timeoutMs: number | null,
	): Promise<Reply> => {
		if (!endpoint || !doFetch) throw new n.Error("运行时不可用");
		const ctl = new n.AbortController();
		const timer =
			timeoutMs === null
				? undefined
				: n.setTimeout(() => ctl.abort(), timeoutMs);
		try {
			const res = await doFetch(endpoint, {
				method: "POST",
				headers: { "Content-Type": "application/json" },
				body: n.stringify({ v: 1, id: nextId(), ...msg }),
				credentials: "omit",
				signal: ctl.signal,
			});
			const data: unknown = n.parse(await res.text());
			return readReply(data, n);
		} finally {
			if (timer !== undefined) n.clearTimeout(timer);
		}
	};

	const call = async (cap: string, op: string, args: unknown[]) => {
		let reply: Reply;
		try {
			reply = await post({ kind: "call", cap, op, args }, null);
		} catch (e) {
			throw capabilityError(
				n,
				"failed",
				e instanceof Error ? e.message : "调用失败",
			);
		}
		if (!reply.ok)
			throw capabilityError(n, reply.error.code, reply.error.message);
		return reply.value;
	};

	return async (cap: string) => {
		if (typeof cap !== "string" || !CAP_PATTERN.test(cap)) return null;
		let reply: Reply;
		try {
			reply = await post({ kind: "use", cap }, USE_TIMEOUT_MS);
		} catch {
			return null;
		}
		if (!reply.ok) return null;
		const value = reply.value as { ops?: unknown } | null;
		if (!value || !Array.isArray(value.ops)) return null;
		const ns: Record<string, (...args: unknown[]) => Promise<unknown>> = {};
		for (const op of value.ops) {
			if (typeof op !== "string" || !OP_PATTERN.test(op)) continue;
			ns[op] = (...args: unknown[]) => call(cap, op, args);
		}
		return n.freeze(ns);
	};
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

function capabilityError(
	n: TransportNatives,
	code: string,
	message: string,
): CapabilityError {
	const e = new n.Error(message) as Error & { code: string };
	e.name = "OrynError";
	e.code = code;
	return e;
}
