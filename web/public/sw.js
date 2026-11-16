/*
 * Oryn service worker（M5 计划 6.4）：只处理 Web Push 与通知点击，没有 fetch 处理器（不做离线缓存）。
 * 手写 JS，不经打包；由 `lib/push.ts` 的 registerServiceWorker() 注册为 `/sw.js`。
 *
 * 同源窗口不一定是 Oryn 应用：匿名分享页 `/s/<slug>`、登录 / 配对页都没有站内通知与跳转的接收器。
 * 所以不按「聚焦 / 可见」猜，而是先问：向窗口发 `oryn.probe`（带 MessageChannel 端口），
 * 装了接收器的页面（`lib/push.ts`）应答 `{app: true, toasts}`，`toasts` = 它此刻会自己弹站内提示。
 * - 推送：只有某个聚焦窗口确认 `toasts: true` 才不弹系统通知；没有确认一律弹（Safari 要求每次都弹）。
 * - 点击：只把 `oryn.navigate` 发给确认过的应用窗口（优先聚焦的），页面应答 `{ok: true}` 才算跳转成功；
 *   没有应用窗口或没有应答 → 新开窗口打开该地址（私有路由自己的鉴权守卫处理登录）。
 * - 操作按钮：可一触作答的 `input` 载荷带 `actions: [{i, label, role?}]`，按钮数超过浏览器上限
 *   （`Notification.maxActions`）时一个都不显示，免得漏掉「拒绝」。点按钮带 Cookie 调
 *   `POST /api/v1/notifications/{id}/answer`，结果以同 tag 的通知替换原通知（失败时保留原文与按钮）。
 * - 撤回：`{kind: "withdraw", id, tag}` 把仍显示着同一 id、未作答的通知换成「已在别处作答」。
 *   换而不关：推送事件结束时必须留有可见通知，否则 Chrome 会弹「此网站已在后台更新」。
 */

const SAFARI = /^((?!chrome|android).)*safari/i;
const PROBE = "oryn.probe";
const NAVIGATE = "oryn.navigate";
/** 等页面应答的上限：要赶在通知点击的用户激活失效之前（Firefox 只给 1s）聚焦或打开窗口。 */
const REPLY_MS = 250;
const ICON = "/icon-192.png";
/** 与服务端 `pushActions` 一致：2–3 个按钮。 */
const MAX_ACTIONS = 3;
const NOTIFICATION_ID = /^ntf_[0-9A-Za-z]{1,64}$/;

/** 固定文案按浏览器语言（service worker 拿不到界面语言设置）。 */
const TEXT = /^zh\b/i.test(self.navigator.language ?? "")
	? {
			yes: "是",
			no: "否",
			answered: "已作答",
			elsewhere: "已在别处作答",
			failed: "作答失败，点按打开 Oryn",
		}
	: {
			yes: "Yes",
			no: "No",
			answered: "Answered",
			elsewhere: "Already answered elsewhere",
			failed: "Couldn't answer. Tap to open Oryn.",
		};

self.addEventListener("install", () => {
	self.skipWaiting();
});

self.addEventListener("activate", (event) => {
	event.waitUntil(self.clients.claim());
});

/** 推送载荷 `{v, id, kind, title, body, url, tag, actions?}` 或撤回 `{v, kind: "withdraw", id, tag}`；解析失败按空对象处理。 */
function payloadOf(event) {
	try {
		return event.data?.json() ?? {};
	} catch {
		return {};
	}
}

/**
 * 载荷里的按钮 → `[{action, title}]`（`action` 为选项序号的字符串）。只认 input 的 2–3 个合法按钮，且不超过
 * 浏览器能显示的数量；否则为空数组（只显示普通通知，点按打开线程）。confirm 的 `role` 按语言显示「是 / 否」。
 */
function actionsOf(d) {
	if (d.kind !== "input" || !Array.isArray(d.actions)) return [];
	const limit = Math.min(self.Notification?.maxActions ?? 0, MAX_ACTIONS);
	if (d.actions.length < 2 || d.actions.length > limit) return [];
	const out = [];
	for (const a of d.actions) {
		if (!Number.isInteger(a?.i) || typeof a.label !== "string" || !a.label)
			return [];
		const title = a.role === "yes" || a.role === "no" ? TEXT[a.role] : a.label;
		out.push({ action: String(a.i), title });
	}
	return out;
}

/**
 * 系统通知的选项。`data` 记下通知 id 与按钮，作答失败时据此恢复；`state` 非空 = 已作答 / 已在别处作答 / 失败后的替换版。
 */
function notificationOptions({ body, tag, url, id, actions, state }) {
	const opts = {
		body,
		tag,
		data: { url, id, actions, state },
		icon: ICON,
		badge: ICON,
	};
	if (actions.length > 0) opts.actions = actions;
	if (state) opts.silent = true;
	return opts;
}

