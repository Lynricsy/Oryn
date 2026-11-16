/**
 * Artifacts 总览页（计划 M14 2.5）：`/artifacts` 与 `/artifacts/trash`（最近删除）。版式参照 Claude 的 Artifacts 页：
 * 大标题，下面一行左侧类型标签、右侧图标按钮（搜索 / 筛选 / 显示 / 导入），再下面是大卡片网格（2:1 预览区里露出上半截的
 * 页面缩略图）。筛选全部进 URL 查询串（`artifacts-model.ts` 的 `parseView` / `viewSearch`），刷新后视图不变。
 * - 工具条：类型标签、筛选（项目多选含「无项目」、来源多选）、显示（三种排序、两种布局）、标题搜索（点放大镜或 `Mod+F`
 *   展开并聚焦，有搜索词时保持展开；`Esc` 在有筛选时清除）、导入（HTML 文件 / 带 index.html 的文件夹）。`compact`
 *   断点下（9.2）类型标签独占一行，筛选、显示与导入收进一个「筛选与显示」菜单，搜索框常驻；网格 2 列（360 一档 1 列）。
 * - 卡片网格是一个 Tab 停靠点：方向键按阅读顺序移动，`Home` / `End` 跳首尾；右键或 `Shift+F10` 打开卡片菜单；
 *   从查看器返回时恢复滚动位置并聚焦刚才那张。
 * - 空态：没有任何 artifact 时四个「让 Agent 做一份」磁贴（把一行提示交给首屏输入框）；筛不出结果时「清除筛选」。
 * - 最近删除：卡片显示「剩 N 天」与恢复；`Esc` 返回总览。
 */
import {
	type KeyboardEvent as ReactKeyboardEvent,
	type ReactNode,
	type RefObject,
	useEffect,
	useLayoutEffect,
	useRef,
	useState,
} from "react";
import { useTranslation } from "react-i18next";
import { Link, useLocation, useNavigate, useSearchParams } from "react-router";
import {
	type Artifact,
	useArtifacts,
	useUndeleteArtifact,
} from "../api/artifact-queries";
import { useProjects } from "../api/queries";
import { ArtifactCard } from "../components/artifacts/ArtifactCard";
import { ImportDialog } from "../components/artifacts/ArtifactDialogs";
import { ArtifactMenu } from "../components/artifacts/ArtifactMenu";
import {
	type ArtifactsView,
	clearFilters,
	filterArtifacts,
	flattenGroups,
	gridMove,
	groupArtifacts,
	hasFilters,
	KIND_TABS,
	LAYOUT_KEYS,
	NO_PROJECT,
	ORIGIN_KEYS,
	type ProjectSection,
	parseView,
	SORT_KEYS,
	sortArtifacts,
	viewSearch,
} from "../components/artifacts/artifacts-model";
import {
	daysLeft,
	KEEP_DELETED_DAYS,
} from "../components/artifacts/viewer-states";
import {
	ChromeLead,
	WindowControls,
} from "../components/layout/WindowControls";
import { Callout, Sheet } from "../components/ui/data";
import { Icon, type IconName } from "../components/ui/icons";
import { Popover, PopoverLabel } from "../components/ui/PickerPopover";
import {
	Button,
	describeError,
	EmptyState,
	IconButton,
	Menu,
	tabClass,
} from "../components/ui/primitives";
import { ARTIFACT_KINDS, kindIcon, kindLabelKey } from "../lib/artifacts/kinds";
import { useBreakpoint } from "../lib/viewport";
import { useIndex } from "../state/stores";
import { showToast } from "../state/toasts";
import { uiStore, useSidebarShown, useSidebarToggleShown } from "../state/ui";

/** 从查看器返回时要恢复的位置（同一个地址才恢复；用过即清）。 */
let returnTo: { path: string; scrollTop: number; id: string } | null = null;

/** 浏览器能选文件夹（`<input webkitdirectory>`）；不支持时隐藏「文件夹」一项。 */
function supportsFolderPick(): boolean {
	return (
		typeof document !== "undefined" &&
		"webkitdirectory" in document.createElement("input")
	);
}

/** 网格里全部卡片按钮（阅读顺序）。 */
function cardsIn(root: HTMLElement | null): HTMLButtonElement[] {
	return [
		...(root?.querySelectorAll<HTMLButtonElement>(
			"[data-testid=artifact-card]",
		) ?? []),
	];
}

