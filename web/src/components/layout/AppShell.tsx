/**
 * 应用壳：三档自适应——expanded（≥840）侧边栏常驻，是浮在壁纸上的悬浮玻璃（macOS Tahoe 式），
 * 中栏与右栏是纸面，8px 缝里露出壁纸，侧栏可收起（存 localStorage）；medium（600–839）侧边栏可折叠推入，
 * 同样是悬浮玻璃；compact（<600）单栏 + 同材质的玻璃抽屉侧边栏（只圆右侧）。壁纸见 index.css 的 `.shell-wash`。
 * 收起 / 展开是连续动画：外层列宽与右侧缝一起收到 0，内层玻璃保持固定宽度、同步左移并淡出（不挤扁内容）。
 * 右侧信息面板由线程页自行处理。
 * 通知（M5）：连接建立 / 重连后拉一次最近通知；鉴权通过后（非 mock 模式）注册 service worker，
 * 它发来的 `oryn.navigate` 与系统通知的点击都走 react-router 跳转；toast 挂在这里。
 */
import { type ReactNode, useEffect, useRef } from "react";
import { useTranslation } from "react-i18next";
import { Navigate, Outlet, useLocation, useNavigate } from "react-router";
import { UNAUTHORIZED_EVENT } from "../../api/client";
import { useMe } from "../../api/queries";
import { currentSubscription, registerServiceWorker } from "../../lib/push";
import { useBreakpoint } from "../../lib/viewport";
import { realtime } from "../../realtime/socket";
import {
	notificationsStore,
	setNotificationNavigator,
} from "../../state/notifications";
import { useConnection } from "../../state/stores";
import { uiStore, useUi } from "../../state/ui";
import { NotificationToasts } from "../notify/NotificationToasts";
import { ServerErrors } from "../ServerErrors";
import { Spinner } from "../ui/primitives";
import { Sidebar } from "./Sidebar";

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
	const collapsed = useUi((s) => s.sidebarCollapsed);
	const conn = useConnection((s) => s.status);
	useGlobalShortcuts();

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
		const off = registerServiceWorker((url) => navRef.current(url));
		void currentSubscription().then(
			(sub) => notificationsStore.getState().setPushActive(sub !== null),
			() => undefined,
		);
		return off;
	}, [me.data]);

	useEffect(() => {
		const onUnauthorized = () =>
			navigate(`/login?next=${encodeURIComponent(location.pathname)}`);
		window.addEventListener(UNAUTHORIZED_EVENT, onUnauthorized);
		return () => window.removeEventListener(UNAUTHORIZED_EVENT, onUnauthorized);
	}, [navigate, location.pathname]);

	if (me.isPending) {
		return (
			<div className="flex h-dvh items-center justify-center text-muted">
				<Spinner size={20} />
			</div>
		);
	}
	if (me.isError || conn === "unauthorized") {
		return (
			<Navigate
				to={`/login?next=${encodeURIComponent(location.pathname)}`}
				replace
			/>
		);
	}

	const expanded = bp === "expanded";
	// 列侧栏（expanded / medium）是否展开；compact 走下面的抽屉
	const shown = expanded ? !collapsed : bp === "medium" && sidebarOpen;
	return (
		<div
			className={`shell-wash flex h-dvh w-full overflow-hidden text-fg ${expanded ? "p-2" : ""}`}
		>
			{bp !== "compact" && (
				// 外层只负责占位宽度与右侧 8px 缝；内层固定宽度，translate 与列宽同时长同缓动，
				// 右缘始终贴着列右缘，所以外层不必 overflow-hidden（那会裁掉玻璃的描边与投影）
				<div
					inert={!shown}
					data-sidebar={shown ? "shown" : "collapsed"}
					className={`h-full shrink-0 transition-[width,margin-right] duration-[260ms] ease-out ${shown ? `w-64 lg:w-72 ${expanded ? "mr-2" : ""}` : "mr-0 w-0"}`}
				>
					<div
						className={`h-full w-64 transition-[translate,opacity] duration-[260ms] ease-out lg:w-72 ${expanded ? "" : "p-2"} ${shown ? "" : "-translate-x-full opacity-0"}`}
					>
						<div className="glass-sidebar h-full">
							<Sidebar />
						</div>
					</div>
				</div>
			)}
			{bp === "compact" && sidebarOpen && (
				<div className="fixed inset-0 z-40 flex">
					<div className="glass-sidebar h-full w-[85vw] max-w-xs rounded-l-none transition-[translate] duration-[260ms] ease-out starting:-translate-x-full">
						<Sidebar />
					</div>
					<button
						type="button"
						aria-label={t("common.close")}
						className="flex-1 bg-scrim transition-opacity duration-[260ms] ease-out starting:opacity-0"
						onClick={() => uiStore.getState().setSidebar(false)}
					/>
				</div>
			)}
			<main className="flex h-full min-w-0 flex-1 flex-col pr-safe">
				<Outlet />
			</main>
			<ServerErrors />
			<NotificationToasts />
		</div>
	);
}
