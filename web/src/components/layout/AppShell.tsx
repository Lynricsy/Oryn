/**
 * 应用壳：三档自适应——expanded（≥840）侧边栏常驻，且三栏都是纸面，8px 缝里露出桌面晕染；
 * medium（600–839）侧边栏可折叠推入，透明坐在桌面上；compact（<600）单栏 + 玻璃抽屉侧边栏。
 * 右侧信息面板由线程页自行处理。
 */
import { type ReactNode, useEffect } from "react";
import { useTranslation } from "react-i18next";
import { Navigate, Outlet, useLocation, useNavigate } from "react-router";
import { UNAUTHORIZED_EVENT } from "../../api/client";
import { useMe } from "../../api/queries";
import { useBreakpoint } from "../../lib/viewport";
import { realtime } from "../../realtime/socket";
import { useConnection } from "../../state/stores";
import { uiStore, useUi } from "../../state/ui";
import { ServerErrors } from "../ServerErrors";
import { Sheet } from "../ui/data";
import { Spinner } from "../ui/primitives";
import { Sidebar } from "./Sidebar";

function useGlobalShortcuts() {
	const navigate = useNavigate();
	useEffect(() => {
		const onKey = (e: KeyboardEvent) => {
			const mod = e.ctrlKey || e.metaKey;
			if (mod && e.key.toLowerCase() === "k") {
				e.preventDefault();
				uiStore.getState().focusSearch();
			} else if (mod && e.shiftKey && e.key.toLowerCase() === "o") {
				e.preventDefault();
				navigate("/");
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
	const conn = useConnection((s) => s.status);
	useGlobalShortcuts();

	useEffect(() => {
		if (!me.data) return;
		realtime.connect();
		return () => realtime.close();
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
	const column = expanded || (bp === "medium" && sidebarOpen);
	return (
		<div
			className={`shell-wash flex h-dvh w-full overflow-hidden text-fg ${expanded ? "gap-2 p-2" : ""}`}
		>
			{column &&
				(expanded ? (
					<Sheet inset className="h-full w-64 shrink-0 lg:w-72">
						<Sidebar />
					</Sheet>
				) : (
					<div className="h-full w-64 shrink-0 lg:w-72">
						<Sidebar />
					</div>
				))}
			{bp === "compact" && sidebarOpen && (
				<div className="fixed inset-0 z-40 flex">
					<div className="glass h-full w-[85vw] max-w-xs rounded-r-xl shadow-xl transition-[translate] duration-[260ms] ease-out starting:-translate-x-full">
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
		</div>
	);
}