/** 焦点在输入框、或有对话框 / 菜单打开时，`Esc` 不归页面。 */
function escOwnedElsewhere(e: KeyboardEvent): boolean {
	if (document.querySelector("[role=dialog], [role=menu]")) return true;
	return e.defaultPrevented;
}

export function ArtifactsPage({
	trash = false,
}: {
	trash?: boolean;
}): ReactNode {
	return trash ? <TrashScreen /> : <OverviewScreen />;
}

function TitleBar({ trash }: { trash: boolean }): ReactNode {
	const { t } = useTranslation();
	const bp = useBreakpoint();
	const navigate = useNavigate();
	const sidebarToggle = useSidebarToggleShown();
	const chromeLeft = !useSidebarShown();
	// 总览页的标题是正文里的大标题，这一条只留窗口控件、侧栏开关（可拖动窗口）；回收站保留面包屑
	return (
		<header
			data-window-drag=""
			className={`flex h-12 shrink-0 items-center gap-2 px-3 ${trash ? "border-border border-b" : ""}`}
		>
			{chromeLeft && <ChromeLead />}
			{sidebarToggle && (
				<IconButton
					icon={bp === "expanded" ? "sidebar" : "menu"}
					label={t("nav.sidebar")}
					onClick={() => uiStore.getState().toggleSidebar()}
				/>
			)}
			{trash && (
				<>
					<IconButton
						icon="back"
						label={t("artifacts.page.back")}
						onClick={() => navigate("/artifacts")}
					/>
					<Icon name="shapes" size={16} className="shrink-0 text-faint" />
					<h1 className="flex min-w-0 items-center gap-2 font-semibold text-sm">
						<Link to="/artifacts" className="shrink-0 hover:underline">
							{t("artifacts.title")}
						</Link>
						<span aria-hidden className="text-ghost">
							/
						</span>
						<span className="truncate">{t("artifacts.page.trash")}</span>
					</h1>
				</>
			)}
			<span className="min-w-0 flex-1" />
			<WindowControls side="right" className="-mr-1" />
		</header>
	);
}

/**
 * 卡片网格的列数：手机 2 列（视口窄于 380，即 360 一档，1 列），平板 2 列，`lg` 起 3 列（卡片要够大，预览才看得清）。
 * 断点按视口宽度（同 `useBreakpoint`）。
 */
const GRID_CLASS =
	"grid grid-cols-1 gap-3 min-[380px]:grid-cols-2 sm:gap-5 lg:grid-cols-3";

/** 骨架卡：与 tile 同形的占位（加载中）。 */
function SkeletonGrid(): ReactNode {
	return (
		<div role="status" data-testid="artifacts-loading" className={GRID_CLASS}>
			{[0, 1, 2].map((i) => (
				<div
					key={i}
					className="flex animate-pulse flex-col overflow-hidden rounded-xl border border-border bg-panel"
				>
					<span className="aspect-[2/1] w-full bg-inset" />
					<span className="flex flex-col gap-1.5 px-4 pt-3 pb-3.5">
						<span className="h-3.5 w-3/4 rounded bg-inset" />
						<span className="h-3 w-1/3 rounded bg-inset" />
					</span>
				</div>
			))}
		</div>
	);
}

/**
 * 一张卡片 + 浮在右上角的更多菜单（菜单按钮不是 Tab 停靠点；右键与 `Shift+F10` 打开它）。`action` 浮在右下角
 * （回收站的「恢复」），与卡片按钮是兄弟元素，不嵌套交互元素。
 */
