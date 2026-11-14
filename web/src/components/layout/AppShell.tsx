/**
 * 应用壳：三档自适应——expanded（≥840）侧边栏常驻，是浮在壁纸上的悬浮玻璃（macOS Tahoe 式），
 * 中栏与右栏是纸面，8px 缝里露出壁纸，侧栏可收起（存 localStorage）；medium（600–839）侧边栏可折叠推入，
 * 同样是悬浮玻璃；compact（<600）是下沉式侧栏（SinkLayout：侧栏常驻底层，内容卡片右移露出它）；
 * Viewport Segments 双段时是同材质的悬浮抽屉（四角都圆、四周留 8px 与安全区，背后整屏模糊并轻度压暗）。壁纸见 index.css 的 `.shell-wash`。
 * 收起 / 展开是连续动画：外层列宽与右侧缝一起收到 0，内层玻璃保持固定宽度、同步左移并淡出（不挤扁内容）。
 * 右侧信息面板由线程页自行处理。
 * 通知（M5）：连接建立 / 重连后拉一次最近通知；鉴权通过后（非 mock 模式）注册 service worker 并作为应用接收器
 * 应答它的探询，它发来的 `oryn.navigate` 与系统通知的点击都走 react-router 跳转；toast 挂在这里。
 * 桌面壳（M6）：挂载时 `host.ready` 握手（壳里待开的路径随返回值交出），壳推来的 `navigate` 走路由跳转；
 * 只有明确的鉴权失效（`me` 401、WS unauthorized、请求钩子的 401 事件）才交给 `host.reauth` 由壳重新换取 App 会话
 * （壳里没有可用的 Passkey，不去 `/login`）；`me` 的网络错误 / 5xx 显示失败与重试，已有身份时保留当前界面；
 * 线程索引变化后 300ms 去抖把待处理 / 未读计数交给托盘（`host.setStatus`），界面语言变化同步给壳。
 * 移动壳（M7）：系统栏颜色跟随页面底色（`host.mobile.setChrome`）；系统分享进来的内容存入 share-intake 并跳到首页
 * 由新建线程框预填，随即 `shareAck`；Viewport Segments 双段（折叠屏半开）时侧栏走悬浮抽屉。
 * 连接状态写到 `<html data-conn>`（验收观察面）。左侧玻璃侧栏贴着屏幕左缘，左两角与屏幕圆角同心（`.dc-*`）。
 * 桌面 App 无系统标题栏：加载中 / 加载失败的整页也贴顶放一条可拖动的标题行与窗口按钮（`ChromeBar`）。
 */
import { type ReactNode, useEffect, useRef } from "react";
import { useTranslation } from "react-i18next";
import { Navigate, Outlet, useLocation, useNavigate } from "react-router";
import { ApiError, UNAUTHORIZED_EVENT } from "../../api/client";
import { useMe } from "../../api/queries";
import { useRealtimeScheduleSync } from "../../api/schedule-queries";
import { host } from "../../lib/host";
import { watchChrome } from "../../lib/host-chrome";
import { computeHostStatus } from "../../lib/host-status";
import { usePresence } from "../../lib/presence";
import { pushRegistration, registerServiceWorker } from "../../lib/push";
import { useBreakpoint, useViewportSegments } from "../../lib/viewport";
import { realtime } from "../../realtime/socket";
import {
	notificationsStore,
	setNotificationNavigator,
} from "../../state/notifications";
import { shareIntakeStore } from "../../state/share-intake";
import { connectionStore, indexStore, useConnection } from "../../state/stores";
import { uiStore, useSidebarShown, useUi } from "../../state/ui";
import { NotificationToasts } from "../notify/NotificationToasts";
import { ServerErrors } from "../ServerErrors";
import { ActionError } from "../ui/ActionError";
import { Button, Spinner } from "../ui/primitives";
import { Sidebar } from "./Sidebar";
import { SinkLayout } from "./SinkLayout";
import { SinkTabBar } from "./SinkTabBar";
import { ChromeBar } from "./WindowControls";

