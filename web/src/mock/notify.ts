/**
 * mock 后端的通知部分（M5）：渠道、推送订阅、通知列表的内存实现，格式与 orynd 一致。
 * `MockServer.handleFetch` 开头先调 `handleNotifyFetch`，不归这里的请求返回 null。
 */
import type { Schemas } from "../api/client";
import { MAIN_THREAD, NOW } from "./fixtures";

type Channel = Schemas["NotificationChannel"];
type Notification = Schemas["Notification"];
type Sub = Schemas["PushSubscription"];
type Json = Record<string, unknown>;

const MIN = 60_000;

function json(body: unknown, status = 200): Response {
	return Response.json(body, { status });
}

function error(status: number, code: string, message: string): Response {
	return json({ code, message }, status);
}

let seq = 0;
function newId(prefix: string): string {
	seq++;
	return `${prefix}_01JD${String(seq).padStart(22, "0")}`;
}

/** 脱敏摘要：主机 + 末 4 位（与 orynd 的 `summary` 同形）。 */
function summaryOf(kind: Channel["kind"], cfg: Json): string {
	const tail = (s: unknown) => (typeof s === "string" ? s.slice(-4) : "");
	const host = (u: unknown, fallback: string) => {
		try {
			return new URL(String(u)).host;
		} catch {
			return fallback;
		}
	};
	if (kind === "telegram") return "@oryn_mock_bot";
	if (kind === "webhook")
		return `${host(cfg.url, "webhook")}/…${tail(cfg.url)}`;
	if (kind === "ntfy")
		return `${host(cfg.server ?? "https://ntfy.sh", "ntfy.sh")}/…${tail(cfg.topic)}`;
	return host(cfg.server ?? "https://api.day.app", "api.day.app");
}

interface State {
	channels: Channel[];
	subs: Sub[];
	notifications: Notification[];
	/** 绑定码发出后再被拉取几次就算绑定（模拟用户在 Telegram 里发了 /start） */
	linkPolls: Map<string, number>;
}