function CardCell({
	artifact,
	index,
	focusIdx,
	trash,
	onOpen,
	onFocusIdx,
	onKey,
	meta,
	action,
}: {
	artifact: Artifact;
	index: number;
	focusIdx: number;
	trash: boolean;
	onOpen(id: string): void;
	onFocusIdx(i: number): void;
	onKey(e: ReactKeyboardEvent<HTMLButtonElement>, index: number): void;
	meta?: ReactNode;
	action?: ReactNode;
}): ReactNode {
	const openMenu = useRef<(() => void) | null>(null);
	// 菜单弹层在右上角按钮的容器里（带 backdrop-blur，自成层叠上下文），盖不过后面的卡片：菜单开着时把整格抬上来
	return (
		<li className="group/cell relative min-w-0 [&:has([aria-expanded=true])]:z-20">
			<ArtifactCard
				artifact={artifact}
				form="tile"
				meta={meta}
				tabIndex={index === focusIdx ? 0 : -1}
				onOpen={onOpen}
				onFocus={() => onFocusIdx(index)}
				onKeyDown={(e) => {
					if (e.key === "ContextMenu" || (e.shiftKey && e.key === "F10")) {
						e.preventDefault();
						openMenu.current?.();
						return;
					}
					onKey(e, index);
				}}
				onContextMenu={(e) => {
					e.preventDefault();
					openMenu.current?.();
				}}
			/>
			<div className="absolute top-2 right-2 rounded-md bg-panel/80 opacity-100 backdrop-blur-sm sm:opacity-0 sm:focus-within:opacity-100 sm:group-hover/cell:opacity-100 [&:has([aria-expanded=true])]:opacity-100">
				<ArtifactMenu
					artifact={artifact}
					place={trash ? "trash" : "card"}
					openRef={openMenu}
					tabIndex={-1}
				/>
			</div>
			{action && <div className="absolute right-2 bottom-2">{action}</div>}
		</li>
	);
}

/**
 * 卡片网格（平铺或分组）：漫游 tabIndex，一个 Tab 停靠点。`renderMeta` / `renderAction` 给回收站换掉说明行、加「恢复」。
 * 打开卡片前记下滚动位置，返回时恢复并聚焦它。
 */
function CardGrid({
	flat,
	groups,
	trash,
	scrollRef,
	renderMeta,
	renderAction,
	renderGroupHead,
}: {
	flat: Artifact[];
	groups?: ProjectSection[];
	trash: boolean;
	scrollRef: RefObject<HTMLDivElement | null>;
	renderMeta?(a: Artifact): ReactNode;
	renderAction?(a: Artifact): ReactNode;
	renderGroupHead?(g: ProjectSection): ReactNode;
}): ReactNode {
	const { t } = useTranslation();
	const navigate = useNavigate();
	const location = useLocation();
	const index = useIndex((s) => s.threads);
	const rootRef = useRef<HTMLDivElement>(null);
	const [focusIdx, setFocusIdx] = useState(0);
	const clamped = Math.min(focusIdx, Math.max(0, flat.length - 1));
	const path = location.pathname + location.search;

	// 从查看器返回：恢复滚动并聚焦刚才那张
	useLayoutEffect(() => {
		if (!returnTo || returnTo.path !== path || flat.length === 0) return;
		const target = returnTo;
		returnTo = null;
		const i = flat.findIndex((a) => a.id === target.id);
		if (scrollRef.current) scrollRef.current.scrollTop = target.scrollTop;
		if (i < 0) return;
		setFocusIdx(i);
		cardsIn(rootRef.current)[i]?.focus({ preventScroll: true });
	}, [path, flat, scrollRef]);

	const open = (id: string) => {
		returnTo = {
			path,
			scrollTop: scrollRef.current?.scrollTop ?? 0,
			id,
		};
		navigate(`/artifacts/${id}`);
	};

	const onKey = (e: ReactKeyboardEvent<HTMLButtonElement>, i: number) => {
		const all = cardsIn(rootRef.current);
		// 列数 = 本网格第一行的卡片数（与第一张同一个顶边）
		const grid = e.currentTarget.closest("ul");
		const tops = grid
			? [
					...grid.querySelectorAll<HTMLElement>("[data-testid=artifact-card]"),
				].map((c) => c.getBoundingClientRect().top)
			: [];
		const columns = Math.max(1, tops.filter((y) => y === tops[0]).length);
		const next = gridMove(i, e.key, all.length, columns);
		if (next === null) return;
		e.preventDefault();
		setFocusIdx(next);
		all[next]?.focus();
	};

	const cell = (a: Artifact, i: number) => (
		<CardCell
			key={a.id}
			artifact={a}
			index={i}
			focusIdx={clamped}
			trash={trash}
			onOpen={open}
			onFocusIdx={setFocusIdx}
			onKey={onKey}
			meta={renderMeta?.(a)}
			action={renderAction?.(a)}
		/>
	);
	if (!groups)
		return (
			<div ref={rootRef}>
				<ul
					aria-label={t("artifacts.page.grid")}
					data-testid="artifacts-grid"
					className={GRID_CLASS}
				>
					{flat.map(cell)}
				</ul>
			</div>
		);

	let i = 0;
	return (
		<div ref={rootRef} className="flex flex-col gap-6">
			{groups.map((g) => (
				<section
					key={g.key}
					data-testid="artifacts-group"
					className="flex flex-col gap-3"
				>
					{renderGroupHead?.(g)}
					{g.sections.map((s) => {
						const title =
							s.type === "imported"
								? t("artifacts.page.imported")
								: s.type === "deletedThread"
									? t("artifacts.page.deletedThread")
									: (index[s.threadId ?? ""]?.title ??
										t("artifacts.page.unknownThread"));
						return (
							<div key={s.key} className="flex flex-col gap-2">
								<h3
									data-testid="artifacts-section"
									className="flex min-w-0 items-center gap-1.5 text-muted text-xs"
								>
									<Icon
										name={
											s.type === "thread"
												? "message"
												: s.type === "imported"
													? "upload"
													: "trash"
										}
										size={12}
										className="shrink-0 text-faint"
									/>
									{s.type === "thread" && s.threadId ? (
										<Link
											to={`/t/${s.threadId}`}
											className="truncate hover:text-fg hover:underline"
										>
											{title}
										</Link>
									) : (
										<span className="truncate">{title}</span>
									)}
								</h3>
								<ul aria-label={title} className={GRID_CLASS}>
									{s.items.map((a) => cell(a, i++))}
								</ul>
							</div>
						);
					})}
				</section>
			))}
		</div>
	);
}

