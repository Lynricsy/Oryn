/**
 * Web Push 与 service worker（M5 计划 6.4）：注册 `/sw.js`、本浏览器的推送订阅与退订。
 * 订阅 = `GET push/vapid-key` → `pushManager.subscribe` → `POST push/subscriptions`；
 * 退订 = 按 endpoint 在服务端列表里找到 id → `DELETE` → 浏览器侧 `unsubscribe()`。
 */
import { api, type Schemas, unwrap } from "../api/client";

export const SW_URL = "/sw.js";
/** service worker 发给页面的跳转消息（点击系统通知时）。 */
export const NAVIGATE_MESSAGE = "oryn.navigate";

/** 浏览器具备 Web Push 的全部前提（安全上下文 + service worker + PushManager + Notification）。 */
export function pushSupported(): boolean {
	return (
		typeof window !== "undefined" &&
		window.isSecureContext === true &&
		"serviceWorker" in navigator &&
		"PushManager" in window &&
		"Notification" in window
	);
}

/** iOS / iPadOS 上 Web Push 只对「添加到主屏幕」的 Web App 开放。 */
export function needsHomeScreen(): boolean {
	if (typeof navigator === "undefined") return false;
	const ios =
		/iPad|iPhone|iPod/.test(navigator.userAgent) ||
		(navigator.platform === "MacIntel" && navigator.maxTouchPoints > 1);
	const standalone =
		typeof matchMedia === "function" &&
		matchMedia("(display-mode: standalone)").matches;
	return ios && !standalone;
}

/** base64url（可无填充）→ 字节；VAPID 公钥作 `applicationServerKey`。 */
export function base64UrlToBytes(s: string): Uint8Array<ArrayBuffer> {
	const b64 = s.replace(/-/g, "+").replace(/_/g, "/");
	const padded = b64 + "=".repeat((4 - (b64.length % 4)) % 4);
	const bin = atob(padded);
	const out = new Uint8Array(new ArrayBuffer(bin.length));
	for (let i = 0; i < bin.length; i++) out[i] = bin.charCodeAt(i);
	return out;
}

let navigateHandler: ((url: string) => void) | null = null;

function onWorkerMessage(ev: MessageEvent) {
	const data = ev.data as { type?: unknown; url?: unknown } | null;
	if (data?.type === NAVIGATE_MESSAGE && typeof data.url === "string")
		navigateHandler?.(data.url);
}

/**
 * 注册 service worker，并把它发来的 `oryn.navigate` 交给 `navigate`（站内路径）。
 * 不支持 service worker 时无操作；返回注销监听的函数。
 */
export function registerServiceWorker(
	navigate: (url: string) => void,
): () => void {
	if (typeof navigator === "undefined" || !("serviceWorker" in navigator))
		return () => {};
	navigateHandler = navigate;
	navigator.serviceWorker.addEventListener("message", onWorkerMessage);
	navigator.serviceWorker.register(SW_URL).catch(() => {
		// 注册失败（如非安全上下文）只影响后台推送，不影响站内通知
	});
	return () => {
		navigator.serviceWorker.removeEventListener("message", onWorkerMessage);
		if (navigateHandler === navigate) navigateHandler = null;
	};
}

async function registration(): Promise<ServiceWorkerRegistration | null> {
	if (!pushSupported()) return null;
	return (
		(await navigator.serviceWorker.getRegistration(SW_URL)) ??
		(await navigator.serviceWorker.register(SW_URL))
	);
}

/** 本浏览器当前的推送订阅（没有或不支持时为 null）。 */
export async function currentSubscription(): Promise<PushSubscription | null> {
	if (!pushSupported()) return null;
	const reg = await navigator.serviceWorker.getRegistration(SW_URL);
	return (await reg?.pushManager.getSubscription()) ?? null;
}

/** `PushSubscription.toJSON()` → 接口请求体；缺密钥时报错。 */
export function subscriptionInput(
	sub: PushSubscriptionJSON,
): Schemas["PushSubscriptionInput"] {
	const { endpoint, keys } = sub;
	if (!endpoint || !keys?.p256dh || !keys.auth)
		throw new Error("push subscription is missing keys");
	return { endpoint, keys: { p256dh: keys.p256dh, auth: keys.auth } };
}

/** 订阅后台推送并登记到服务端；需要先拿到通知权限（调用方负责 `requestPermission`）。 */
export async function subscribePush(): Promise<Schemas["PushSubscription"]> {
	const reg = await registration();
	if (!reg) throw new Error("push not supported");
	await navigator.serviceWorker.ready;
	const { publicKey } = await unwrap(api.GET("/api/v1/push/vapid-key"));
	const sub =
		(await reg.pushManager.getSubscription()) ??
		(await reg.pushManager.subscribe({
			userVisibleOnly: true,
			applicationServerKey: base64UrlToBytes(publicKey),
		}));
	return unwrap(
		api.POST("/api/v1/push/subscriptions", {
			body: subscriptionInput(sub.toJSON()),
		}),
	);
}

/** 退订：先删服务端登记，再让浏览器放弃订阅。没有订阅时无操作。 */
export async function unsubscribePush(): Promise<void> {
	const sub = await currentSubscription();
	if (!sub) return;
	const { subscriptions } = await unwrap(api.GET("/api/v1/push/subscriptions"));
	const mine = subscriptions.find((s) => s.endpoint === sub.endpoint);
	if (mine)
		await unwrap(
			api.DELETE("/api/v1/push/subscriptions/{id}", {
				params: { path: { id: mine.id } },
			}),
		);
	await sub.unsubscribe();
}
