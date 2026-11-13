/**
 * Web Push 与 service worker（M5 计划 6.4）：注册 `/sw.js`、本浏览器的推送订阅与退订。
 * 订阅 = `GET push/vapid-key` → `pushManager.subscribe` → `POST push/subscriptions`；
 * 退订 = 按 endpoint 在服务端列表里找到 id → `DELETE` → 浏览器侧 `unsubscribe()`。
 * 「推送有效」= 本浏览器有订阅 **且** 服务端登记里有同一 endpoint（另一台设备可能已移除它，本地对象却还在）。
 * 与 service worker 的握手见 `public/sw.js`：应答 `oryn.probe`（表明这里有应用接收器、此刻能否自己弹站内提示），
 * 执行 `oryn.navigate` 后回 `{ok: true}`。
 * 桌面壳（M6）里不注册 service worker、不订阅推送：App 常驻托盘，通知经 WebSocket 到达后由壳弹系统通知。
 */
import { api, type Schemas, unwrap } from "../api/client";
import { host } from "./host";

export const SW_URL = "/sw.js";
/** service worker 发给页面的跳转消息（点击系统通知时）。 */
export const NAVIGATE_MESSAGE = "oryn.navigate";
/** service worker 询问页面是否装着应用接收器。 */
export const PROBE_MESSAGE = "oryn.probe";

/** 浏览器具备 Web Push 的全部前提（安全上下文 + service worker + PushManager + Notification）。 */
export function pushSupported(): boolean {
	return (
		host.kind !== "tauri" &&
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

export interface WorkerReceiver {
	/** 站内跳转（react-router）。 */
	navigate(url: string): void;
	/** 此刻收到通知会自己弹站内提示（页面可见且实时连接在线）；为真时 service worker 不再弹系统通知。 */
	showsToasts(): boolean;
}

let receiver: WorkerReceiver | null = null;

function onWorkerMessage(ev: MessageEvent) {
	const data = ev.data as { type?: unknown; url?: unknown } | null;
	const port = ev.ports[0];
	if (!receiver) return;
	if (data?.type === PROBE_MESSAGE) {
		port?.postMessage({ app: true, toasts: receiver.showsToasts() });
		return;
	}
	if (data?.type === NAVIGATE_MESSAGE && typeof data.url === "string") {
		receiver.navigate(data.url);
		port?.postMessage({ ok: true });
	}
}

/**
 * 注册 service worker，并作为应用接收器应答它的 `oryn.probe` / `oryn.navigate`。
 * 不支持 service worker 或在桌面壳里时无操作；返回注销监听的函数（注销后本页不再被当作应用窗口）。
 */
export function registerServiceWorker(r: WorkerReceiver): () => void {
	if (
		host.kind === "tauri" ||
		typeof navigator === "undefined" ||
		!("serviceWorker" in navigator)
	)
		return () => {};
	receiver = r;
	navigator.serviceWorker.addEventListener("message", onWorkerMessage);
	navigator.serviceWorker.register(SW_URL).catch(() => {
		// 注册失败（如非安全上下文）只影响后台推送，不影响站内通知
	});
	return () => {
		navigator.serviceWorker.removeEventListener("message", onWorkerMessage);
		if (receiver === r) receiver = null;
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

/**
 * 本浏览器的推送是否真正有效：本地有订阅，且服务端登记里有同一 endpoint。
 * 返回本地订阅的 endpoint（没有为 null）与是否已登记；不会替用户自动重新登记。
 */
export async function pushRegistration(): Promise<{
	endpoint: string | null;
	registered: boolean;
}> {
	const sub = await currentSubscription();
	if (!sub) return { endpoint: null, registered: false };
	const { subscriptions } = await unwrap(api.GET("/api/v1/push/subscriptions"));
	return {
		endpoint: sub.endpoint,
		registered: subscriptions.some((s) => s.endpoint === sub.endpoint),
	};
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