/** 筛选弹层与显示弹层里的一行：可勾选的按钮（不关弹层）。 */
function ChoiceRow({
	selected,
	onClick,
	children,
	icon,
	testId,
}: {
	selected: boolean;
	onClick(): void;
	children: ReactNode;
	icon?: IconName;
	testId?: string;
}): ReactNode {
	return (
		<button
			type="button"
			aria-pressed={selected}
			data-testid={testId}
			onClick={onClick}
			className={`flex w-full items-center gap-2 rounded-md px-2 py-1.5 text-left text-sm text-fg transition-colors duration-150 hover:bg-hover ${selected ? "bg-hover" : ""}`}
		>
			{icon && <Icon name={icon} size={14} className="shrink-0 text-faint" />}
			<span className="min-w-0 flex-1 truncate">{children}</span>
			{selected && (
				<Icon name="check" size={13} className="shrink-0 text-muted" />
			)}
		</button>
	);
}

/** 手机菜单里的一行动作（导入）：点了关弹层再执行。 */
function ActionRow({
	onClick,
	children,
	icon,
	testId,
}: {
	onClick(): void;
	children: ReactNode;
	icon: IconName;
	testId: string;
}): ReactNode {
	return (
		<button
			type="button"
			data-testid={testId}
			onClick={onClick}
			className="flex w-full items-center gap-2 rounded-md px-2 py-1.5 text-left text-sm text-fg transition-colors duration-150 hover:bg-hover"
		>
			<Icon name={icon} size={14} className="shrink-0 text-faint" />
			<span className="min-w-0 flex-1 truncate">{children}</span>
		</button>
	);
}

function toggled<T>(list: readonly T[], x: T): T[] {
	return list.includes(x) ? list.filter((y) => y !== x) : [...list, x];
}

