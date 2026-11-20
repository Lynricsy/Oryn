/**
 * 手机线程页头（compact）：不是一整条栏，而是浮在转录上的三件玻璃——左侧栏开关（圆钮）、中间标题胶囊、右视图菜单（圆钮，ThreadPage 给）。
 * 标题胶囊两行：标题（标题卡图标 / 线程色）+ 一行状态（Space、项目、orb / runner、定时、公开）。线程的状态再多也只挤在这一行，
 * 页头的按钮不随功能增多；全部操作都在点标题打开的标题菜单里。
 *
 * 标题菜单：顶部三个常用操作（归档、分享、会话树）；然后是带说明的状态行（运行环境、定时、公开链接、关联的 GitHub PR、
 * GitHub 任务状态），只在线程真有这些状态时出现；
 * 最后是其余操作，按「整理 / 派生 / 危险」分组。带子菜单的项在菜单里原地换页（顶部「返回」）。
 * 打开时页头下方铺一层遮罩，标题胶囊留在遮罩之上；点遮罩、按 Esc 或选中一项后关闭，焦点回到标题胶囊。
 *
 * iOS 26+ 的 App 里这三件与两个菜单由原生 Liquid Glass 接管（lib/native-header.ts）：每次渲染把三件的矩形、内容与两棵菜单树
 * 登记成声明式 spec（菜单项 id 是路径，如 `env/freeze`），原生点击回传 id，这里在**最新**的菜单项里找回调执行。
 * 原生显示稳了（`useNativeHeaderActive`）网页三件换成透明（仍可命中：遮挡探测靠它们）；原生页头跟着下沉卡片走，开合途中不换回来。
 * 网页遮罩盖住页头、切到面板标签（`visible` 为假）时原生让位，网页三件当帧回来。网页标题菜单原样保留，让位期间点到的就是它。
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
import type { NativeIcon, NativeMark, NativeSeg } from "../../lib/host";
import {
	avatarHex,
	findMenuItem,
	isDark,
	markHex,
	nativeItems,
	useHeaderCover,
	useHeaderTheme,
	useNativeHeaderActive,
	useNativeHeaderPart,
	useSlotFrame,
} from "../../lib/native-header";
import { usePresence } from "../../lib/presence";
import { parseTitleCard } from "../../lib/title";
import { useThread } from "../../state/stores";
import { uiStore } from "../../state/ui";
import { useSessionCost } from "../composer/ComposerMeta";
import { openPull, prIcon, prStatusText } from "../github/PrChip";
import { ChromeLead, WindowControls } from "../layout/WindowControls";
import { firstGrapheme, nameHue, ProjectBadge, StatusDot } from "../ui/data";
import { Icon } from "../ui/icons";
import { OrbBead, orbVisual } from "../ui/OrbBead";
import type { MenuItem } from "../ui/primitives";
import {
	githubTaskIcon,
	githubTaskText,
	githubTaskUrl,
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
/** `ROUND_GLASS` 的原生接管版：去掉玻璃、整件透明（原生在同一位置画），仍占位、仍可命中。 */
export const ROUND_GLASS_HIDDEN = `${ROUND_GLASS.replace(/^glass /, "")} opacity-0`;

const MENU_MS = 160;

/** 标题菜单里带说明的一行（状态行的第二行是当前状态）。 */
type Row = MenuItem & { sub?: ReactNode };

