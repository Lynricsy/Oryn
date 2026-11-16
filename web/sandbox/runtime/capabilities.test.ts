import { describe, expect, it, vi } from "vitest";
import { type CapabilityNatives, createUse } from "./capabilities";

const ENDPOINT = "https://a-token.example/_oryn/v1";

function testNatives(fetchImpl: unknown): CapabilityNatives {
	return {
		fetch: fetchImpl as typeof fetch,
		parse: JSON.parse,
		stringify: JSON.stringify,
		freeze: Object.freeze,
		setTimeout: (cb, ms) => window.setTimeout(cb, ms),
		clearTimeout: (h) => window.clearTimeout(h),
		AbortController,
		TextDecoder,
		Error,
		atob: (s) => window.atob(s),
		Uint8Array,
	};
}

function reply(body: unknown): Response {
	return new Response(JSON.stringify(body), {
		headers: { "Content-Type": "application/json" },
	});
}

const DB_OPS = {
	v: 1,
	id: "u",
	ok: true,
	value: { ops: ["get", "set", "watch"], streams: ["watch"] },
};

/** 一条可以从测试里逐行推送的 NDJSON 应答；记下请求的 signal。 */
function liveStream() {
	const enc = new TextEncoder();
	let ctl!: ReadableStreamDefaultController<Uint8Array>;
	const body = new ReadableStream<Uint8Array>({
		start(c) {
			ctl = c;
		},
	});
	let signal: AbortSignal | undefined;
	const respond = (init: RequestInit) => {
		signal = init.signal ?? undefined;
		signal?.addEventListener("abort", () =>
			ctl.error(new DOMException("aborted", "AbortError")),
		);
		return new Response(body, {
			headers: { "Content-Type": "application/x-ndjson" },
		});
	};
	return {
		respond,
		push: (line: unknown) =>
			ctl.enqueue(enc.encode(`${JSON.stringify(line)}\n`)),
		close: () => ctl.close(),
		signal: () => signal,
	};
}

function sentBody(fetchMock: ReturnType<typeof vi.fn>, call: number) {
	const init = fetchMock.mock.calls[call]?.[1] as RequestInit;
	return JSON.parse(String(init.body));
}

describe("oryn.use 的命名空间", () => {
	it("可用时返回冻结的对象，普通操作按位置参数发 call", async () => {
		const fetchMock = vi
			.fn()
			.mockResolvedValueOnce(reply(DB_OPS))
			.mockResolvedValueOnce(
				reply({ v: 1, id: "c", ok: true, value: { id: "t1", version: 4 } }),
			);
		const db = await createUse(ENDPOINT, testNatives(fetchMock))("db");
		expect(db).not.toBeNull();
		expect(Object.isFrozen(db)).toBe(true);
		expect(Object.keys(db ?? {}).sort()).toEqual(["get", "set", "watch"]);
		await expect(db?.set?.("todos", "t1", { text: "a" })).resolves.toEqual({
			id: "t1",
			version: 4,
		});
		const call = sentBody(fetchMock, 1);
		expect(call).toMatchObject({
			v: 1,
			kind: "call",
			cap: "db",
			op: "set",
			args: ["todos", "t1", { text: "a" }],
		});
		expect(call.id).not.toBe(sentBody(fetchMock, 0).id);
	});

	it("ok 为假时抛出带 code 的错误；请求失败抛 failed", async () => {
		const fetchMock = vi
			.fn()
			.mockResolvedValueOnce(reply(DB_OPS))
			.mockResolvedValueOnce(
				reply({
					v: 1,
					id: "c",
					ok: false,
					error: { code: "conflict", message: "已有新版本" },
				}),
			)
			.mockRejectedValueOnce(new TypeError("network"));
		const db = await createUse(ENDPOINT, testNatives(fetchMock))("db");
		await expect(db?.get?.("todos", "t1")).rejects.toMatchObject({
			name: "OrynError",
			code: "conflict",
			message: "已有新版本",
		});
		await expect(db?.get?.("todos", "t1")).rejects.toMatchObject({
			code: "failed",
		});
	});

	it("use 回 null 时不构造命名空间，也不再发请求", async () => {
		const fetchMock = vi
			.fn()
			.mockResolvedValue(reply({ v: 1, id: "u", ok: true, value: null }));
		await expect(
			createUse(ENDPOINT, testNatives(fetchMock))("files"),
		).resolves.toBeNull();
		await expect(
			createUse(null, testNatives(fetchMock))("db"),
		).resolves.toBeNull();
		expect(fetchMock).toHaveBeenCalledTimes(1);
	});

	it("没有适配的流式操作不暴露", async () => {
		const fetchMock = vi.fn().mockResolvedValue(
			reply({
				v: 1,
				id: "u",
				ok: true,
				value: {
					ops: ["ask", "constructor"],
					streams: ["ask", "constructor"],
				},
			}),
		);
		const cap = await createUse(ENDPOINT, testNatives(fetchMock))("db");
		expect(cap).toEqual({});
	});

	it('files.read 带 {as: "bytes"} 时把 base64 转成 Uint8Array，按文本读时原样返回', async () => {
		const fetchMock = vi
			.fn()
			.mockResolvedValueOnce(
				reply({ v: 1, id: "u", ok: true, value: { ops: ["list", "read"] } }),
			)
			.mockResolvedValueOnce(reply({ v: 1, id: "c", ok: true, value: "AP8Q" }))
			.mockResolvedValueOnce(reply({ v: 1, id: "c", ok: true, value: "AP8Q" }))
			.mockResolvedValueOnce(reply({ v: 1, id: "c", ok: true, value: [] }));
		const files = await createUse(ENDPOINT, testNatives(fetchMock))("files");
		const bytes = await files?.read?.("bin.dat", { as: "bytes" });
		expect(bytes).toBeInstanceOf(Uint8Array);
		expect(bytes).toEqual(new Uint8Array([0x00, 0xff, 0x10]));
		expect(sentBody(fetchMock, 1)).toMatchObject({
			cap: "files",
			op: "read",
			args: ["bin.dat", { as: "bytes" }],
		});
		await expect(files?.read?.("a.txt")).resolves.toBe("AP8Q");
		await expect(files?.list?.()).resolves.toEqual([]);
	});
});