function OverviewScreen(): ReactNode {
	const { t } = useTranslation();
	const bp = useBreakpoint();
	const navigate = useNavigate();
	const [sp, setSp] = useSearchParams();
	const view = parseView(sp);
	const list = useArtifacts();
	const trashList = useArtifacts({ deleted: true });
	const projects = useProjects();
	const scrollRef = useRef<HTMLDivElement>(null);
	const searchRef = useRef<HTMLInputElement>(null);
	const fileRef = useRef<HTMLInputElement>(null);
	const folderRef = useRef<HTMLInputElement>(null);
	const [pop, setPop] = useState<"filter" | "display" | "options" | null>(null);
	const compact = bp === "compact";
	const [importing, setImporting] = useState<File[] | null>(null);
	const [folderPick] = useState(supportsFolderPick);
	// 宽屏的搜索框平时收成放大镜；有搜索词时保持展开。`focusReq` 每加一就在展开后聚焦并全选一次
	const [searchOpen, setSearchOpen] = useState(false);
	const [focusReq, setFocusReq] = useState(0);
	const searchShown = compact || searchOpen || view.q !== "";
	const openSearch = () => {
		setSearchOpen(true);
		setFocusReq((n) => n + 1);
	};
	useEffect(() => {
		if (focusReq === 0) return;
		searchRef.current?.focus();
		searchRef.current?.select();
	}, [focusReq]);

	const setView = (next: ArtifactsView) =>
		setSp(new URLSearchParams(viewSearch(next)), { replace: true });
	const viewRef = useRef(view);
	viewRef.current = view;

	useEffect(() => {
		const onKey = (e: KeyboardEvent) => {
			if ((e.ctrlKey || e.metaKey) && e.key.toLowerCase() === "f") {
				e.preventDefault();
				setSearchOpen(true);
				setFocusReq((n) => n + 1);
				return;
			}
			if (e.key !== "Escape" || escOwnedElsewhere(e)) return;
			const v = viewRef.current;
			if (!hasFilters(v)) return;
			e.preventDefault();
			setSp(new URLSearchParams(viewSearch(clearFilters(v))), {
				replace: true,
			});
		};
		window.addEventListener("keydown", onKey);
		return () => window.removeEventListener("keydown", onKey);
	}, [setSp]);

	const projectList = projects.data?.projects ?? [];
	const projectName = (id: string | null) =>
		id === null || id === NO_PROJECT
			? t("artifacts.page.noProject")
			: (projectList.find((p) => p.id === id)?.name ?? id);

	const all = list.data ?? [];
	const shown = sortArtifacts(filterArtifacts(all, view), view.sort);
	const groups =
		view.layout === "project-thread" ? groupArtifacts(shown) : undefined;
	const flat = groups ? flattenGroups(groups) : shown;
	const deletedCount = trashList.data?.length ?? 0;
	const filterCount = view.projects.length + view.origins.length;
	// 筛选里列出的项目：已有项目 + 列表里出现过的（项目已删除也能筛）+ 已选中的
	const projectIds = [
		...new Set([
			...projectList.map((p) => p.id),
			...all.flatMap((a) => (a.projectId ? [a.projectId] : [])),
			...view.projects.filter((p) => p !== NO_PROJECT),
		]),
	];

	const pickFiles = (files: FileList | null) => {
		if (files && files.length > 0) setImporting([...files]);
	};

	const filterChoices = (
		<>
			<PopoverLabel>{t("artifacts.page.projects")}</PopoverLabel>
			<ChoiceRow
				selected={view.projects.includes(NO_PROJECT)}
				icon="folder"
				onClick={() =>
					setView({
						...view,
						projects: toggled(view.projects, NO_PROJECT),
					})
				}
			>
				{t("artifacts.page.noProject")}
			</ChoiceRow>
			{projectIds.map((id) => (
				<ChoiceRow
					key={id}
					selected={view.projects.includes(id)}
					icon="folder"
					onClick={() =>
						setView({
							...view,
							projects: toggled(view.projects, id),
						})
					}
				>
					{projectName(id)}
				</ChoiceRow>
			))}
			<PopoverLabel>{t("artifacts.page.origins")}</PopoverLabel>
			{ORIGIN_KEYS.map((o) => (
				<ChoiceRow
					key={o}
					selected={view.origins.includes(o)}
					icon={o === "agent" ? "message" : "upload"}
					testId={`origin-${o}`}
					onClick={() =>
						setView({ ...view, origins: toggled(view.origins, o) })
					}
				>
					{t(`artifacts.page.origin.${o}`)}
				</ChoiceRow>
			))}
		</>
	);
	const displayChoices = (
		<>
			<PopoverLabel>{t("artifacts.page.sortBy")}</PopoverLabel>
			{SORT_KEYS.map((s) => (
				<ChoiceRow
					key={s}
					selected={view.sort === s}
					testId={`sort-${s}`}
					onClick={() => setView({ ...view, sort: s })}
				>
					{t(`artifacts.page.sort.${s}`)}
				</ChoiceRow>
			))}
			<PopoverLabel>{t("artifacts.page.layoutBy")}</PopoverLabel>
			{LAYOUT_KEYS.map((l) => (
				<ChoiceRow
					key={l}
					selected={view.layout === l}
					testId={`layout-${l}`}
					onClick={() => setView({ ...view, layout: l })}
				>
					{t(`artifacts.page.layout.${l}`)}
				</ChoiceRow>
			))}
		</>
	);

	let content: ReactNode;
	if (list.isPending) content = <SkeletonGrid />;
	else if (list.isError)
		content = (
			<EmptyState icon="alert" title={t("artifacts.page.failed")}>
				<div className="flex flex-col items-center gap-3">
					<span className="break-words">{describeError(list.error, t)}</span>
					<Button size="sm" icon="refresh" onClick={() => void list.refetch()}>
						{t("artifacts.viewer.retry")}
					</Button>
				</div>
			</EmptyState>
		);
	else if (all.length === 0)
		content = (
			<div
				data-testid="artifacts-empty"
				className="flex flex-col items-center gap-5 py-10"
			>
				<EmptyState icon="shapes" title={t("artifacts.page.empty")}>
					{t("artifacts.page.emptyHint")}
				</EmptyState>
				<ul className="grid w-full max-w-3xl grid-cols-2 gap-3 sm:grid-cols-4">
					{ARTIFACT_KINDS.map((k) => (
						<li key={k}>
							<button
								type="button"
								data-seed={k}
								onClick={() => {
									uiStore
										.getState()
										.seedHome({ text: t(`artifacts.page.seeds.${k}`) });
									navigate("/");
								}}
								className="flex w-full flex-col items-center gap-2 rounded-lg border border-border bg-panel px-3 py-5 text-sm transition-[background-color,border-color] duration-150 hover:border-border-strong hover:bg-hover"
							>
								<Icon name={kindIcon(k)} size={24} className="text-faint" />
								<span className="font-medium">
									{t("artifacts.page.seedTile", { kind: t(kindLabelKey(k)) })}
								</span>
							</button>
						</li>
					))}
				</ul>
			</div>
		);
	else if (flat.length === 0)
		content = (
			<EmptyState icon="search" title={t("artifacts.page.noMatch")}>
				<Button size="sm" onClick={() => setView(clearFilters(view))}>
					{t("artifacts.page.clearFilters")}
				</Button>
			</EmptyState>
		);
	else
		content = (
			<CardGrid
				flat={flat}
				groups={groups}
				trash={false}
				scrollRef={scrollRef}
				renderGroupHead={(g) => (
					<h2
						data-testid="artifacts-group-head"
						className="flex items-center gap-2 font-semibold text-fg text-sm"
					>
						<Icon name="folder" size={14} className="shrink-0 text-faint" />
						<span className="truncate">{projectName(g.projectId)}</span>
					</h2>
				)}
			/>
		);

	return (
		<Sheet inset={bp === "expanded"} className="flex h-full min-h-0 flex-col">
			<TitleBar trash={false} />
			<div
				ref={scrollRef}
				data-testid="artifacts-scroll"
				className="min-h-0 flex-1 overflow-y-auto overflow-x-hidden px-3 pt-2 pb-safe sm:px-8 sm:pt-6"
			>
				<div className="mx-auto flex min-w-0 max-w-6xl flex-col gap-5 pb-6 sm:gap-6">
					<h1 className="font-semibold text-2xl text-fg tracking-tight sm:text-3xl">
						{t("artifacts.title")}
					</h1>
					<div
						role="toolbar"
						aria-label={t("artifacts.page.toolbar")}
						className="flex min-w-0 flex-wrap items-center gap-2"
					>
						<nav
							aria-label={t("artifacts.page.kinds")}
							className={`flex min-w-0 items-center gap-1 overflow-x-auto ${compact ? "w-full" : ""}`}
						>
							{KIND_TABS.map((k) => (
								<button
									key={k}
									type="button"
									data-kind={k}
									aria-pressed={view.kind === k}
									className={`${tabClass(view.kind === k)} h-8 rounded-lg px-3 text-sm ${view.kind === k ? "bg-hover" : ""}`}
									onClick={() => setView({ ...view, kind: k })}
								>
									{k === "all" ? t("artifacts.page.all") : t(kindLabelKey(k))}
								</button>
							))}
						</nav>
						{compact ? (
							// 手机：筛选、显示与导入收进一个菜单
							<div className="relative">
								<Button
									size="sm"
									icon="sliders"
									aria-expanded={pop === "options"}
									data-testid="artifacts-options"
									onClick={() => setPop(pop === "options" ? null : "options")}
								>
									{filterCount > 0
										? t("artifacts.page.optionsN", { n: filterCount })
										: t("artifacts.page.options")}
								</Button>
								{pop === "options" && (
									<Popover side="bottom" onClose={() => setPop(null)}>
										{filterChoices}
										{displayChoices}
										<PopoverLabel>{t("artifacts.import.button")}</PopoverLabel>
										<ActionRow
											icon="fileCode"
											testId="artifacts-import-file"
											onClick={() => {
												setPop(null);
												fileRef.current?.click();
											}}
										>
											{t("artifacts.import.file")}
										</ActionRow>
										{folderPick && (
											<ActionRow
												icon="folder"
												testId="artifacts-import-folder"
												onClick={() => {
													setPop(null);
													folderRef.current?.click();
												}}
											>
												{t("artifacts.import.folder")}
											</ActionRow>
										)}
									</Popover>
								)}
							</div>
						) : (
							<span className="min-w-0 flex-1" />
						)}
						{searchShown ? (
							<label
								className={`relative flex min-w-0 items-center ${compact ? "flex-1" : "w-60"}`}
							>
								<Icon
									name="search"
									size={14}
									className="pointer-events-none absolute left-2.5 text-faint"
								/>
								<input
									ref={searchRef}
									type="search"
									value={view.q}
									aria-label={t("artifacts.page.search")}
									placeholder={t("artifacts.page.search")}
									onChange={(e) => setView({ ...view, q: e.target.value })}
									// 宽屏：没有搜索词时失焦就收回放大镜
									onBlur={() => setSearchOpen(false)}
									className="h-8 w-full rounded-lg border border-border bg-inset pr-2.5 pl-8 text-sm text-fg placeholder:text-faint focus-visible:border-accent focus-visible:outline-none focus-visible:ring-3 focus-visible:ring-accent/20"
								/>
							</label>
						) : (
							<IconButton
								icon="search"
								label={t("artifacts.page.search")}
								data-testid="artifacts-search"
								onClick={openSearch}
							/>
						)}
						{!compact && (
							<>
								<div className="relative">
									<IconButton
										icon="funnel"
										label={
											filterCount > 0
												? t("artifacts.page.filterN", { n: filterCount })
												: t("artifacts.page.filter")
										}
										className={
											pop === "filter" || filterCount > 0
												? "bg-active text-fg"
												: ""
										}
										aria-expanded={pop === "filter"}
										data-testid="artifacts-filter"
										onClick={() => setPop(pop === "filter" ? null : "filter")}
									/>
									{filterCount > 0 && (
										<span
											aria-hidden
											className="pointer-events-none absolute -top-1 -right-1 grid h-4 min-w-4 place-items-center rounded-full bg-fg px-1 font-medium text-2xs text-bg tabular-nums"
										>
											{filterCount}
										</span>
									)}
									{pop === "filter" && (
										<Popover
											side="bottom"
											align="end"
											onClose={() => setPop(null)}
										>
											{filterChoices}
										</Popover>
									)}
								</div>
								<div className="relative">
									<IconButton
										icon="sliders"
										label={t("artifacts.page.display")}
										className={pop === "display" ? "bg-active text-fg" : ""}
										aria-expanded={pop === "display"}
										data-testid="artifacts-display"
										onClick={() => setPop(pop === "display" ? null : "display")}
									/>
									{pop === "display" && (
										<Popover
											side="bottom"
											align="end"
											onClose={() => setPop(null)}
										>
											{displayChoices}
										</Popover>
									)}
								</div>
								<Menu
									items={[
										{
											key: "file",
											icon: "fileCode",
											label: t("artifacts.import.file"),
											onSelect: () => fileRef.current?.click(),
										},
										...(folderPick
											? [
													{
														key: "folder",
														icon: "folder" as const,
														label: t("artifacts.import.folder"),
														onSelect: () => folderRef.current?.click(),
													},
												]
											: []),
									]}
									trigger={(p) => (
										<IconButton
											icon="plus"
											label={t("artifacts.import.button")}
											data-testid="artifacts-import"
											{...p}
										/>
									)}
								/>
							</>
						)}
						<input
							ref={fileRef}
							type="file"
							accept=".html,.htm"
							hidden
							data-testid="import-file"
							onChange={(e) => {
								pickFiles(e.currentTarget.files);
								e.currentTarget.value = "";
							}}
						/>
						{folderPick && (
							<input
								ref={folderRef}
								type="file"
								hidden
								// 非标准属性（Chromium / WebKit / Firefox 都支持），React 的类型里没有
								{...{ webkitdirectory: "" }}
								onChange={(e) => {
									pickFiles(e.currentTarget.files);
									e.currentTarget.value = "";
								}}
							/>
						)}
					</div>
					{content}
					<div className="flex justify-center pt-2">
						<Link
							to="/artifacts/trash"
							data-testid="artifacts-trash-link"
							className="inline-flex items-center gap-1.5 rounded-md px-2 py-1 text-muted text-xs hover:bg-hover hover:text-fg"
						>
							<Icon name="trash" size={13} />
							{t("artifacts.page.trashLink", { n: deletedCount })}
						</Link>
					</div>
				</div>
			</div>
			{importing && (
				<ImportDialog
					files={importing}
					defaultProjectId={
						view.projects.length === 1 && view.projects[0] !== NO_PROJECT
							? view.projects[0]
							: undefined
					}
					onClose={() => setImporting(null)}
					onImported={(a) =>
						showToast({
							text: t("artifacts.import.done", { title: a.title }),
							action: {
								label: t("artifacts.menu.open"),
								run: () => navigate(`/artifacts/${a.id}`),
							},
						})
					}
				/>
			)}
		</Sheet>
	);
}