/** 撤回：只换掉仍显示着同一 id、尚未作答的那条（同一线程后来的通知 id 不同，不受影响）。 */
async function withdraw(d) {
	if (typeof d.tag !== "string" || typeof d.id !== "string") return;
	const shown = await self.registration.getNotifications({ tag: d.tag });
	for (const n of shown) {
		if (n.data?.id !== d.id || n.data?.state) continue;
		await self.registration.showNotification(
			n.title,
			notificationOptions({
				body: TEXT.elsewhere,
				tag: d.tag,
				url: n.data.url ?? "/",
				id: d.id,
				actions: [],
				state: "elsewhere",
			}),
		);
	}
}

/** 点了操作按钮：带 Cookie 作答，再用同 tag 的通知替换原通知显示结果；不打开窗口。 */
async function answer(n, action) {
	const data = n.data ?? {};
	const option = Number(action);
	let result = "failed";
	if (NOTIFICATION_ID.test(data.id ?? "") && Number.isInteger(option)) {
		try {
			const res = await self.fetch(`/api/v1/notifications/${data.id}/answer`, {
				method: "POST",
				credentials: "same-origin",
				headers: { "Content-Type": "application/json", "X-Oryn-CSRF": "1" },
				body: JSON.stringify({ option }),
			});
			if (res.ok) result = "answered";
			else if (res.status === 409) result = "elsewhere";
		} catch {
			// 网络错误：按失败处理
		}
	}
	const failed = result === "failed";
	await self.registration.showNotification(
		n.title,
		notificationOptions({
			body: failed
				? [n.body, TEXT.failed].filter(Boolean).join("\n")
				: TEXT[result],
			tag: n.tag,
			url: data.url ?? "/",
			id: data.id,
			// 失败时保留按钮可重试；点通知本身仍打开线程
			actions: failed && Array.isArray(data.actions) ? data.actions : [],
			state: result,
		}),
	);
}

/**
 * 通知点击的落点只允许本站：按 URL 标准相对本站解析（`\` 当 `/`、去掉制表与换行），不同源、无法解析或
 * 不是字符串一律落到 `/`；同源时返回规范化后的路径 + 查询 + 片段（sec F7：`/\evil.example` 会被解析成外站）。
 */
function sameOriginPath(url) {
	if (typeof url !== "string") return "/";
	try {
		const u = new URL(url, self.location.origin);
		if (u.origin !== self.location.origin) return "/";
		return u.pathname + u.search + u.hash;
	} catch {
		return "/";
	}
}

/** 发一条消息并等页面经端口应答；超时或发送失败为 null。 */
function ask(win, message) {
	return new Promise((resolve) => {
		const channel = new MessageChannel();
		const timer = setTimeout(() => done(null), REPLY_MS);
		function done(value) {
			clearTimeout(timer);
			channel.port1.onmessage = null;
			channel.port1.close();
			resolve(value);
		}
		channel.port1.onmessage = (ev) => done(ev.data ?? null);
		try {
			win.postMessage(message, [channel.port2]);
		} catch {
			done(null);
		}
	});
}

/** 确认过装着 Oryn 应用接收器的窗口及其应答。 */
async function appWindows(windows) {
	const replies = await Promise.all(
		windows.map((w) => ask(w, { type: PROBE })),
	);
	return windows
		.map((win, i) => ({ win, reply: replies[i] }))
		.filter((x) => x.reply?.app === true);
}

self.addEventListener("push", (event) => {
	const d = payloadOf(event);
	event.waitUntil(
		(async () => {
			if (d.kind === "withdraw") return withdraw(d);
			if (!SAFARI.test(self.navigator.userAgent)) {
				const windows = await self.clients.matchAll({
					type: "window",
					includeUncontrolled: true,
				});
				const apps = await appWindows(windows.filter((w) => w.focused));
				if (apps.some((x) => x.reply.toasts === true)) return;
			}
			await self.registration.showNotification(
				d.title ?? "Oryn",
				notificationOptions({
					body: d.body ?? "",
					tag: d.tag,
					url: d.url ?? "/",
					id: typeof d.id === "string" ? d.id : undefined,
					actions: actionsOf(d),
				}),
			);
		})(),
	);
});

self.addEventListener("notificationclick", (event) => {
	if (event.action) {
		event.waitUntil(answer(event.notification, event.action));
		return;
	}
	event.notification.close();
	const url = sameOriginPath(event.notification.data?.url ?? "/");
	event.waitUntil(
		(async () => {
			const windows = await self.clients.matchAll({
				type: "window",
				includeUncontrolled: true,
			});
			const apps = await appWindows(windows);
			const target = apps.find((x) => x.win.focused) ?? apps[0];
			if (target) {
				const reply = await ask(target.win, { type: NAVIGATE, url });
				if (reply?.ok === true) return target.win.focus();
			}
			return self.clients.openWindow(url);
		})(),
	);
});