/** 手机抽屉进出场时长，与列侧栏的收起动画一致 */
const DRAWER_MS = 260;
/** 托盘状态的去抖间隔：流式期间索引频繁变化（activity），不逐次打扰壳。 */
const HOST_STATUS_DEBOUNCE_MS = 300;

/** 桌面壳：启动握手、壳发起的跳转、托盘状态与界面语言（web 下全部无操作）。 */
function useHostBridge() {
	const { t, i18n } = useTranslation();
	const navigate = useNavigate();
	const location = useLocation();
	const navRef = useRef(navigate);
	navRef.current = navigate;
	const tRef = useRef(t);
	tRef.current = t;
	// 握手只在挂载时做一次，路径取挂载时的
	const startPath = useRef(location.pathname + location.search);

	useEffect(() => {
		void host.ready(i18n.language, startPath.current).then((p) => {
			if (p) navRef.current(p);
		});
	}, [i18n]);

	useEffect(() => host.onNavigate((path) => navRef.current(path)), []);

	// 移动壳：系统栏跟随页面底色；分享进来的内容交给新建线程框，确认后壳清掉缓存
	useEffect(() => {
		const mobile = host.mobile;
		if (!mobile) return;
		const offChrome = watchChrome(window, (c) => mobile.setChrome(c));
		const offShare = host.onShare((s) => {
			shareIntakeStore.getState().set(s);
			navRef.current("/");
			mobile.shareAck(s.id);
		});
		return () => {
			offChrome();
			offShare();
		};
	}, []);

	useEffect(() => {
		if (host.kind !== "tauri") return;
		let timer: ReturnType<typeof setTimeout> | undefined;
		let last = "";
		const push = () => {
			clearTimeout(timer);
			timer = setTimeout(() => {
				const status = computeHostStatus(
					Object.values(indexStore.getState().threads),
					tRef.current("thread.untitled"),
				);
				const key = JSON.stringify(status);
				if (key === last) return;
				last = key;
				host.setStatus(status);
			}, HOST_STATUS_DEBOUNCE_MS);
		};
		const onLanguage = (lang: string) => {
			host.setLanguage(lang);
			push();
		};
		const offIndex = indexStore.subscribe(push);
		i18n.on("languageChanged", onLanguage);
		push();
		return () => {
			clearTimeout(timer);
			offIndex();
			i18n.off("languageChanged", onLanguage);
		};
	}, [i18n]);
}
/** 焦点在可编辑区域（输入框、文本域、CodeMirror、contenteditable）时不抢快捷键：macOS 下 Ctrl+B 是光标左移。 */
function inEditable(target: EventTarget | null): boolean {
	return (
		target instanceof Element &&
		!!target.closest(
			'input, textarea, select, [contenteditable=""], [contenteditable="true"], .cm-editor',
		)
	);
}

function useGlobalShortcuts() {
	const navigate = useNavigate();
	useEffect(() => {
		const onKey = (e: KeyboardEvent) => {
			const mod = e.ctrlKey || e.metaKey;
			const key = e.key.toLowerCase();
			if (mod && key === "k") {
				e.preventDefault();
				uiStore.getState().focusSearch();
			} else if (mod && e.shiftKey && key === "o") {
				e.preventDefault();
				navigate("/");
			} else if (
				mod &&
				!e.shiftKey &&
				!e.altKey &&
				key === "b" &&
				!inEditable(e.target)
			) {
				e.preventDefault();
				uiStore.getState().toggleSidebar();
			} else if (mod && !e.altKey && (e.key === "/" || e.code === "Slash")) {
				// 全局助手：焦点在输入框里同样生效（Ctrl/⌘+/ 在编辑区没有别的含义）
				e.preventDefault();
				navigate("/assistant");
			}
		};
		window.addEventListener("keydown", onKey);
		return () => window.removeEventListener("keydown", onKey);
	}, [navigate]);
}

