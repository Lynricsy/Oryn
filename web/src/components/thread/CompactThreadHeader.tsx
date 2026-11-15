/**
 * 手机线程页头（compact）：不是一整条栏，而是浮在转录上的三件玻璃——左侧栏开关（圆钮）、中间标题胶囊、右视图菜单（圆钮，ThreadPage 给）。
 * 标题胶囊两行：标题（标题卡图标 / 线程色）+ 一行状态（Space、项目、orb / runner、定时、公开）。线程的状态再多也只挤在这一行，
 * 页头的按钮不随功能增多；全部操作都在点标题打开的标题菜单里。
 *
 * 标题菜单：顶部三个常用操作（归档、分享、会话树）；然后是带说明的状态行（运行环境、定时、公开链接），只在线程真有这些状态时出现；
 * 最后是其余操作，按「整理 / 派生 / 危险」分组。带子菜单的项在菜单里原地换页（顶部「返回」）。
 * 打开时页头下方铺一层遮罩，标题胶囊留在遮罩之上；点遮罩、按 Esc 或选中一项后关闭，焦点回到标题胶囊。
 */
import {
	type KeyboardEvent,
	type ReactNode,
	useEffect,
	useRef,
	useState,
} from "react";
import { useTranslation } from "react-i18next";
import { useSchedule } from "../../api/schedule-queries";
import { formatCost, formatRelative } from "../../lib/format";
import { usePresence } from "../../lib/presence";
import { parseTitleCard } from "../../lib/title";
import { useThread } from "../../state/stores";
import { uiStore } from "../../state/ui";
import { useSessionCost } from "../composer/ComposerMeta";
import { ChromeLead, WindowControls } from "../layout/WindowControls";
import { nameHue, ProjectBadge, StatusDot } from "../ui/data";
import { Icon } from "../ui/icons";
import { OrbBead } from "../ui/OrbBead";
import type { MenuItem } from "../ui/primitives";
import {
	hueVar,
	orbStatus,
	shortPath,
	threadHue,
	useOrbOf,
	useThreadActions,
	useThreadProject,
	useThreadSpace,
} from "./ThreadHeader";

/** 页头两侧的圆形玻璃钮（侧栏开关、视图菜单），44px 触控尺寸。 */
export const ROUND_GLASS =
	"glass relative inline-grid h-11 w-11 shrink-0 place-items-center rounded-full text-fg transition-[scale] duration-150 active:scale-95";

const MENU_MS = 160;

/** 标题菜单里带说明的一行（状态行的第二行是当前状态）。 */
type Row = MenuItem & { sub?: ReactNode };

