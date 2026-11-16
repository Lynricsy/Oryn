// 能力的命名空间：`oryn.use(cap)` 按服务端给的操作表构造页面拿到的对象。
// 普通操作一律是「按位置传参、返回 Promise」的函数；流式操作没有统一的形状，各自在 STREAM_ADAPTERS 里登记
// 怎么暴露（例如 `db.watch(collections, onChange, onError?)` 返回取消函数）。没登记的流式操作不暴露。

import {
	type Capability,
	createTransport,
	type Transport,
	type TransportNatives,
} from "./transport";

type StreamAdapter = (
	t: Transport,
	n: TransportNatives,
	cap: string,
	op: string,
) => (...args: unknown[]) => unknown;

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

const STREAM_ADAPTERS: Readonly<
	Record<string, Readonly<Record<string, StreamAdapter>>>
> = Object.freeze({ db: Object.freeze({ watch: watchAdapter }) });

/**
 * 构造 `oryn.use`：`endpoint` 是运行时端点的绝对地址，为 `null` 时（旧版本的帧、配置不全）任何能力都不可用。
 * 返回的函数永不拒绝：失败、超时、能力不可用都得到 `null`；可用时得到冻结的命名空间。
 */
export function createUse(
	endpoint: string | null,
	n: TransportNatives,
): (cap: string) => Promise<Capability | null> {
	const t = createTransport(endpoint, n);
	return async (cap: string) => {
		const info = await t.use(cap);
		if (!info) return null;
		const ns: Record<string, (...args: unknown[]) => unknown> = {};
		for (const op of info.ops) {
			if (info.streams.indexOf(op) < 0) {
				ns[op] = (...args: unknown[]) => t.call(cap, op, args);
				continue;
			}
			const table = Object.hasOwn(STREAM_ADAPTERS, cap)
				? STREAM_ADAPTERS[cap]
				: undefined;
			const adapt = table && Object.hasOwn(table, op) ? table[op] : undefined;
			if (adapt) ns[op] = adapt(t, n, cap, op);
		}
		return n.freeze(ns);
	};
}
