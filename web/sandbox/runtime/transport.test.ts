import { afterEach, describe, expect, it, vi } from "vitest";
import { createUse, type TransportNatives, USE_TIMEOUT_MS } from "./transport";

const ENDPOINT = "https://a-token.example/_oryn/v1";

function natives(fetchImpl: typeof fetch): TransportNatives {
	return {
		fetch: fetchImpl,
		parse: JSON.parse,
		stringify: JSON.stringify,
		freeze: Object.freeze,
		setTimeout: (cb, ms) => window.setTimeout(cb, ms),
		clearTimeout: (h) => window.clearTimeout(h),
		AbortController,
		Error,
	};
}

function reply(body: unknown, status = 200): Response {
	return new Response(JSON.stringify(body), {
		status,
		headers: { "Content-Type": "application/json" },
	});
}

function sentBody(fetchMock: ReturnType<typeof vi.fn>, call = 0) {
	const init = fetchMock.mock.calls[call]?.[1] as RequestInit;
	return JSON.parse(String(init.body));
}

afterEach(() => {
	vi.useRealTimers();
});

describe("oryn.use", () => {
	it("可用时返回冻结的能力对象，操作按位置参数发 call", async () => {
		const fetchMock = vi
			.fn()
			.mockResolvedValueOnce(
				reply({ v: 1, id: "x", ok: true, value: { ops: ["read", "write"] } }),
			)
			.mockResolvedValueOnce(
				reply({ v: 1, id: "y", ok: true, value: { version: 4 } }),
			);
		const use = createUse(ENDPOINT, natives(fetchMock));
		const cap = await use("content");
		expect(cap).not.toBeNull();
		expect(Object.isFrozen(cap)).toBe(true);
		expect(Object.keys(cap ?? {})).toEqual(["read", "write"]);
		expect(fetchMock.mock.calls[0]?.[0]).toBe(ENDPOINT);
		expect(sentBody(fetchMock, 0)).toMatchObject({
			v: 1,
			kind: "use",
			cap: "content",
		});

		await expect(cap?.write?.({ baseVersion: 3, text: "x" })).resolves.toEqual({
			version: 4,
		});
		const call = sentBody(fetchMock, 1);
		expect(call).toMatchObject({
			v: 1,
			kind: "call",
			cap: "content",
			op: "write",
			args: [{ baseVersion: 3, text: "x" }],
		});
		expect(call.id).not.toBe(sentBody(fetchMock, 0).id);
	});

	it("调用失败时抛出带 code 的错误", async () => {
		const fetchMock = vi
			.fn()
			.mockResolvedValueOnce(
				reply({ v: 1, id: "x", ok: true, value: { ops: ["write"] } }),
			)
			.mockResolvedValueOnce(
				reply({
					v: 1,
					id: "y",
					ok: false,
					error: { code: "conflict", message: "已有新版本" },
				}),
			);
		const cap = await createUse(ENDPOINT, natives(fetchMock))("content");
		await expect(cap?.write?.({})).rejects.toMatchObject({
			code: "conflict",
			message: "已有新版本",
		});
	});

	it("服务端拒绝（value 为 null）返回 null", async () => {
		const fetchMock = vi
			.fn()
			.mockResolvedValue(reply({ v: 1, id: "x", ok: true, value: null }));
		await expect(
			createUse(ENDPOINT, natives(fetchMock))("db"),
		).resolves.toBeNull();
	});

	it("请求失败、应答不是 JSON 或 ok 为假时返回 null", async () => {
		const failing = vi.fn().mockRejectedValue(new TypeError("network"));
		await expect(
			createUse(ENDPOINT, natives(failing))("content"),
		).resolves.toBeNull();

		const garbage = vi
			.fn()
			.mockResolvedValue(new Response("<html>", { status: 502 }));
		await expect(
			createUse(ENDPOINT, natives(garbage))("content"),
		).resolves.toBeNull();

		const refused = vi
			.fn()
			.mockResolvedValue(
				reply(
					{ v: 1, id: "", ok: false, error: { code: "invalid", message: "x" } },
					400,
				),
			);
		await expect(
			createUse(ENDPOINT, natives(refused))("content"),
		).resolves.toBeNull();
	});

	it("超时返回 null 并中止请求", async () => {
		vi.useFakeTimers();
		let signal: AbortSignal | undefined;
		const hanging = vi.fn((_url: string, init: RequestInit) => {
			signal = init.signal ?? undefined;
			// tsconfig 的 lib 是 ES2023，没有 Promise.withResolvers
			return new Promise<Response>((_, reject) => {
				signal?.addEventListener("abort", () =>
					reject(new DOMException("aborted", "AbortError")),
				);
			});
		});
		const result = createUse(
			ENDPOINT,
			natives(hanging as unknown as typeof fetch),
		)("content");
		await vi.advanceTimersByTimeAsync(USE_TIMEOUT_MS);
		await expect(result).resolves.toBeNull();
		expect(signal?.aborted).toBe(true);
	});

	it("没有端点（旧版本的帧）或能力名不合法时不发请求", async () => {
		const fetchMock = vi.fn();
		await expect(
			createUse(null, natives(fetchMock))("content"),
		).resolves.toBeNull();
		await expect(
			createUse(ENDPOINT, natives(fetchMock))("Bad Name"),
		).resolves.toBeNull();
		expect(fetchMock).not.toHaveBeenCalled();
	});
});
