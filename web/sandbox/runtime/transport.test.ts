import { afterEach, describe, expect, it, vi } from "vitest";
import {
	createTransport,
	type TransportNatives,
	USE_TIMEOUT_MS,
} from "./transport";

const ENDPOINT = "https://a-token.example/_oryn/v1";

function testNatives(fetchImpl: typeof fetch): TransportNatives {
	return {
		fetch: fetchImpl,
		parse: JSON.parse,
		stringify: JSON.stringify,
		freeze: Object.freeze,
		setTimeout: (cb, ms) => window.setTimeout(cb, ms),
		clearTimeout: (h) => window.clearTimeout(h),
		AbortController,
		TextDecoder,
		Error,
	};
}

function reply(body: unknown, status = 200): Response {
	return new Response(JSON.stringify(body), {
		status,
		headers: { "Content-Type": "application/json" },
	});
}

/** 一个 NDJSON 应答，按给定的分块原样送出（分块可以切在行中间）。 */
function chunked(chunks: string[]): Response {
	const enc = new TextEncoder();
	return new Response(
		new ReadableStream<Uint8Array>({
			start(c) {
				for (const s of chunks) c.enqueue(enc.encode(s));
				c.close();
			},
		}),
		{ headers: { "Content-Type": "application/x-ndjson" } },
	);
}

function sentBody(fetchMock: ReturnType<typeof vi.fn>, call = 0) {
	const init = fetchMock.mock.calls[call]?.[1] as RequestInit;
	return JSON.parse(String(init.body));
}

afterEach(() => {
	vi.useRealTimers();
});

describe("transport.use", () => {
	it("回能力描述：只留合法的操作名，streams 是 ops 的子集", async () => {
		const fetchMock = vi.fn().mockResolvedValue(
			reply({
				v: 1,
				id: "x",
				ok: true,
				value: { ops: ["get", "watch", "Bad op", 3], streams: ["watch", "x"] },
			}),
		);
		const t = createTransport(ENDPOINT, testNatives(fetchMock));
		await expect(t.use("db")).resolves.toEqual({
			ops: ["get", "watch"],
			streams: ["watch"],
		});
		expect(fetchMock.mock.calls[0]?.[0]).toBe(ENDPOINT);
		expect(sentBody(fetchMock)).toMatchObject({ v: 1, kind: "use", cap: "db" });
	});

	it("请求失败、应答不是 JSON、ok 为假或 value 为 null 时返回 null", async () => {
		const cases = [
			vi.fn().mockRejectedValue(new TypeError("network")),
			vi.fn().mockResolvedValue(new Response("<html>", { status: 502 })),
			vi
				.fn()
				.mockResolvedValue(
					reply(
						{
							v: 1,
							id: "",
							ok: false,
							error: { code: "invalid", message: "x" },
						},
						400,
					),
				),
			vi
				.fn()
				.mockResolvedValue(reply({ v: 1, id: "x", ok: true, value: null })),
		];
		for (const f of cases)
			await expect(
				createTransport(ENDPOINT, testNatives(f)).use("content"),
			).resolves.toBeNull();
	});

	it("超时返回 null 并中止请求", async () => {
		vi.useFakeTimers();
		let signal: AbortSignal | undefined;
		const hanging = vi.fn((_url: string, init: RequestInit) => {
			signal = init.signal ?? undefined;
			return new Promise<Response>((_, reject) => {
				signal?.addEventListener("abort", () =>
					reject(new DOMException("aborted", "AbortError")),
				);
			});
		});
		const result = createTransport(
			ENDPOINT,
			testNatives(hanging as unknown as typeof fetch),
		).use("content");
		await vi.advanceTimersByTimeAsync(USE_TIMEOUT_MS);
		await expect(result).resolves.toBeNull();
		expect(signal?.aborted).toBe(true);
	});

	it("没有端点（旧版本的帧）或能力名不合法时不发请求", async () => {
		const fetchMock = vi.fn();
		await expect(
			createTransport(null, testNatives(fetchMock)).use("content"),
		).resolves.toBeNull();
		await expect(
			createTransport(ENDPOINT, testNatives(fetchMock)).use("Bad Name"),
		).resolves.toBeNull();
		expect(fetchMock).not.toHaveBeenCalled();
	});
});

describe("transport.stream", () => {
	it("逐行交付事件（行可以跨分块），done 时兑现", async () => {
		const fetchMock = vi
			.fn()
			.mockResolvedValue(
				chunked([
					'{"v":1,"id":"a","event":{"n":1}}\n{"v":1,"id":"a","ev',
					'ent":{"n":2}}\n',
					'{"v":1,"id":"a","done":true}\n',
				]),
			);
		const events: unknown[] = [];
		const t = createTransport(ENDPOINT, testNatives(fetchMock));
		await t.stream(
			"db",
			"watch",
			[["todos"]],
			(e) => events.push(e),
			new AbortController().signal,
		);
		expect(events).toEqual([{ n: 1 }, { n: 2 }]);
		expect(sentBody(fetchMock)).toMatchObject({
			v: 1,
			kind: "stream",
			cap: "db",
			op: "watch",
			args: [["todos"]],
		});
	});

	it("末行是错误时抛出带 code 的错误", async () => {
		const fetchMock = vi
			.fn()
			.mockResolvedValue(
				chunked([
					'{"v":1,"id":"a","event":1}\n',
					'{"v":1,"id":"a","ok":false,"error":{"code":"failed","message":"跟不上"}}',
				]),
			);
		const events: unknown[] = [];
		await expect(
			createTransport(ENDPOINT, testNatives(fetchMock)).stream(
				"db",
				"watch",
				[],
				(e) => events.push(e),
				new AbortController().signal,
			),
		).rejects.toMatchObject({
			name: "OrynError",
			code: "failed",
			message: "跟不上",
		});
		expect(events).toEqual([1]);
	});

	it("没有 done 就结束、请求失败都算 failed", async () => {
		for (const f of [
			vi.fn().mockResolvedValue(chunked(['{"v":1,"id":"a","event":1}\n'])),
			vi.fn().mockRejectedValue(new TypeError("network")),
		])
			await expect(
				createTransport(ENDPOINT, testNatives(f)).stream(
					"db",
					"watch",
					[],
					() => {},
					new AbortController().signal,
				),
			).rejects.toMatchObject({ code: "failed" });
	});
});