describe("db.watch", () => {
	it("逐条交付变化，done 时正常结束且不调 onError", async () => {
		const live = liveStream();
		const fetchMock = vi
			.fn()
			.mockResolvedValueOnce(reply(DB_OPS))
			.mockImplementationOnce((_u: string, init: RequestInit) =>
				live.respond(init),
			);
		const db = await createUse(ENDPOINT, testNatives(fetchMock))("db");
		const changes: unknown[] = [];
		const onError = vi.fn();
		const stop = db?.watch?.(
			["todos"],
			(c: unknown) => changes.push(c),
			onError,
		);
		expect(typeof stop).toBe("function");
		await vi.waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(2));
		expect(sentBody(fetchMock, 1)).toMatchObject({
			kind: "stream",
			cap: "db",
			op: "watch",
			args: [["todos"]],
		});
		const set = {
			type: "set",
			collection: "todos",
			id: "t1",
			value: { a: 1 },
			version: 3,
		};
		live.push({ v: 1, id: "w", event: set });
		live.push({
			v: 1,
			id: "w",
			event: { type: "delete", collection: "todos", id: "t1" },
		});
		await vi.waitFor(() => expect(changes).toHaveLength(2));
		expect(changes[0]).toEqual(set);
		live.push({ v: 1, id: "w", done: true });
		live.close();
		await Promise.resolve();
		expect(onError).not.toHaveBeenCalled();
	});

	it("流以 failed 结束时把带 code 的错误交给 onError；页面回调抛错不影响监听", async () => {
		const live = liveStream();
		const fetchMock = vi
			.fn()
			.mockResolvedValueOnce(reply(DB_OPS))
			.mockImplementationOnce((_u: string, init: RequestInit) =>
				live.respond(init),
			);
		const db = await createUse(ENDPOINT, testNatives(fetchMock))("db");
		const onChange = vi.fn(() => {
			throw new Error("page bug");
		});
		const onError = vi.fn();
		db?.watch?.(null, onChange, onError);
		await vi.waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(2));
		expect(sentBody(fetchMock, 1).args).toEqual([null]);
		live.push({
			v: 1,
			id: "w",
			event: { type: "delete", collection: "c", id: "1" },
		});
		live.push({
			v: 1,
			id: "w",
			ok: false,
			error: { code: "failed", message: "监听跟不上" },
		});
		await vi.waitFor(() => expect(onError).toHaveBeenCalledTimes(1));
		expect(onChange).toHaveBeenCalledTimes(1);
		expect(onError.mock.calls[0]?.[0]).toMatchObject({
			code: "failed",
			message: "监听跟不上",
		});
	});

	it("取消函数中止请求，之后不再回调", async () => {
		const live = liveStream();
		const fetchMock = vi
			.fn()
			.mockResolvedValueOnce(reply(DB_OPS))
			.mockImplementationOnce((_u: string, init: RequestInit) =>
				live.respond(init),
			);
		const db = await createUse(ENDPOINT, testNatives(fetchMock))("db");
		const onChange = vi.fn();
		const onError = vi.fn();
		// 省略 collections：第一个参数直接是回调
		const stop = db?.watch?.(onChange, onError) as () => void;
		await vi.waitFor(() => expect(live.signal()).toBeDefined());
		expect(sentBody(fetchMock, 1).args).toEqual([null]);
		stop();
		expect(live.signal()?.aborted).toBe(true);
		await new Promise((r) => queueMicrotask(() => r(null)));
		expect(onChange).not.toHaveBeenCalled();
		expect(onError).not.toHaveBeenCalled();
	});

	it("没有 onChange 时同步抛错", async () => {
		const fetchMock = vi.fn().mockResolvedValueOnce(reply(DB_OPS));
		const db = await createUse(ENDPOINT, testNatives(fetchMock))("db");
		expect(() => db?.watch?.(["todos"])).toThrow();
		expect(fetchMock).toHaveBeenCalledTimes(1);
	});
});