function seed(): State {
	const channels: Channel[] = [
		{
			id: "nch_01JD0000000000000000000001",
			kind: "telegram",
			name: "Telegram",
			enabled: true,
			events: ["done", "input", "error"],
			locale: "zh-CN",
			summary: "@oryn_mock_bot",
			telegram: {
				botUsername: "oryn_mock_bot",
				linked: true,
				chatTitle: "Lyn",
			},
			lastDelivery: { status: "sent", at: NOW - 4 * MIN },
			createdAt: NOW - 9 * 86_400_000,
			updatedAt: NOW - 9 * 86_400_000,
		},
		{
			id: "nch_01JD0000000000000000000002",
			kind: "webhook",
			name: "CI 钩子",
			enabled: true,
			events: ["done", "error"],
			locale: "en",
			summary: "hooks.example.com/…ab12",
			lastDelivery: { status: "sent", at: NOW - 4 * MIN },
			createdAt: NOW - 5 * 86_400_000,
			updatedAt: NOW - 5 * 86_400_000,
		},
		{
			id: "nch_01JD0000000000000000000003",
			kind: "ntfy",
			name: "手机 ntfy",
			enabled: true,
			events: ["input"],
			locale: "zh-CN",
			summary: "ntfy.sh/…9f3c",
			lastError: "HTTP 500: upstream unavailable",
			lastDelivery: {
				status: "pending",
				at: NOW + MIN,
				error: "HTTP 500: upstream unavailable",
			},
			createdAt: NOW - 2 * 86_400_000,
			updatedAt: NOW - 2 * 86_400_000,
		},
		{
			id: "nch_01JD0000000000000000000004",
			kind: "bark",
			name: "Bark",
			enabled: false,
			events: ["done", "input", "error"],
			locale: "zh-CN",
			summary: "api.day.app",
			createdAt: NOW - 86_400_000,
			updatedAt: NOW - 86_400_000,
		},
	];
	const subs: Sub[] = [
		{
			id: "psub_01JD000000000000000000001",
			kind: "webpush",
			endpoint: "https://fcm.googleapis.com/fcm/send/mock-laptop",
			userAgent:
				"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/141.0 Safari/537.36",
			createdAt: NOW - 6 * 86_400_000,
			lastSuccessAt: NOW - 4 * MIN,
		},
	];
	const notifications: Notification[] = [
		{
			id: "ntf_01JD0000000000000000000004",
			kind: "input",
			threadId: "T-0193a1b2-0000-7000-8000-00000000a004",
			title: "🐳 DOCKER: Docker provider 冻结后 orbd 重连",
			body: "允许执行 `docker exec orb-… pkill -HUP -x orbd`？",
			url: "/t/T-0193a1b2-0000-7000-8000-00000000a004",
			createdAt: NOW - 2 * MIN,
			deliveries: [
				{
					target: "telegram",
					channelId: channels[0]?.id,
					channelName: "Telegram",
					status: "sent",
					attempts: 1,
					at: NOW - 2 * MIN,
				},
				{
					target: "ntfy",
					channelId: channels[2]?.id,
					channelName: "手机 ntfy",
					status: "pending",
					attempts: 2,
					at: NOW + MIN,
					error: "HTTP 500: upstream unavailable",
				},
				{ target: "webpush", status: "sent", attempts: 1, at: NOW - 2 * MIN },
			],
		},
		{
			id: "ntf_01JD0000000000000000000003",
			kind: "done",
			threadId: MAIN_THREAD,
			title: "\u{F0668} WS: 重构 WebSocket 重连与 afterSeq 补拉",
			body: "重连后按 afterSeq 补拉，断线期间的条目无缺失无重复；测试已全部通过。",
			url: `/t/${MAIN_THREAD}`,
			createdAt: NOW - 4 * MIN,
			deliveries: [
				{
					target: "telegram",
					channelId: channels[0]?.id,
					channelName: "Telegram",
					status: "sent",
					attempts: 1,
					at: NOW - 4 * MIN,
				},
				{
					target: "webhook",
					channelId: channels[1]?.id,
					channelName: "CI 钩子",
					status: "sent",
					attempts: 1,
					at: NOW - 4 * MIN,
				},
			],
		},
		{
			id: "ntf_01JD0000000000000000000002",
			kind: "error",
			threadId: "T-0193a1b2-0000-7000-8000-00000000a005",
			title: "同步上游 can1357/oh-my-pi",
			body: "setup: exit status 1",
			url: "/t/T-0193a1b2-0000-7000-8000-00000000a005",
			createdAt: NOW - 3 * 3_600_000,
			readAt: NOW - 2 * 3_600_000,
			deliveries: [
				{
					target: "webhook",
					channelId: channels[1]?.id,
					channelName: "CI 钩子",
					status: "failed",
					attempts: 4,
					at: NOW - 2.9 * 3_600_000,
					error: 'Post "https://hooks.example.com/oryn": dial tcp: i/o timeout',
				},
			],
		},
		{
			id: "ntf_01JD0000000000000000000001",
			kind: "done",
			threadId: "T-0193a1b2-0000-7000-8000-00000000a002",
			title: "\u{F0453} RETRY: 共用 orb 试另一种退避策略",
			body: "指数退避上限改为 15s，抖动 ±20%。",
			url: "/t/T-0193a1b2-0000-7000-8000-00000000a002",
			createdAt: NOW - 26 * 3_600_000,
			readAt: NOW - 25 * 3_600_000,
			deliveries: [],
		},
	];
	return { channels, subs, notifications, linkPolls: new Map() };
}

let state = seed();

/** 测试用：恢复初始数据；`empty` = 不带任何渠道、订阅与通知。 */
export function resetNotifyMock({ empty = false } = {}) {
	state = empty
		? { channels: [], subs: [], notifications: [], linkPolls: new Map() }
		: seed();
}

const CHANNEL_KINDS = ["telegram", "webhook", "ntfy", "bark"] as const;

async function bodyOf(req: Request): Promise<Json> {
	try {
		return (await req.json()) as Json;
	} catch {
		return {};
	}
}

function listChannels(): Response {
	// 绑定向导轮询：发出绑定码后第 2 次拉取时视为已绑定
	for (const [id, n] of state.linkPolls) {
		if (n + 1 < 2) {
			state.linkPolls.set(id, n + 1);
			continue;
		}
		state.linkPolls.delete(id);
		const ch = state.channels.find((c) => c.id === id);
		if (ch?.telegram)
			ch.telegram = { ...ch.telegram, linked: true, chatTitle: "Lyn" };
	}
	return json({ channels: state.channels });
}

