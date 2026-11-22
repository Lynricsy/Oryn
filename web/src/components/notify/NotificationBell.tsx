/**
 * 侧栏头部的通知铃铛（M5 计划 6.3）：有未读时右上角一个 accent 计数胶囊；点击打开最近 50 条通知。
 * 桌面是非模态玻璃弹层（外点 / Esc 关闭，照 `AccessPopover`），compact 档改用 `Dialog`（「全部已读」在对话框底栏，没有通知时不显示）。
 * 点一条 → 跳到它的 `url`，按 `readTargetOf` 标已读（有线程标该线程，系统通知只标这一条）；顶部「全部已读」。
 */
import {
	type ReactNode,
	useEffect,
	useLayoutEffect,
	useRef,
	useState,
} from "react";
import { useTranslation } from "react-i18next";
import { useNavigate } from "react-router";
import { formatRelative } from "../../lib/format";
import { focusablesIn } from "../../lib/modal";
import { useBreakpoint } from "../../lib/viewport";
import {
	type AppNotification,
	notificationsStore,
	notificationText,
	readTargetOf,
	useNotifications,
} from "../../state/notifications";
import { uiStore } from "../../state/ui";
import { Floating } from "../ui/Floating";
import { Icon, type IconName } from "../ui/icons";
import { Button, Dialog, EmptyState, IconButton } from "../ui/primitives";

/** 各 kind 的图标与颜色：完成绿、待答 accent（需要你）、出错红、预算告警黄。 */
export const KIND_ICON: Record<
	AppNotification["kind"],
	{ icon: IconName; className: string }
> = {
	done: { icon: "check", className: "text-ok" },
	input: { icon: "question", className: "text-accent" },
	error: { icon: "alert", className: "text-err" },
	budget: { icon: "chart", className: "text-warn" },
};

/** 取 kind 的图标；服务端新增了本页不认识的 kind（升级后没刷新）时回落到出错的图标，不让渲染崩掉。 */
export function kindIcon(kind: string): { icon: IconName; className: string } {
	return KIND_ICON[kind as keyof typeof KIND_ICON] ?? KIND_ICON.error;
}

/** 计数胶囊上限：超过显示「99+」。 */
function badgeText(n: number): string {
	return n > 99 ? "99+" : String(n);
}

function Row({
	n,
	onOpen,
}: {
	n: AppNotification;
	onOpen: (n: AppNotification) => void;
}) {
	const { t } = useTranslation();
	const { title, body } = notificationText(n, t);
	const kind = kindIcon(n.kind);
	return (
		<li>
			<button
				type="button"
				onClick={() => onOpen(n)}
				className="flex w-full items-start gap-2.5 rounded-md px-2 py-2 text-left transition-colors duration-150 hover:bg-hover"
			>
				<Icon
					name={kind.icon}
					size={15}
					className={`mt-0.5 shrink-0 ${kind.className}`}
					aria-label={t(`notify.kind.${n.kind}`)}
				/>
				<span className="flex min-w-0 flex-1 flex-col gap-0.5">
					<span className="flex min-w-0 items-center gap-1.5">
						<span
							className={`min-w-0 flex-1 truncate text-sm ${n.readAt ? "text-muted" : "font-medium text-fg"}`}
						>
							{title}
						</span>
						<span className="shrink-0 text-faint text-xs tabular-nums">
							{formatRelative(n.createdAt)}
						</span>
					</span>
					<span className="flex min-w-0 items-center gap-1.5">
						<span className="min-w-0 flex-1 truncate text-faint text-xs">
							{body}
						</span>
						{!n.readAt && (
							<span
								role="img"
								aria-label={t("notify.unread")}
								className="h-[7px] w-[7px] shrink-0 rounded-full bg-accent"
							/>
						)}
					</span>
				</span>
			</button>
		</li>
	);
}

/** 「全部已读」：桌面弹层放在标题行右端；compact 档的对话框放在底栏（对话框自带标题，不再单占一行）。 */
function MarkAllRead(): ReactNode {
	const { t } = useTranslation();
	const unread = useNotifications((s) => s.unread);
	return (
		<Button
			size="sm"
			variant="ghost"
			disabled={unread === 0}
			onClick={() =>
				void notificationsStore
					.getState()
					.markRead()
					.catch(() => {})
			}
		>
			{t("notify.markAllRead")}
		</Button>
	);
}