export function CompactThreadHeader({
	threadId,
	veil,
	safeTop = 0,
	chromeLeft = false,
	chromeRight = false,
	viewMenu,
	visible = true,
}: {
	threadId: string;
	/** 渐进模糊纱的高度（px） */
	veil: number;
	/**
	 * 页面铺到状态栏下面时的顶部安全区（px，ThreadPage 给）：页头往下推这么多；
	 * 此时纱只盖这一带（再 12px 渐隐）：胶囊本身是玻璃，胶囊下面的对话保持清楚，从灵动岛附近才开始渐进模糊。
	 */
	safeTop?: number;
	/** 页头是窗口最左 / 最右侧的标题行（桌面 App 窗口拉窄时）：两端放窗口控件 */
	chromeLeft?: boolean;
	chromeRight?: boolean;
	/** 右上角的视图菜单（`ThreadViewMenu round`） */
	viewMenu?: ReactNode;
	/** 转录纸面是否看得见；切到面板标签时为假，原生页头当帧让位 */
	visible?: boolean;
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
	// 页头在切线程（返回 / 前进）时被复用：标题菜单是上一个线程的，立刻收起（渲染期重置，不留一帧旧菜单）
	const [menuThread, setMenuThread] = useState(threadId);
	if (menuThread !== threadId) {
		setMenuThread(threadId);
		setOpen(false);
	}
	const menu = usePresence(open, MENU_MS);
	const capsuleRef = useRef<HTMLButtonElement>(null);
	const headerRef = useRef<HTMLElement>(null);
	const leadRef = useRef<HTMLButtonElement>(null);
	const probeRef = useRef<HTMLSpanElement>(null);
	const native = useNativeHeaderActive();
	const veilH = safeTop > 0 ? safeTop + 12 : veil;
	const theme = useHeaderTheme(probeRef);
	const dark = theme?.dark ?? isDark();
	const leadFrame = useSlotFrame(leadRef);
	const titleFrame = useSlotFrame(capsuleRef);
	useHeaderCover(headerRef, visible);

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
		// 说明行文字：网页与原生共用同一串
		const envText = `${
			runner
				? orb.state === "ready"
					? t("runner.online")
					: t("runner.offline")
				: env.label
		}${runner && thread?.cwd ? ` · ${shortPath(thread.cwd)}` : ""}`;
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
						{envText}
					</span>
				</span>
			),
			native: {
				sub: {
					text: envText,
					icon: { orb: env.failed ? "err" : orbVisual(orb.state, working) },
					...(env.failed && { tone: "err" as const }),
				},
			},
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
	for (const p of thread?.pulls ?? [])
		status.push({
			key: `pr-${p.number}`,
			icon: prIcon(p),
			label: `#${p.number} ${p.title}`,
			sub: prStatusText(p, t),
			onSelect: () => openPull(p),
		});
	const task = thread?.githubTask;
	if (task) {
		const { subject, state } = githubTaskText(task, t);
		status.push({
			key: "githubTask",
			icon: githubTaskIcon(task),
			label: `${task.repo} ${subject}`,
			sub: state,
			onSelect: () => window.open(githubTaskUrl(task), "_blank", "noopener"),
		});
	}
	// 原生菜单的说明行：网页说明是字符串的原样给原生（运行环境行上面自己给了带状态珠的那份）
	for (const row of status)
		if (typeof row.sub === "string")
			row.native = { ...row.native, sub: { text: row.sub } };

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
		(thread?.pulls?.length ?? 0) > 0 && (
			<Icon key="pr" name="pullRequest" size={11} />
		),
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

	// 原生页头的胶囊第二行：与上面的 meta / marks 一一对应
	const nativeMeta: NativeSeg[] = [];
	if (space) nativeMeta.push({ text: space.name, icon: { symbol: "target" } });
	if (project)
		nativeMeta.push({
			text: project.name,
			icon: {
				glyph: project.icon || firstGrapheme(project.name),
				...avatarHex(nameHue(project.name), dark),
			},
		});
	if (orb && env)
		nativeMeta.push({
			text: runner ? runnerName : env.label,
			icon: { orb: env.failed ? "err" : orbVisual(orb.state, working) },
			...(env.failed && { tone: "err" as const }),
		});
	const nativeMarks: NativeMark[] = [];
	if (schedule)
		nativeMarks.push({
			symbol: "clock",
			...(scheduleLive ? {} : { dim: true }),
		});
	if (scheduleRun) nativeMarks.push({ symbol: "clock.arrow.2.circlepath" });
	if (shares > 0) nativeMarks.push({ symbol: "globe" });
	if ((thread?.pulls?.length ?? 0) > 0)
		nativeMarks.push({ symbol: "arrow.triangle.pull" });
	const titleIcon: NativeIcon | undefined = card.icon
		? { glyph: card.icon, ...avatarHex(hue ?? nameHue(card.text), dark) }
		: hue !== undefined
			? { dot: markHex(hue, dark) }
			: undefined;

	// 原生回传的 id 在本次渲染的菜单项里按路径找（渲染期更新）：不缓存旧闭包，上一个线程的回调不会被执行
	const latestMenu = useRef({ quick, status, groups });
	latestMenu.current = { quick, status, groups };
	const onNativeSelect = (id: string) => {
		const m = latestMenu.current;
		const all: MenuItem[] = [...m.quick, ...m.status, ...m.groups.flat()];
		const item = findMenuItem(all, id);
		if (!item || item.disabled || item.children) return;
		// 带 copy 的项由原生直接写了剪贴板
		if (item.native?.copy === undefined) item.onSelect?.();
	};
	useNativeHeaderPart(
		"theme",
		theme && {
			...theme,
			text: { back: t("common.back"), close: t("common.close") },
		},
	);
	useNativeHeaderPart(
		"lead",
		leadFrame && {
			frame: leadFrame,
			label: t("nav.sidebar"),
			onTap: () => uiStore.getState().toggleSidebar(),
		},
	);
	useNativeHeaderPart(
		"title",
		titleFrame && {
			frame: titleFrame,
			key: threadId,
			text: title,
			icon: titleIcon,
			error: error ?? undefined,
			meta: nativeMeta,
			marks: nativeMarks,
			label: [title, ...nativeMeta.map((s) => s.text)].join(", "),
			menu: {
				label: t("titleMenu.label", { title }),
				dim: true,
				quick: nativeItems(quick),
				status: nativeItems(status),
				groups: groups.filter((g) => g.length > 0).map((g) => nativeItems(g)),
			},
			onSelect: onNativeSelect,
		},
	);

	const close = (refocus: boolean) => {
		setOpen(false);
		if (refocus) capsuleRef.current?.focus({ preventScroll: true });
	};

	return (
		<header
			ref={headerRef}
			data-window-drag=""
			data-testid="compact-thread-header"
			className="veil-host relative isolate flex shrink-0 items-center gap-2 px-3 pb-2"
			style={{ paddingTop: safeTop + 8 }}
		>
			<div
				aria-hidden
				className="veil"
				data-band={safeTop > 0 ? "" : undefined}
				style={{ height: veilH }}
			>
				<i />
				<i />
				<i />
				<i />
				<i />
				<i />
			</div>
			{/* 原生页头的调色板：读这几个元素的计算颜色（useHeaderTheme） */}
			<span ref={probeRef} hidden>
				<i data-k="accent" className="text-accent-ink" />
				<i data-k="brand" style={{ color: "var(--brand)" }} />
				<i data-k="err" className="text-err" />
				<i data-k="warn" className="text-warn" />
				<i data-k="good" style={{ color: "var(--good)" }} />
				<i data-k="frost" style={{ color: "var(--frost)" }} />
			</span>
			<ChromeLead show={chromeLeft} />
			<button
				ref={leadRef}
				type="button"
				data-native-slot="lead"
				aria-label={t("nav.sidebar")}
				title={t("nav.sidebar")}
				aria-hidden={native || undefined}
				tabIndex={native ? -1 : undefined}
				className={native ? ROUND_GLASS_HIDDEN : ROUND_GLASS}
				onClick={() => uiStore.getState().toggleSidebar()}
			>
				<Icon name="sidebar" size={19} />
			</button>
			{menu.mounted && (
				// 遮罩在页头这一层：侧栏钮、视图钮一起压暗，标题胶囊（下面的 z-20 一组）留在遮罩之上
				<button
					type="button"
					aria-label={t("common.close")}
					tabIndex={-1}
					onClick={() => close(false)}
					className={`fixed inset-0 z-10 cursor-default bg-scrim backdrop-blur-[2px] transition-opacity duration-150 ${menu.shown ? "opacity-100" : "opacity-0"}`}
				/>
			)}
			{/* 标题胶囊与它的菜单是一组：菜单挂在胶囊正下方、与胶囊等宽 */}
			<div className={`relative min-w-0 flex-1 ${menu.mounted ? "z-20" : ""}`}>
				<button
					ref={capsuleRef}
					type="button"
					data-testid="thread-title"
					data-native-slot="title"
					aria-haspopup="menu"
					aria-expanded={open}
					aria-hidden={native || undefined}
					tabIndex={native ? -1 : undefined}
					title={title}
					onClick={() => setOpen((v) => !v)}
					className={`${native ? "opacity-0" : "glass"} relative flex h-[46px] w-full flex-col items-center justify-center rounded-full px-4 transition-[scale] duration-150 active:scale-[0.98]`}
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
				{menu.mounted && (
					<TitleMenu
						key={threadId}
						shown={menu.shown}
						label={title}
						quick={quick}
						status={status}
						groups={groups}
						onClose={close}
					/>
				)}
			</div>
			{viewMenu}
			{chromeRight && <WindowControls side="right" className="ml-1" />}
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
	// 只存子菜单的 key，每次渲染从最新的菜单项里取（与共享 Menu 一致）：不缓存带回调的旧对象，
	// 线程状态变化后子菜单的 disabled / 回调也跟着最新
	const [sub, setSub] = useState<string | null>(null);
	const page = sub
		? [...quick, ...status, ...groups.flat()].find(
				(i) => i.key === sub && i.children,
			)
		: undefined;
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
			setSub(item.key);
			return;
		}
		onClose(false);
		// 操作打开的对话框接管焦点；没有对话框的操作（固定、复制链接）失败时错误显示在标题胶囊第二行
		item.onSelect?.();
	};
	const onKeyDown = (e: KeyboardEvent) => {
		if (e.key !== "Escape") return;
		e.stopPropagation();
		if (page) setSub(null);
		else onClose(true);
	};

	return (
		<div
			ref={panelRef}
			role="menu"
			aria-label={t("titleMenu.label", { title: label })}
			data-testid="title-menu"
			onKeyDown={onKeyDown}
			// 与标题胶囊等宽、挂在它正下方；最高到屏幕一半多一点，其余滚动
			className={`title-menu absolute inset-x-0 top-full z-10 mt-1.5 max-h-[min(30rem,calc(100dvh-8rem))] origin-top overflow-y-auto overscroll-contain rounded-[22px] p-1.5 transition-[opacity,scale,translate] duration-150 ease-[var(--ease)] ${shown ? "translate-y-0 scale-100 opacity-100" : "-translate-y-1 scale-95 opacity-0"}`}
		>
			{page ? (
				<>
					<button
						type="button"
						role="menuitem"
						onClick={() => setSub(null)}
						className="flex w-full items-center gap-2 rounded-[12px] px-2.5 py-1.5 text-left text-faint text-xs hover:bg-hover"
					>
						<Icon name="back" size={14} />
						<span className="truncate">{page.label}</span>
					</button>
					<Rule />
					{page.children?.map((item) => (
						<MenuRow key={item.key} item={item} onChoose={choose} />
					))}
				</>
			) : (
				<>
					<div className="grid grid-cols-3 gap-0.5 pb-1">
						{quick.map((item) => (
							<button
								key={item.key}
								type="button"
								role="menuitem"
								data-menu-key={item.key}
								disabled={item.disabled}
								onClick={() => choose(item)}
								className="flex flex-col items-center gap-1 rounded-[14px] px-1 pt-2 pb-1.5 text-[11.5px] text-fg transition-colors duration-150 hover:bg-hover active:bg-active disabled:opacity-40"
							>
								{item.icon && <Icon name={item.icon} size={18} />}
								<span className="max-w-full truncate">{item.label}</span>
							</button>
						))}
					</div>
					{status.length > 0 && (
						// 当前状态自成一块浅底，与下面的「操作」分开扫读
						<div
							data-testid="title-menu-status"
							className="my-1 rounded-[14px] bg-[color-mix(in_oklab,var(--text)_4%,transparent)]"
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
	);
}

function Rule(): ReactNode {
	return <hr className="mx-2.5 my-1 border-[var(--glass-line)]" />;
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
			className={`flex min-h-10 w-full items-center gap-2.5 rounded-[12px] px-2.5 py-1.5 text-left text-sm transition-colors duration-150 hover:bg-hover active:bg-active disabled:opacity-40 ${item.danger ? "text-err" : "text-fg"}`}
		>
			<span
				aria-hidden
				className={`inline-flex w-4 shrink-0 justify-center ${item.danger ? "text-err" : "text-muted"}`}
			>
				{item.leading ?? (item.icon && <Icon name={item.icon} size={16} />)}
			</span>
			<span className="min-w-0 flex-1">
				<span className="block truncate">{item.label}</span>
				{item.sub && (
					<span className="block truncate text-[11px] text-faint leading-4">
						{item.sub}
					</span>
				)}
			</span>
			{item.hint && (
				<span className="shrink-0 text-faint text-xs tabular-nums">
					{item.hint}
				</span>
			)}
			{item.children && (
				<Icon name="chevronRight" size={13} className="shrink-0 text-ghost" />
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
