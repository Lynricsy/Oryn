/*
 * Oryn service worker（M5 计划 6.4）：只处理 Web Push 与通知点击，没有 fetch 处理器（不做离线缓存）。
 * 手写 JS，不经打包；由 `lib/push.ts` 的 registerServiceWorker() 注册为 `/sw.js`。
 */

const SAFARI = /^((?!chrome|android).)*safari/i;

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

self.addEventListener("push", (event) => {
	const d = payloadOf(event);
	event.waitUntil(
		self.clients
			.matchAll({ type: "window", includeUncontrolled: true })
			.then((windows) => {
				// 有聚焦的窗口时页面内已有 toast；Safari 要求每次推送都显示通知，否则会吊销订阅
				if (
					!SAFARI.test(self.navigator.userAgent) &&
					windows.some((w) => w.focused)
				)
					return undefined;
				return self.registration.showNotification(d.title ?? "Oryn", {
					body: d.body ?? "",
					tag: d.tag,
					data: { url: d.url ?? "/" },
					icon: "/icon-192.png",
					badge: "/icon-192.png",
				});
			}),
	);
});

self.addEventListener("notificationclick", (event) => {
	event.notification.close();
	const url = event.notification.data?.url ?? "/";
	event.waitUntil(
		self.clients
			.matchAll({ type: "window", includeUncontrolled: true })
			.then((windows) => {
				const win = windows[0];
				if (!win) return self.clients.openWindow(url);
				win.postMessage({ type: "oryn.navigate", url });
				return win.focus();
			}),
	);
});