export function CompactThreadHeader({
	threadId,
	veil,
	chromeLeft = false,
	chromeRight = false,
	viewMenu,
}: {
	threadId: string;
	/** 渐进模糊纱的高度（px） */
	veil: number;
	/** 页头是窗口最左 / 最右侧的标题行（桌面 App 窗口拉窄时）：两端放窗口控件 */
	chromeLeft?: boolean;
	chromeRight?: boolean;
	/** 右上角的视图菜单（`ThreadViewMenu round`） */
	viewMenu?: ReactNode;
}): ReactNode {
	const { t } = useTranslation();
	const thread = useThread(threadId, (s) => s.thread);
	const orb = useOrbOf(threadId);
	const project = useThreadProject(thread?.projectId);
	const space = useThreadSpace(thread?.spaceId);
	const runSchedule = useSchedule(thread?.scheduleRun?.scheduleId);
	const { cost } = useSessionCost(threadId);
	const { items, error, openSchedule, dialogs } = useThreadActions(threadId);
	const [open, setOpen] = useState(false);
	const menu = usePresence(open, MENU_MS);
	const capsuleRef = useRef<HTMLButtonElement>(null);

	const card = parseTitleCard(thread?.title ?? "");
	const hue = threadHue(thread?.color);
	const title = card.text || t("thread.untitled");
	const working = thread?.state === "working";
	const runner = orb?.kind === "runner";
	const env = orb ? orbStatus(orb, t) : null;
	const runnerName = orb?.providerName ?? t("runner.label");
	const shares = thread?.publicShares ?? 0;
	const schedule = thread?.schedule;
	const scheduleLive = schedule?.enabled && schedule.nextRunAt !== undefined;
	const scheduleRun = thread?.scheduleRun;
	const runName = runSchedule.data?.name || t("sidebar.scheduledRuns");

	const byKey = new Map(items.map((i) => [i.key, i]));
	const pick = (keys: string[]): MenuItem[] =>
		keys.flatMap((k) => {
			const item = byKey.get(k);
			return item ? [item] : [];
		});

	const quick = [
		...pick(["archive", "share"]),
		{
			key: "tree",
			icon: "tree",
			label: t("thread.tree"),
			onSelect: () => uiStore.getState().open({ kind: "tree" }),
		} satisfies MenuItem,
	];

	const status: Row[] = [];
	if (orb && env) {
		const orbItem = byKey.get("orb");
		status.push({
			key: "env",
			icon: runner ? "device" : "box",
			label: runner
				? t("titleMenu.runsOn", { name: runnerName })
				: t("thread.orb"),
			sub: (
				<span className="inline-flex min-w-0 items-center gap-1.5">
					{env.failed ? (
						<StatusDot tone="err" />
					) : (
						<OrbBead state={orb.state} active={working} size={9} />
					)}
					<span className={`truncate ${env.failed ? "text-err" : ""}`}>
						{runner
							? orb.state === "ready"
								? t("runner.online")
								: t("runner.offline")
							: env.label}
						{runner && thread?.cwd ? ` · ${shortPath(thread.cwd)}` : ""}
					</span>
				</span>
			),
			children: [
				{
					key: "info",
					icon: "info",
					label: t("titleMenu.envInfo"),
					onSelect: () => uiStore.getState().setThreadTab("info"),
				},
				// orb 子菜单的第一项是不可点的状态行，这里已经在说明里写了
				...(orbItem?.children ?? []).filter((c) => c.key !== "state"),
			],
		});
	}
	if (schedule)
		status.push({
			key: "scheduled",
			icon: "clock",
			label: t("titleMenu.schedule"),
			sub: scheduleLive
				? t("titleMenu.scheduleNext", {
						when: formatRelative(schedule.nextRunAt ?? Date.now()),
					})
				: t("schedule.paused"),
			onSelect: () => openSchedule({ mode: "thread" }),
		});
	if (scheduleRun)
		status.push({
			key: "scheduleRun",
			icon: "repeat",
			label: t("sidebar.scheduledRuns"),
			sub: `${runName} #${scheduleRun.run}`,
			onSelect: () =>
				openSchedule({ mode: "new_thread", id: scheduleRun.scheduleId }),
		});
	if (shares > 0)
		status.push({
			key: "public",
			icon: "globe",
			label: t("share.header.badge"),
			sub: t("titleMenu.publicLinks", { count: shares }),
			onSelect: () => uiStore.getState().open({ kind: "share" }),
		});

	const costItem = byKey.get("costUsage");
	const groups: MenuItem[][] = [
		pick([
			"rename",
			"labels",
			"color",
			"pin",
			"snooze",
			// 已有定时时由上面的状态行打开
			...(schedule ? [] : ["schedule"]),
		]),
		[
			...pick(["archiveShipped", "fork", "attachments"]),
			...(costItem ? [{ ...costItem, hint: formatCost(cost) }] : []),
			...pick(["link"]),
		],
		pick(["debug", "delete"]),
	];

	// 状态行：线程的状态再多也只挤在胶囊第二行，按 Space / 项目 / 运行环境 / 定时 / 公开 的顺序
	const meta: ReactNode[] = [];
	if (space)
		meta.push(
			<span key="space" className="inline-flex min-w-0 items-center gap-1">
				<Icon name="target" size={11} className="shrink-0 text-faint" />
				<span className="truncate">{space.name}</span>
			</span>,
		);
	if (project)
		meta.push(
			<span key="project" className="inline-flex min-w-0 items-center gap-1">
				<ProjectBadge name={project.name} icon={project.icon} size={13} />
				<span className="truncate">{project.name}</span>
			</span>,
		);
	if (orb && env)
		meta.push(
			<span
				key="env"
				data-testid="title-env"
				className={`inline-flex min-w-0 shrink-0 items-center gap-1 ${env.failed ? "text-err" : ""}`}
			>
				{env.failed ? (
					<StatusDot tone="err" />
				) : (
					<OrbBead state={orb.state} active={working} size={9} />
				)}
				<span className="max-w-24 truncate">
					{runner ? runnerName : env.label}
				</span>
			</span>,
		);
	const marks = [
		schedule && (
			<Icon
				key="schedule"
				name="clock"
				size={11}
				className={scheduleLive ? "" : "text-ghost"}
			/>
		),
		scheduleRun && <Icon key="run" name="repeat" size={11} />,
		shares > 0 && <Icon key="public" name="globe" size={11} />,
	].filter(Boolean);
	if (marks.length > 0)
		meta.push(
			<span
				key="marks"
				aria-hidden
				className="inline-flex shrink-0 items-center gap-1 text-faint"
			>
				{marks}
			</span>,
		);

	const close = (refocus: boolean) => {
		setOpen(false);
		if (refocus) capsuleRef.current?.focus({ preventScroll: true });
	};

	return (
		<header
			data-window-drag=""
			data-testid="compact-thread-header"
			className="veil-host relative isolate flex shrink-0 items-center gap-2 px-3 py-2"
		>
			<div aria-hidden className="veil" style={{ height: veil }}>
				<i />
				<i />
				<i />
				<i />
				<i />
				<i />
			</div>
			{chromeLeft && <ChromeLead />}
			<button
				type="button"
				aria-label={t("nav.sidebar")}
				title={t("nav.sidebar")}
				className={ROUND_GLASS}
				onClick={() => uiStore.getState().toggleSidebar()}
			>
				<Icon name="sidebar" size={19} />
			</button>
			<button
				ref={capsuleRef}
				type="button"
				data-testid="thread-title"
				aria-haspopup="menu"
				aria-expanded={open}
				title={title}
				onClick={() => setOpen((v) => !v)}
				className={`glass relative flex h-[46px] min-w-0 flex-1 flex-col items-center justify-center rounded-full px-4 transition-[scale] duration-150 active:scale-[0.98] ${open ? "z-20" : ""}`}
			>
				<span className="flex max-w-full items-center gap-1.5 font-semibold text-[15px] leading-5 tracking-tight">
					{card.icon ? (
						<span
							aria-hidden
							data-title-glyph="card"
							className="avatar inline-flex h-4 w-4 shrink-0 items-center justify-center rounded-xs text-[11px] leading-none"
							style={hueVar(hue ?? nameHue(card.text))}
						>
							{card.icon}
						</span>
					) : (
						hue !== undefined && (
							<span
								aria-hidden
								className="thread-mark h-2 w-2 shrink-0 rounded-full"
								style={hueVar(hue)}
							/>
						)
					)}
					<span className="min-w-0 truncate">{title}</span>
					{/* 展开提示：标题胶囊是菜单入口，不只是标题牌 */}
					<Icon
						name="chevronDown"
						size={12}
						weight="bold"
						aria-hidden
						className={`shrink-0 text-faint transition-transform duration-150 ${open ? "rotate-180" : ""}`}
					/>
				</span>
				{error ? (
					<span
						role="alert"
						className="max-w-full truncate text-[11.5px] text-err leading-4"
					>
						{error}
					</span>
				) : (
					meta.length > 0 && (
						// 段间的「·」由 ::before 画（第一段之外），各段自己决定能不能被挤窄
						<span
							data-testid="title-meta"
							className="flex max-w-full items-center gap-1.5 overflow-hidden text-[11.5px] text-muted leading-4 [&>*+*]:before:mr-0.5 [&>*+*]:before:text-ghost [&>*+*]:before:content-['·']"
						>
							{meta}
						</span>
					)
				)}
			</button>
			{viewMenu}
			{chromeRight && <WindowControls side="right" className="ml-1" />}
			{menu.mounted && (
				<TitleMenu
					shown={menu.shown}
					label={title}
					quick={quick}
					status={status}
					groups={groups}
					onClose={close}
				/>
			)}
			{dialogs}
		</header>
	);
}