function TrashScreen(): ReactNode {
	const { t } = useTranslation();
	const bp = useBreakpoint();
	const navigate = useNavigate();
	const list = useArtifacts({ deleted: true });
	const undelete = useUndeleteArtifact();
	const scrollRef = useRef<HTMLDivElement>(null);

	useEffect(() => {
		const onKey = (e: KeyboardEvent) => {
			if (e.key !== "Escape" || escOwnedElsewhere(e)) return;
			const el = e.target as HTMLElement | null;
			if (el?.closest?.("input, textarea, select, [contenteditable]")) return;
			navigate("/artifacts");
		};
		window.addEventListener("keydown", onKey);
		return () => window.removeEventListener("keydown", onKey);
	}, [navigate]);

	// 最近删除的在前
	const items = (list.data ?? []).toSorted(
		(x, y) => (y.deletedAt ?? 0) - (x.deletedAt ?? 0),
	);

	let content: ReactNode;
	if (list.isPending) content = <SkeletonGrid />;
	else if (list.isError)
		content = (
			<EmptyState icon="alert" title={t("artifacts.page.failed")}>
				<div className="flex flex-col items-center gap-3">
					<span className="break-words">{describeError(list.error, t)}</span>
					<Button size="sm" icon="refresh" onClick={() => void list.refetch()}>
						{t("artifacts.viewer.retry")}
					</Button>
				</div>
			</EmptyState>
		);
	else if (items.length === 0)
		content = (
			<EmptyState icon="trash" title={t("artifacts.page.trashEmpty")} />
		);
	else
		content = (
			<CardGrid
				flat={items}
				trash
				scrollRef={scrollRef}
				renderMeta={(a) => (
					<span data-testid="days-left">
						{t("artifacts.page.daysLeft", {
							count: daysLeft(a.deletedAt ?? Date.now()),
						})}
					</span>
				)}
				renderAction={(a) => (
					<Button
						size="sm"
						variant="ghost"
						icon="restart"
						tabIndex={-1}
						data-testid="trash-restore"
						disabled={undelete.isPending && undelete.variables === a.id}
						onClick={() =>
							undelete.mutateAsync(a.id).then(
								() =>
									showToast({
										text: t("artifacts.toast.restored", { title: a.title }),
									}),
								(err: unknown) =>
									showToast({ tone: "err", text: describeError(err, t) }),
							)
						}
					>
						{t("artifacts.menu.restore")}
					</Button>
				)}
			/>
		);

	return (
		<Sheet inset={bp === "expanded"} className="flex h-full min-h-0 flex-col">
			<TitleBar trash />
			<div
				ref={scrollRef}
				data-testid="artifacts-scroll"
				className="min-h-0 flex-1 overflow-y-auto overflow-x-hidden p-3 pb-safe sm:px-8 sm:pt-6"
			>
				<div className="mx-auto flex min-w-0 max-w-6xl flex-col gap-4 pb-6">
					<Callout tone="neutral">
						{t("artifacts.page.trashHint", { days: KEEP_DELETED_DAYS })}
					</Callout>
					{content}
				</div>
			</div>
		</Sheet>
	);
}