export async function handleNotifyFetch(
	req: Request,
): Promise<Response | null> {
	const url = new URL(req.url, location.origin);
	const p = url.pathname;
	const m = req.method.toUpperCase();
	if (p === "/api/v1/notifications" && m === "GET") {
		const limit = Number(url.searchParams.get("limit") ?? 50);
		return json({
			notifications: state.notifications.slice(0, limit),
			unread: state.notifications.filter((n) => !n.readAt).length,
		});
	}
	if (p === "/api/v1/notifications/read" && m === "POST") {
		const { threadId } = await bodyOf(req);
		for (const n of state.notifications)
			if (!n.readAt && (!threadId || n.threadId === threadId))
				n.readAt = Date.now();
		return new Response(null, { status: 204 });
	}
	if (p === "/api/v1/notification-channels") {
		if (m === "GET") return listChannels();
		if (m === "POST") {
			const b = await bodyOf(req);
			const kind = b.kind as Channel["kind"];
			if (!CHANNEL_KINDS.includes(kind) || typeof b.name !== "string")
				return error(400, "invalid_channel", "invalid channel");
			if (
				kind === "telegram" &&
				state.channels.some((c) => c.kind === "telegram")
			)
				return error(409, "channel_exists", "only one telegram channel");
			const cfg = (b[kind] ?? {}) as Json;
			const ch: Channel = {
				id: newId("nch"),
				kind,
				name: b.name,
				enabled: b.enabled !== false,
				events: (b.events as Channel["events"]) ?? ["done", "input", "error"],
				locale: (b.locale as Channel["locale"]) ?? "zh-CN",
				summary: summaryOf(kind, cfg),
				...(kind === "telegram"
					? { telegram: { botUsername: "oryn_mock_bot", linked: false } }
					: {}),
				createdAt: Date.now(),
				updatedAt: Date.now(),
			};
			state.channels.push(ch);
			return json(ch, 201);
		}
	}
	const chMatch =
		/^\/api\/v1\/notification-channels\/([^/]+)(\/test|\/telegram\/link)?$/.exec(
			p,
		);
	if (chMatch) {
		const ch = state.channels.find((c) => c.id === chMatch[1]);
		if (!ch) return error(404, "not_found", "channel not found");
		const sub = chMatch[2];
		if (!sub && m === "PATCH") {
			const b = await bodyOf(req);
			const cfg = b[ch.kind] as Json | undefined;
			Object.assign(ch, {
				...(typeof b.name === "string" ? { name: b.name } : {}),
				...(typeof b.enabled === "boolean" ? { enabled: b.enabled } : {}),
				...(Array.isArray(b.events) ? { events: b.events } : {}),
				...(typeof b.locale === "string" ? { locale: b.locale } : {}),
				...(cfg ? { summary: summaryOf(ch.kind, cfg) } : {}),
				updatedAt: Date.now(),
			});
			if (cfg && ch.telegram)
				ch.telegram = { ...ch.telegram, linked: false, chatTitle: undefined };
			return json(ch);
		}
		if (!sub && m === "DELETE") {
			state.channels = state.channels.filter((c) => c !== ch);
			return new Response(null, { status: 204 });
		}
		if (sub === "/test" && m === "POST") {
			if (ch.telegram && !ch.telegram.linked)
				return error(409, "channel_not_linked", "telegram chat not linked");
			if (ch.lastError) return json({ ok: false, error: ch.lastError });
			return json({ ok: true });
		}
		if (sub === "/telegram/link" && m === "POST") {
			if (ch.kind !== "telegram")
				return error(400, "invalid", "not a telegram channel");
			state.linkPolls.set(ch.id, 0);
			return json({
				code: "K7QM-W3XR",
				botUsername: ch.telegram?.botUsername ?? "oryn_mock_bot",
				expiresAt: Date.now() + 10 * MIN,
			});
		}
	}
	if (p === "/api/v1/push/vapid-key" && m === "GET")
		return json({
			publicKey:
				"BCVxsr7N_eNgVRqvHtD0zTZsEc6-VV-JvLexhqUzORcxaOzi6-AYWXvTBHm4bjyPjs7Vd8pZGH6SRpkNtoIAiw4",
		});
	if (p === "/api/v1/push/subscriptions") {
		if (m === "GET") return json({ subscriptions: state.subs });
		if (m === "POST") {
			const b = await bodyOf(req);
			const endpoint = String(b.endpoint ?? "");
			const existing = state.subs.find((s) => s.endpoint === endpoint);
			if (existing) return json(existing, 201);
			const sub: Sub = {
				id: newId("psub"),
				kind: "webpush",
				endpoint,
				userAgent: navigator.userAgent,
				createdAt: Date.now(),
			};
			state.subs.push(sub);
			return json(sub, 201);
		}
	}
	const subMatch = /^\/api\/v1\/push\/subscriptions\/([^/]+)$/.exec(p);
	if (subMatch && m === "DELETE") {
		const before = state.subs.length;
		state.subs = state.subs.filter((s) => s.id !== subMatch[1]);
		return state.subs.length === before
			? error(404, "not_found", "subscription not found")
			: new Response(null, { status: 204 });
	}
	return null;
}