export function AppShell(): ReactNode {
	const { t } = useTranslation();
	const me = useMe();
	const location = useLocation();
	const navigate = useNavigate();
	const bp = useBreakpoint();
	const sidebarOpen = useUi((s) => s.sidebarOpen);
	// 列侧栏（expanded / medium）是否展开；双段走下面的抽屉，compact 走下沉侧栏
	const shown = useSidebarShown();
	const conn = useConnection((s) => s.status);
	// 双段（折叠屏半开）：线程页占两段，侧栏退成悬浮抽屉；手机（compact）是下沉式侧栏
	const segments = useViewportSegments();
	const drawerMode = segments !== null;
	const sink = bp === "compact" && !drawerMode;
	useGlobalShortcuts();
	useHostBridge();
	// 定时查询跟着 WS 推送与重连对账（M9 审查 W6）
	useRealtimeScheduleSync();
	const here = useRef("");
	here.current = location.pathname + location.search;
	// 双段抽屉的进出场（挂载与过渡状态）；钩子必须在下面的提前返回之前
	const drawer = usePresence(drawerMode && sidebarOpen, DRAWER_MS);

	useEffect(() => {
		document.documentElement.dataset.conn = conn;
	}, [conn]);

	useEffect(() => {
		if (!me.data) return;
		realtime.connect();
		return () => realtime.close();
	}, [me.data]);

	// navigate 随路由变化换实例：经 ref 调用，service worker 只在鉴权通过时注册一次
	const navRef = useRef(navigate);
	navRef.current = navigate;
	useEffect(() => {
		setNotificationNavigator((url) => navRef.current(url));
	}, []);

	useEffect(() => {
		if (conn === "open") void notificationsStore.getState().load();
	}, [conn]);

	useEffect(() => {
		if (!me.data || import.meta.env.VITE_MOCK === "1") return;
		const off = registerServiceWorker({
			navigate: (url) => navRef.current(url),
			// 页面可见且实时连接在线时，通知会经 WS 弹成站内 toast（见 state/notifications）
			showsToasts: () =>
				host.isForeground() && connectionStore.getState().status === "open",
		});
		// 推送有效 = 本地订阅 + 服务端登记；另一台设备移除了登记时显示未开启，由用户重新开启
		void pushRegistration().then(
			(r) => notificationsStore.getState().setPushActive(r.registered),
			() => undefined,
		);
		return off;
	}, [me.data]);

	useEffect(() => {
		const onUnauthorized = () => {
			if (host.kind === "tauri") host.reauth(here.current);
			else navigate(`/login?next=${encodeURIComponent(location.pathname)}`);
		};
		window.addEventListener(UNAUTHORIZED_EVENT, onUnauthorized);
		return () => window.removeEventListener(UNAUTHORIZED_EVENT, onUnauthorized);
	}, [navigate, location.pathname]);

	// 浏览器：沿用旧判定，`me` 任何失败都回登录页
	const unauthorized = me.isError || conn === "unauthorized";
	// 壳里只有明确的鉴权失效才重新换取 App 会话：reauth 会删旧会话、整页导航并计入壳的「60 秒内 3 次」循环保护，
	// 网络错误 / 5xx 不能触发它（凭据仍然有效，换取只会白白清掉页面状态，持续出错时还会把用户锁在连接页）
	const authLost =
		(me.error instanceof ApiError && me.error.status === 401) ||
		conn === "unauthorized";
	const tauri = host.kind === "tauri";
	// 壳里：交给壳重新换取 App 会话（成功后整页重新加载，带着当前路径回来）
	useEffect(() => {
		if (authLost && tauri) host.reauth(here.current);
	}, [authLost, tauri]);

	if (me.isPending || (authLost && tauri)) {
		return (
			<div className="relative flex h-dvh items-center justify-center text-muted">
				<ChromeBar />
				<Spinner size={20} />
			</div>
		);
	}
	// 壳里 `me` 非鉴权失败：首次加载失败显示原因与重试；已有身份时后台重取失败不打断当前界面
	if (tauri && me.isError && !me.data) {
		return (
			<div className="relative flex h-dvh flex-col items-center justify-center gap-3 p-6">
				<ChromeBar />
				<ActionError step="meLoad" error={me.error} />
				<Button
					size="sm"
					disabled={me.isFetching}
					onClick={() => void me.refetch()}
				>
					{t("actionError.retry")}
				</Button>
			</div>
		);
	}
	if (unauthorized && !tauri) {
		return (
			<Navigate
				to={`/login?next=${encodeURIComponent(location.pathname)}`}
				replace
			/>
		);
	}

	const main = (
		<main className="flex h-full min-w-0 flex-1 flex-col pr-safe">
			<Outlet />
		</main>
	);
	if (sink)
		return (
			<>
				<SinkLayout sidebar={<Sidebar sink />} footer={<SinkTabBar />}>
					{main}
				</SinkLayout>
				<ServerErrors />
				<NotificationToasts />
			</>
		);

	const expanded = bp === "expanded";
	// 侧栏玻璃离屏幕左、上、下缘 8px：左两角取同心半径
	const sidebarCorners = "dc-tl dc-bl [--dc-gap:0.5rem] [--dc-r:var(--r-2xl)]";
	return (
		<div
			// 双段时不留外边距：线程页按段的视口坐标分窗格
			className={`shell-wash flex h-dvh w-full overflow-hidden text-fg ${expanded && !segments ? "p-2" : ""}`}
		>
			{!drawerMode && (
				// 外层只负责占位宽度与右侧 8px 缝；内层固定宽度，translate 与列宽同时长同缓动，
				// 右缘始终贴着列右缘，所以外层不必 overflow-hidden（那会裁掉玻璃的描边与投影）
				<div
					inert={!shown}
					data-sidebar={shown ? "shown" : "collapsed"}
					className={`h-full shrink-0 transition-[width,margin-right] duration-[260ms] ease-out ${shown ? `w-64 lg:w-72 ${expanded ? "mr-2" : ""}` : "mr-0 w-0"}`}
				>
					{/* 只动 translate、不动 opacity（opacity<1 会让玻璃的模糊在收起途中失效，见下面抽屉的注释）；多移 1rem 越过 8px 外边距，完全藏住 */}
					<div
						className={`h-full w-64 transition-[translate] duration-[260ms] ease-out lg:w-72 ${expanded ? "" : "p-2"} ${shown ? "" : "-translate-x-[calc(100%+1rem)]"}`}
					>
						<div className={`glass-sidebar h-full ${sidebarCorners}`}>
							<Sidebar />
						</div>
					</div>
				</div>
			)}
			{drawer.mounted && (
				<div className="fixed inset-0 z-40">
					{/* 背景整屏只做模糊、点击关闭；压暗在抽屉外壳的 .drawer-spot 外投影上，抽屉底下不压暗（否则玻璃透上来发灰） */}
					<button
						type="button"
						aria-label={t("common.close")}
						className={`absolute inset-0 backdrop-blur-sm transition-opacity duration-[260ms] ease-out ${drawer.shown ? "opacity-100" : "opacity-0"}`}
						onClick={() => uiStore.getState().setSidebar(false)}
					/>
					{/*
					 * 只动 translate、不动 opacity：opacity<1 的元素是 backdrop root，玻璃的 backdrop-filter 只能模糊它自己的内容，
					 * 滑入途中整块玻璃会变成「透明不模糊」、落位才突然糊上。滑出量多 1rem，阴影也完全移出屏外。
					 */}
					<div
						inert={!sidebarOpen}
						data-shown={drawer.shown ? "" : undefined}
						className={`drawer-spot ${sidebarCorners} absolute top-[max(0.5rem,env(safe-area-inset-top))] bottom-[max(0.5rem,env(safe-area-inset-bottom))] left-[max(0.5rem,env(safe-area-inset-left))] w-[min(85vw,20rem)] ${drawer.shown ? "translate-x-0" : "-translate-x-[calc(100%+1rem)]"}`}
					>
						<div className={`glass-sidebar h-full ${sidebarCorners}`}>
							<Sidebar floating />
						</div>
					</div>
				</div>
			)}
			{main}
			<ServerErrors />
			<NotificationToasts />
		</div>
	);
}