function TitleMenu({
	shown,
	label,
	quick,
	status,
	groups,
	onClose,
}: {
	shown: boolean;
	label: string;
	quick: MenuItem[];
	status: Row[];
	groups: MenuItem[][];
	onClose: (refocus: boolean) => void;
}): ReactNode {
	const { t } = useTranslation();
	const [sub, setSub] = useState<Row | null>(null);
	const panelRef = useRef<HTMLDivElement>(null);

	// 打开 / 换页后焦点给第一项（键盘与读屏从菜单里接着走）
	// biome-ignore lint/correctness/useExhaustiveDependencies: 换页（sub）时重新聚焦
	useEffect(() => {
		if (!shown) return;
		panelRef.current
			?.querySelector<HTMLElement>("[role=menuitem]:not(:disabled)")
			?.focus({ preventScroll: true });
	}, [shown, sub]);

	const choose = (item: Row) => {
		if (item.children) {
			setSub(item);
			return;
		}
		onClose(false);
		// 操作打开的对话框接管焦点；没有对话框的操作（固定、复制链接）失败时错误显示在标题胶囊第二行
		item.onSelect?.();
	};
	const onKeyDown = (e: KeyboardEvent) => {
		if (e.key !== "Escape") return;
		e.stopPropagation();
		if (sub) setSub(null);
		else onClose(true);
	};

	return (
		<>
			<button
				type="button"
				aria-label={t("common.close")}
				tabIndex={-1}
				data-shown={shown ? "" : undefined}
				onClick={() => onClose(false)}
				className={`fixed inset-0 z-10 cursor-default bg-scrim backdrop-blur-[2px] transition-opacity duration-150 ${shown ? "opacity-100" : "opacity-0"}`}
			/>
			<div
				ref={panelRef}
				role="menu"
				aria-label={t("titleMenu.label", { title: label })}
				data-testid="title-menu"
				onKeyDown={onKeyDown}
				className={`title-menu absolute inset-x-3 top-[calc(100%-2px)] z-30 mx-auto max-h-[calc(100dvh-7rem)] max-w-[22rem] origin-top overflow-y-auto overscroll-contain rounded-[26px] p-2 transition-[opacity,scale,translate] duration-150 ease-[var(--ease)] ${shown ? "translate-y-0 scale-100 opacity-100" : "-translate-y-1 scale-95 opacity-0"}`}
			>
				{sub ? (
					<>
						<button
							type="button"
							role="menuitem"
							onClick={() => setSub(null)}
							className="flex w-full items-center gap-2 rounded-[14px] px-3 py-2 text-left text-faint text-sm hover:bg-hover"
						>
							<Icon name="back" size={15} />
							<span className="truncate">{sub.label}</span>
						</button>
						<Rule />
						{sub.children?.map((item) => (
							<MenuRow key={item.key} item={item} onChoose={choose} />
						))}
					</>
				) : (
					<>
						<div className="grid grid-cols-3 gap-1 px-0.5 pt-0.5 pb-1.5">
							{quick.map((item) => (
								<button
									key={item.key}
									type="button"
									role="menuitem"
									data-menu-key={item.key}
									disabled={item.disabled}
									onClick={() => choose(item)}
									className="flex flex-col items-center gap-1.5 rounded-2xl px-1 pt-3 pb-2.5 text-fg text-xs transition-colors duration-150 hover:bg-hover active:bg-active disabled:opacity-40"
								>
									{item.icon && <Icon name={item.icon} size={21} />}
									<span className="max-w-full truncate">{item.label}</span>
								</button>
							))}
						</div>
						{status.length > 0 && (
							// 当前状态自成一块浅底，与下面的「操作」分开扫读
							<div
								data-testid="title-menu-status"
								className="mx-0.5 my-1 rounded-[18px] bg-[color-mix(in_oklab,var(--text)_4%,transparent)] p-0.5"
							>
								{status.map((item) => (
									<MenuRow key={item.key} item={item} onChoose={choose} />
								))}
							</div>
						)}
						{groups
							.filter((g) => g.length > 0)
							.map((group) => (
								<div key={group[0]?.key}>
									<Rule />
									{group.map((item) => (
										<MenuRow key={item.key} item={item} onChoose={choose} />
									))}
								</div>
							))}
					</>
				)}
			</div>
		</>
	);
}

