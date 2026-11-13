import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
	base64UrlToBytes,
	pushSupported,
	subscribePush,
	unsubscribePush,
} from "./push";

// RFC 8291 附录 A 的接收方公钥（65 字节未压缩点，0x04 开头）
const UA_PUBLIC =
	"BCVxsr7N_eNgVRqvHtD0zTZsEc6-VV-JvLexhqUzORcxaOzi6-AYWXvTBHm4bjyPjs7Vd8pZGH6SRpkNtoIAiw4";

describe("base64UrlToBytes", () => {
	it("VAPID 公钥 → 65 字节、以 0x04 开头", () => {
		const b = base64UrlToBytes(UA_PUBLIC);
		expect(b).toBeInstanceOf(Uint8Array);
		expect(b.length).toBe(65);
		expect(b[0]).toBe(0x04);
	});

	it("补齐填充并换回标准字母表", () => {
		expect([...base64UrlToBytes("-_8")]).toEqual([0xfb, 0xff]);
		expect([...base64UrlToBytes("AQ")]).toEqual([1]);
	});
});

interface FakeSub {
	endpoint: string;
	toJSON(): PushSubscriptionJSON;
	unsubscribe: ReturnType<typeof vi.fn>;
}

describe("订阅与退订", () => {
	let sub: FakeSub | null;
	let subscribeArgs: PushSubscriptionOptionsInit[];
	let requests: { method: string; path: string; body: unknown }[];

	beforeEach(() => {
		sub = null;
		subscribeArgs = [];
		requests = [];
		const pushManager = {
			getSubscription: async () => sub,
			subscribe: async (opts: PushSubscriptionOptionsInit) => {
				subscribeArgs.push(opts);
				sub = {
					endpoint: "https://push.example/ep1",
					toJSON: () => ({
						endpoint: "https://push.example/ep1",
						keys: { p256dh: UA_PUBLIC, auth: "BTBZMqHH6r4Tts7J_aSIgg" },
					}),
					unsubscribe: vi.fn(async () => true),
				};
				return sub;
			},
		};
		const reg = { pushManager };
		vi.stubGlobal("isSecureContext", true);
		vi.stubGlobal("PushManager", class {});
		vi.stubGlobal("Notification", class {});
		Object.defineProperty(navigator, "serviceWorker", {
			configurable: true,
			value: {
				getRegistration: async () => reg,
				register: async () => reg,
				ready: Promise.resolve(reg),
			},
		});
		vi.stubGlobal(
			"fetch",
			vi.fn(async (req: Request) => {
				const url = new URL(req.url);
				const body = req.method === "GET" ? undefined : await req.text();
				requests.push({
					method: req.method,
					path: url.pathname,
					body: body ? JSON.parse(body) : undefined,
				});
				if (url.pathname === "/api/v1/push/vapid-key")
					return Response.json({ publicKey: UA_PUBLIC });
				if (
					url.pathname === "/api/v1/push/subscriptions" &&
					req.method === "POST"
				)
					return Response.json(
						{
							id: "psub_1",
							kind: "webpush",
							endpoint: "https://push.example/ep1",
							userAgent: "ua",
							createdAt: 1,
						},
						{ status: 201 },
					);
				if (url.pathname === "/api/v1/push/subscriptions")
					return Response.json({
						subscriptions: [
							{ id: "psub_0", endpoint: "https://push.example/other" },
							{ id: "psub_1", endpoint: "https://push.example/ep1" },
						],
					});
				return new Response(null, { status: 204 });
			}),
		);
	});

	afterEach(() => {
		vi.unstubAllGlobals();
		Reflect.deleteProperty(navigator, "serviceWorker");
	});

	it("pushSupported 需要安全上下文、service worker、PushManager 与 Notification", () => {
		expect(pushSupported()).toBe(true);
		vi.stubGlobal("isSecureContext", false);
		expect(pushSupported()).toBe(false);
	});

	it("subscribePush：取 VAPID 公钥订阅，再把 endpoint 与密钥 POST 给服务端", async () => {
		const created = await subscribePush();
		expect(created.id).toBe("psub_1");
		expect(subscribeArgs).toHaveLength(1);
		expect(subscribeArgs[0]?.userVisibleOnly).toBe(true);
		const key = subscribeArgs[0]?.applicationServerKey;
		expect(key).toBeInstanceOf(Uint8Array);
		expect([...(key as Uint8Array)]).toEqual([...base64UrlToBytes(UA_PUBLIC)]);
		expect(requests.at(-1)).toEqual({
			method: "POST",
			path: "/api/v1/push/subscriptions",
			body: {
				endpoint: "https://push.example/ep1",
				keys: { p256dh: UA_PUBLIC, auth: "BTBZMqHH6r4Tts7J_aSIgg" },
			},
		});
	});

	it("unsubscribePush：按 endpoint 找到自己的登记删除，再让浏览器退订", async () => {
		await subscribePush();
		const own = sub;
		requests = [];
		await unsubscribePush();
		expect(requests.map((r) => `${r.method} ${r.path}`)).toEqual([
			"GET /api/v1/push/subscriptions",
			"DELETE /api/v1/push/subscriptions/psub_1",
		]);
		expect(own?.unsubscribe).toHaveBeenCalled();
	});

	it("没有订阅时退订无操作", async () => {
		await unsubscribePush();
		expect(requests).toEqual([]);
	});
});
