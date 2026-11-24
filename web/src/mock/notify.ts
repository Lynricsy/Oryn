/**
 * mock 后端的通知部分（M5）：渠道、推送订阅、通知列表的内存实现，格式与 orynd 一致。
 * `MockServer.handleFetch` 开头先调 `handleNotifyFetch`，不归这里的请求返回 null。
 */
import type { Schemas } from "../api/client";
import { M9_IDS, MAIN_THREAD, NOW } from "./fixtures";

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
			events: ["done", "input", "error", "budget"],
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
		{
			id: "psub_01JD000000000000000000002",
			kind: "gateway",
			endpoint: "https://push.oryn.best/v1/grants/Q2xhdWRlT3J5bk1vY2tHcg",
			userAgent: "OrynMobile/0.9.3 (ios)",
			createdAt: NOW - 2 * 86_400_000,
			lastSuccessAt: NOW - 40 * MIN,
		},
	];
	const notifications: Notification[] = [
		{
			id: "ntf_01JD0000000000000000000005",
			kind: "input",
			threadId: M9_IDS.spaceNeedsYou,
			title: "📌 FOLD: 侧栏按定时折叠运行线程",
			body: "同一定时任务的历次运行，侧栏里默认怎么显示？",
			url: `/t/${M9_IDS.spaceNeedsYou}`,
			createdAt: NOW - MIN,
			deliveries: [],
			quickAnswer: {
				question: "同一定时任务的历次运行，侧栏里默认怎么显示？",
				options: [
					{ i: 0, label: "折叠成一行" },
					{ i: 1, label: "全部展开" },
					{ i: 2, label: "只留最近一次" },
				],
			},
		},
		{
			id: "ntf_01JD0000000000000000000004",
			kind: "input",
			threadId: "T-0193a1b2-0000-7000-8000-00000000a004",
			title: "🐳 DOCKER: Docker provider 冻结后 orbd 重连",
			body: "允许执行 `docker exec orb-… pkill -HUP -x orbd`？",
			url: "/t/T-0193a1b2-0000-7000-8000-00000000a004",
			createdAt: NOW - 2 * MIN,
			quickAnswer: {
				question:
					"允许执行 `docker exec orb-7f3c pkill -HUP -x orbd`？\n让冻结后恢复的容器里的 orbd 重新连上控制面。",
				options: [
					{ i: 0, label: "允许" },
					{ i: 1, label: "拒绝" },
				],
			},
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
			// 预算告警（M10）：系统通知，没有线程；标题与正文是服务端的英文固定短语
			id: "ntf_01JD0000000000000000000005",
			kind: "budget",
			title: "Daily budget warning",
			body: "$8.16 of $10.00 spent, past the 80% warning threshold (2026-11-15, Asia/Shanghai).",
			url: "/stats/usage",
			createdAt: NOW - 40 * MIN,
			deliveries: [
				{
					target: "telegram",
					channelId: channels[0]?.id,
					channelName: "Telegram",
					status: "sent",
					attempts: 1,
					at: NOW - 40 * MIN,
				},
				{ target: "webpush", status: "sent", attempts: 1, at: NOW - 40 * MIN },
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
		...olderNotifications(),
	];
	return { channels, subs, notifications, linkPolls: new Map() };
}

/**
 * 更早的已读通知：让通知列表超过一屏（铃铛弹层与收件箱要能看到滚动、长标题截断与各 kind 的图标）。
 * 都在一天以前且已读，不影响未读计数与「需要你」的提问。
 */
function olderNotifications(): Notification[] {
	const rows: [Notification["kind"], string, string][] = [
		[
			"done",
			"补 M9 截图矩阵",
			"1920 / 1366 / 390 三档 × 明暗，共 214 张截图已更新。",
		],
		[
			"error",
			"接入 Postgres 测试数据库",
			"setup: pg_isready 超时（30s），容器未就绪。",
		],
		[
			"done",
			"把订阅表换成引用计数，并在 onopen 时对每个仍被订阅的线程重发 sub{afterSeq}，顺带清理断线期间的在途命令",
			"改动 6 个文件，新增 3 个单测；端到端重连用例 12/12 通过。",
		],
		["input", "修复登录重定向", "回调地址要不要带上原始查询串？"],
		["done", "回合头按来源记录显示 role", "已按来源记录渲染 role 胶囊。"],
		["budget", "Weekly budget warning", "$41.20 of $50.00 spent this week."],
		[
			"done",
			"补全 CLI 的 shell 补全脚本",
			"bash / zsh / fish 三种补全脚本已生成并加了安装说明。",
		],
		[
			"error",
			"比价并下单测试物料",
			"浏览器 Profile 被另一台 orb 占用，无法登录商城。",
		],
		[
			"done",
			"每天早上同步上游并跑测试",
			"上游 37 个提交已合并，测试全部通过。",
		],
		["done", "博客换成静态站点生成器", "已迁移 48 篇文章，构建产物 3.1 MB。"],
		[
			"error",
			"在自己机器上排查登录页闪烁",
			"runner 离线超过 10 分钟，回合已中止。",
		],
		["done", "旧 Core 上的线程（没有来源记录）", "回合已完成"],
	];
	return rows.map(([kind, title, body], i) => ({
		id: `ntf_01JD00000000000000000001${String(i).padStart(2, "0")}`,
		kind,
		title,
		body,
		url: kind === "budget" ? "/stats/usage" : `/t/${MAIN_THREAD}`,
		...(kind === "budget" ? {} : { threadId: MAIN_THREAD }),
		createdAt: NOW - (30 + i * 9) * 3_600_000,
		readAt: NOW - (29 + i * 9) * 3_600_000,
		...(kind === "input" ? { resolvedAt: NOW - (29 + i * 9) * 3_600_000 } : {}),
		deliveries: [],
	}));
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

/** 通知按钮作答成功后由 MockServer 推进线程状态（等你处理 → 工作中）。 */
export interface NotifyHooks {
	answered(threadId: string): void;
}

export async function handleNotifyFetch(
	req: Request,
	hooks?: NotifyHooks,
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
	const answer = p.match(/^\/api\/v1\/notifications\/([^/]+)\/answer$/);
	if (answer && m === "POST") {
		const { option } = await bodyOf(req);
		const n = state.notifications.find((x) => x.id === answer[1]);
		if (!n) return error(404, "not_found", "notification not found");
		if (n.resolvedAt)
			return error(409, "input_resolved", "request already answered elsewhere");
		if (!n.quickAnswer || !n.threadId)
			return error(422, "not_answerable", "not answerable with one tap");
		if (typeof option !== "number")
			return error(400, "invalid_request", "缺少 option");
		if (!n.quickAnswer.options.some((o) => o.i === option))
			return error(400, "invalid_option", "option out of range");
		n.resolvedAt = Date.now();
		n.quickAnswer = undefined;
		hooks?.answered(n.threadId);
		return json({ threadId: n.threadId });
	}
	if (p === "/api/v1/notifications/read" && m === "POST") {
		const { threadId, id } = await bodyOf(req);
		// 与服务端一致：字段给出但为空串、或两者都给出 → 400；都省略才是「全部已读」
		if (id === "" || threadId === "")
			return error(400, "invalid_request", "id / threadId 不能为空串");
		if (threadId !== undefined && id !== undefined)
			return error(400, "invalid_request", "id 与 threadId 至多给一个");
		for (const n of state.notifications)
			if (
				!n.readAt &&
				(id ? n.id === id : !threadId || n.threadId === threadId)
			)
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
				events: (b.events as Channel["events"]) ?? [
					"done",
					"input",
					"error",
					"budget",
				],
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