function Rule(): ReactNode {
	return <hr className="mx-3 my-1 border-[var(--glass-line)]" />;
}

function MenuRow({
	item,
	onChoose,
}: {
	item: Row;
	onChoose: (item: Row) => void;
}): ReactNode {
	return (
		<button
			type="button"
			{...item.attrs}
			{...(item.checked === undefined
				? { role: "menuitem" }
				: { role: "menuitemradio", "aria-checked": item.checked })}
			data-menu-key={item.key}
			disabled={item.disabled}
			onClick={() => onChoose(item)}
			className={`flex min-h-11 w-full items-center gap-3.5 rounded-[14px] px-3 py-2 text-left text-[15px] transition-colors duration-150 hover:bg-hover active:bg-active disabled:opacity-40 ${item.danger ? "text-err" : "text-fg"}`}
		>
			<span
				aria-hidden
				className={`inline-flex w-5 shrink-0 justify-center ${item.danger ? "text-err" : "text-muted"}`}
			>
				{item.leading ?? (item.icon && <Icon name={item.icon} size={18} />)}
			</span>
			<span className="min-w-0 flex-1">
				<span className="block truncate">{item.label}</span>
				{item.sub && (
					<span className="mt-0.5 block truncate text-faint text-xs">
						{item.sub}
					</span>
				)}
			</span>
			{item.hint && (
				<span className="shrink-0 text-faint text-[13px] tabular-nums">
					{item.hint}
				</span>
			)}
			{item.children && (
				<Icon name="chevronRight" size={14} className="shrink-0 text-ghost" />
			)}
			{item.checked && (
				<Icon
					name="check"
					size={14}
					weight="bold"
					className="shrink-0 text-accent-ink"
				/>
			)}
		</button>
	);
}