function Body({ onDone, heading }: { onDone: () => void; heading: boolean }) {
	const { t } = useTranslation();
	const navigate = useNavigate();
	const items = useNotifications((s) => s.items);
	const open = (n: AppNotification) => {
		onDone();
		uiStore.getState().setSidebar(false);
		navigate(n.url);
		if (!n.readAt)
			void notificationsStore
				.getState()
				.markRead(readTargetOf(n))
				.catch(() => {});
	};
	return (
		<>
			{heading && (
				<div className="mb-1 flex h-7 shrink-0 items-center justify-between gap-2 pl-2">
					<h2 className="font-medium text-faint text-xs">
						{t("notify.title")}
					</h2>
					<MarkAllRead />
				</div>
			)}
			{items.length === 0 ? (
				<EmptyState icon="bell" title={t("notify.empty")}>
					{t("notify.emptyHint")}
				</EmptyState>
			) : (
				<ul className="focus-inset -mx-0.5 flex min-h-0 flex-col overflow-y-auto px-0.5">
					{items.map((n) => (
						<Row key={n.id} n={n} onOpen={open} />
					))}
				</ul>
			)}
		</>
	);
}

export function NotificationBell(): ReactNode {
	const { t } = useTranslation();
	const bp = useBreakpoint();
	const compact = bp === "compact";
	const unread = useNotifications((s) => s.unread);
	const hasItems = useNotifications((s) => s.items.length > 0);
	const [open, setOpen] = useState(false);
	const ref = useRef<HTMLDivElement>(null);
	const triggerRef = useRef<HTMLButtonElement>(null);
	const popRef = useRef<HTMLDivElement>(null);
	useLayoutEffect(() => {
		const pop = popRef.current;
		if (open && !compact && pop) (focusablesIn(pop)[0] ?? pop).focus();
	}, [open, compact]);
	useEffect(() => {
		if (!open || compact) return;
		const onDown = (e: MouseEvent) => {
			if (ref.current && !ref.current.contains(e.target as Node))
				setOpen(false);
		};
		const onKey = (e: KeyboardEvent) => {
			if (e.key !== "Escape") return;
			const active = document.activeElement;
			const restore =
				!active ||
				active === document.body ||
				(ref.current?.contains(active) ?? false);
			setOpen(false);
			if (restore) triggerRef.current?.focus();
		};
		document.addEventListener("mousedown", onDown);
		window.addEventListener("keydown", onKey);
		return () => {
			document.removeEventListener("mousedown", onDown);
			window.removeEventListener("keydown", onKey);
		};
	}, [open, compact]);
	const label =
		unread > 0 ? t("notify.bellUnread", { n: unread }) : t("notify.bell");
	return (
		<div ref={ref} className="relative">
			<IconButton
				ref={triggerRef}
				icon="bell"
				box="sm"
				label={label}
				aria-expanded={open}
				aria-haspopup="dialog"
				className={open ? "bg-active text-fg" : ""}
				onClick={() => setOpen((v) => !v)}
			/>
			{unread > 0 && (
				<span
					aria-hidden
					className="pointer-events-none absolute -top-0.5 -right-0.5 inline-flex h-4 min-w-4 items-center justify-center rounded-full bg-accent px-1 font-semibold text-2xs text-accent-fg tabular-nums leading-none"
				>
					{badgeText(unread)}
				</span>
			)}
			{open &&
				(compact ? (
					<Dialog
						open
						title={t("notify.title")}
						onClose={() => setOpen(false)}
						footer={hasItems ? <MarkAllRead /> : undefined}
					>
						<Body onDone={() => setOpen(false)} heading={false} />
					</Dialog>
				) : (
					<Floating
						ref={popRef}
						role="dialog"
						aria-label={t("notify.title")}
						tabIndex={-1}
						bare
						maxHeight={512}
						className="w-80 max-w-[calc(100vw-1rem)] p-1.5 outline-none"
					>
						<Body onDone={() => setOpen(false)} heading />
					</Floating>
				))}
		</div>
	);
}