const SAMPLE_OPS = {
	v: 1,
	id: "u",
	ok: true,
	value: { ops: ["ask"], streams: ["ask"] },
};

describe("sample.ask", () => {
	it("增量交给 onText，answer 作为结果；只把 format 与 images 发给服务端", async () => {
		const live = liveStream();
		const fetchMock = vi
			.fn()
			.mockResolvedValueOnce(reply(SAMPLE_OPS))
			.mockImplementationOnce((_u: string, init: RequestInit) =>
				live.respond(init),
			);
		const sample = await createUse(ENDPOINT, testNatives(fetchMock))("sample");
		const parts: string[] = [];
		const pending = sample?.ask?.("总结一下", {
			format: "json",
			images: ["data:image/png;base64,AA=="],
			onText: (s: string) => {
				parts.push(s);
				throw new Error("page bug");
			},
			signal: new AbortController().signal,
		}) as Promise<unknown>;
		await vi.waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(2));
		expect(sentBody(fetchMock, 1)).toMatchObject({
			kind: "stream",
			cap: "sample",
			op: "ask",
			args: [
				"总结一下",
				{ format: "json", images: ["data:image/png;base64,AA=="] },
			],
		});
		live.push({ v: 1, id: "s", event: { type: "text", text: '{"a"' } });
		live.push({ v: 1, id: "s", event: { type: "text", text: ":1}" } });
		live.push({
			v: 1,
			id: "s",
			event: { type: "answer", text: '{"a":1}', json: { a: 1 } },
		});
		live.push({ v: 1, id: "s", done: true });
		live.close();
		const answer = await pending;
		expect(answer).toEqual({ text: '{"a":1}', json: { a: 1 } });
		expect(Object.isFrozen(answer)).toBe(true);
		expect(parts).toEqual(['{"a"', ":1}"]);
	});

	it("没有 json 时结果只有 text；流以错误结束时拒绝并带 code", async () => {
		const ok = liveStream();
		const bad = liveStream();
		const fetchMock = vi
			.fn()
			.mockResolvedValueOnce(reply(SAMPLE_OPS))
			.mockImplementationOnce((_u: string, init: RequestInit) =>
				ok.respond(init),
			)
			.mockImplementationOnce((_u: string, init: RequestInit) =>
				bad.respond(init),
			);
		const sample = await createUse(ENDPOINT, testNatives(fetchMock))("sample");
		const first = sample?.ask?.("hi") as Promise<unknown>;
		await vi.waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(2));
		expect(sentBody(fetchMock, 1).args).toEqual(["hi", {}]);
		ok.push({ v: 1, id: "s", event: { type: "answer", text: "你好" } });
		ok.push({ v: 1, id: "s", done: true });
		ok.close();
		await expect(first).resolves.toEqual({ text: "你好" });

		const second = sample?.ask?.("hi") as Promise<unknown>;
		await vi.waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(3));
		bad.push({
			v: 1,
			id: "s",
			ok: false,
			error: { code: "rate_limited", message: "太多了" },
		});
		bad.close();
		await expect(second).rejects.toMatchObject({ code: "rate_limited" });
	});

	it("signal 中止时中止请求并以 reason 拒绝；已中止的 signal 不发请求", async () => {
		const live = liveStream();
		const fetchMock = vi
			.fn()
			.mockResolvedValueOnce(reply(SAMPLE_OPS))
			.mockImplementationOnce((_u: string, init: RequestInit) =>
				live.respond(init),
			);
		const sample = await createUse(ENDPOINT, testNatives(fetchMock))("sample");
		const ctl = new AbortController();
		const onText = vi.fn();
		const pending = sample?.ask?.("hi", {
			onText,
			signal: ctl.signal,
		}) as Promise<unknown>;
		await vi.waitFor(() => expect(live.signal()).toBeDefined());
		ctl.abort();
		expect(live.signal()?.aborted).toBe(true);
		await expect(pending).rejects.toMatchObject({ name: "AbortError" });
		expect(onText).not.toHaveBeenCalled();

		await expect(
			sample?.ask?.("hi", { signal: ctl.signal }) as Promise<unknown>,
		).rejects.toMatchObject({ name: "AbortError" });
		await expect(
			sample?.ask?.("hi", { onText: 1 }) as Promise<unknown>,
		).rejects.toThrow();
		expect(fetchMock).toHaveBeenCalledTimes(2);
	});
});
