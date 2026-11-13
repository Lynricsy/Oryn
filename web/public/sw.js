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
 */

const SAFARI = /^((?!chrome|android).)*safari/i;
const PROBE = "oryn.probe";
const NAVIGATE = "oryn.navigate";
/** 等页面应答的上限：要赶在通知点击的用户激活失效之前（Firefox 只给 1s）聚焦或打开窗口。 */
const REPLY_MS = 250;

self.addEventListener("install", () => {
	self.skipWaiting();
});

self.addEventListener("activate", (event) => {
	event.waitUntil(self.clients.claim());
});

/** 推送载荷 `{v, id, kind, title, body, url, tag}`；解析失败按空对象处理。 */
function payloadOf(event) {
	try {
		return event.data?.json() ?? {};
	} catch {
		return {};
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
			if (!SAFARI.test(self.navigator.userAgent)) {
				const windows = await self.clients.matchAll({
					type: "window",
					includeUncontrolled: true,
				});
				const apps = await appWindows(windows.filter((w) => w.focused));
				if (apps.some((x) => x.reply.toasts === true)) return;
			}
			await self.registration.showNotification(d.title ?? "Oryn", {
				body: d.body ?? "",
				tag: d.tag,
				data: { url: d.url ?? "/" },
				icon: "/icon-192.png",
				badge: "/icon-192.png",
			});
		})(),
	);
});

self.addEventListener("notificationclick", (event) => {
	event.notification.close();
	const url = event.notification.data?.url ?? "/";
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
