/**
 * 命令面板（Ctrl/⌘+K，按需加载的独立分块）：一个输入框搜遍线程、Artifacts、对话内容、项目、Space、设置分区与常用操作。
 * - 空查询：最近 6 个线程 + 最近打开的至多 3 个 artifact + 全部操作；有查询：按组列出，每组 3 项，组尾「显示全部」把这一组展开到 50 项（只看这一组，Esc 退回）。
 * - 本地各组用 `lib/palette-match`（多词 AND、大小写与变音符折叠、高亮落在原文上）；artifact 只匹配标题、项目名与类型名，
 *   不搜内容；「对话内容」组走服务端全文搜索
 *   （输入停顿 200ms 后发请求，查询变化时取消在途请求），选中后打开线程并定位到那条消息。
 * - 键盘：↑↓ / Home / End 移动，Enter 执行，Esc 先退出展开再关闭；输入法组字期间的 Enter / 方向键一律不处理。
 *   焦点始终在输入框（组合框 + aria-activedescendant），关闭后还原到打开前的元素。
 */
import { keepPreviousData, useQuery } from "@tanstack/react-query";
import {
	type KeyboardEvent,
	type ReactNode,
	useCallback,
	useEffect,
	useId,
	useLayoutEffect,
	useMemo,
	useRef,
	useState,
} from "react";
import { createPortal } from "react-dom";
import { useTranslation } from "react-i18next";
import { useNavigate } from "react-router";
import { SETTINGS } from "../../App";
import { useArtifacts } from "../../api/artifact-queries";
import { api, type Schemas, unwrap } from "../../api/client";
import { useProjects } from "../../api/queries";
import { useSpaces } from "../../api/space-queries";
import { kindIcon, kindLabelKey } from "../../lib/artifacts/kinds";
import { formatRelative } from "../../lib/format";
import { host } from "../../lib/host";
import { pushModal, trapTab } from "../../lib/modal";
import {
	type Folded,
	fold,
	matchFields,
	queryTerms,
	splitHighlights,
	type TextRange,
} from "../../lib/palette-match";
import { isCoordinatorThread } from "../../lib/thread-kind";
import { parseTitleCard } from "../../lib/title";
import { useDebounced } from "../../lib/use-debounced";
import { useBreakpoint } from "../../lib/viewport";
import { useIndex } from "../../state/stores";
import { type ThemeMode, themeStore, useTheme } from "../../state/theme";
import { uiStore } from "../../state/ui";
import { recentAt } from "../artifacts/artifacts-model";
import { MORE_ENTRIES } from "../layout/more-entries";
import { revealWhenReady } from "../transcript/reveal";
import { ProjectBadge } from "../ui/data";
import { Icon, type IconName } from "../ui/icons";
import {
	describeError,
	IconButton,
	Kbd,
	Spinner,
	ThreadStateIcon,
} from "../ui/primitives";
import { closePalette, paletteStore } from "./palette-state";

/** 输入停顿多久才发内容搜索 */
const DEBOUNCE_MS = 200;
/** 有查询时每组显示的条数；展开后的上限 */
const PER_GROUP = 3;
const EXPANDED_MAX = 50;
/** 空查询时的最近线程数、最近打开的 artifact 数 */
const RECENT = 6;
const RECENT_ARTIFACTS = 3;

type GroupKey =
	| "recent"
	| "recentArtifacts"
	| "threads"
	| "artifacts"
	| "content"
	| "projects"
	| "spaces"
	| "settings"
	| "actions";

interface ItemRow {
	kind: "item";
	key: string;
	leading: ReactNode;
	label: string;
	labelRanges?: readonly TextRange[];
	/** 第二行：线程的项目 / 标签 / 分支、Space 目标、内容命中的片段 */
	detail?: string;
	detailRanges?: readonly TextRange[];
	/** 片段最多两行，其余第二行单行截断 */
	multiline?: boolean;
	/** 标题后的淡色标记（已归档、fork 数） */
	tags?: string[];
	/** 右侧淡色说明（相对时间、当前值） */
	meta?: string;
	run: () => void;
}

interface MoreRow {
	kind: "more";
	key: string;
	group: GroupKey;
	label: string;
}

type Row = ItemRow | MoreRow;

interface Group {
	key: GroupKey;
	rows: Row[];
	/** 标题右侧的状态（搜索中、索引未完成） */
	note?: ReactNode;
}

/** 本地候选：匹配字段（第一个是标题；第二个若有，是显示在第二行的 detail）与生成行的函数。 */
interface Candidate {
	fields: Folded[];
	/** 同分时越大越靠前 */
	recency: number;
	/** 同分时排在后面（已归档） */
	demote?: boolean;
	row: (ranges: TextRange[][]) => ItemRow;
}

type SearchResults = Schemas["SearchResults"];

const MODE_ICON = {
	system: "monitor",
	light: "sun",
	dark: "moon",
} as const satisfies Record<ThemeMode, IconName>;

/** 按查询过滤并排序候选：得分高的在前，同分时未归档在前、近的在前。 */
function rank(
	cands: readonly Candidate[],
	terms: readonly string[],
): ItemRow[] {
	const hits: { c: Candidate; score: number; ranges: TextRange[][] }[] = [];
	for (const c of cands) {
		const m = matchFields(terms, c.fields);
		if (m) hits.push({ c, score: m.score, ranges: m.ranges });
	}
	hits.sort(
		(a, b) =>
			b.score - a.score ||
			Number(a.c.demote ?? false) - Number(b.c.demote ?? false) ||
			b.c.recency - a.c.recency,
	);
	return hits.map((h) => h.c.row(h.ranges));
}

function Highlighted({
	text,
	ranges,
}: {
	text: string;
	ranges?: readonly TextRange[];
}): ReactNode {
	if (!ranges?.length) return text;
	return splitHighlights(text, ranges).map((p, i) =>
		p.hit ? (
			// biome-ignore lint/suspicious/noArrayIndexKey: 片段按位置切出，顺序固定
			<mark key={i} className="bg-transparent font-semibold text-fg">
				{p.text}
			</mark>
		) : (
			p.text
		),
	);
}

export function CommandPalette(): ReactNode {
	const { t } = useTranslation();
	const navigate = useNavigate();
	const compact = useBreakpoint() === "compact";
	const hostRef = useRef<HTMLDivElement>(null);
	const panelRef = useRef<HTMLDivElement>(null);
	const inputRef = useRef<HTMLInputElement>(null);
	const listRef = useRef<HTMLDivElement>(null);
	const listId = useId();
	const [q, setQ] = useState(() => paletteStore.getState().initialQuery);
	const [expanded, setExpanded] = useState<GroupKey | null>(null);
	const [active, setActive] = useState(0);
	const threads = useIndex((s) => s.threads);
	const projects = useProjects();
	const spaces = useSpaces();
	// 面板只在打开时挂载（PaletteHost），列表也就只在打开时请求；缺省筛选不含回收站
	const artifacts = useArtifacts({});
	const mode = useTheme((s) => s.mode);
	const terms = useMemo(() => queryTerms(q), [q]);
	const searching = terms.length > 0;

	// 模态：背景 inert、Tab 圈在面板里；打开即聚焦输入框。卸载时先撤掉背景 inert，再按关闭方式还原焦点
	// （见 palette-state；元素已不在文档里时不还原）
	useLayoutEffect(() => {
		const hostEl = hostRef.current;
		if (!hostEl) return;
		const release = pushModal(hostEl);
		inputRef.current?.focus();
		return () => {
			release();
			const opener = paletteStore.getState().opener;
			if (opener?.isConnected) opener.focus();
		};
	}, []);

	// 选中结果即跳转：不还原焦点；手机的下沉侧栏 / 抽屉随之收起（同侧栏里点线程）
	const go = useCallback(
		(to: string) => {
			closePalette(false);
			uiStore.getState().setSidebar(false);
			navigate(to);
		},
		[navigate],
	);

	const projectName = useMemo(
		() =>
			new Map(
				(projects.data?.projects ?? []).map((p) => [p.id, p.name] as const),
			),
		[projects.data],
	);

	// ---- 本地候选（只随数据变化重建；折叠一次，逐键匹配）
	const threadCands = useMemo<Candidate[]>(
		() =>
			Object.values(threads)
				.filter((th) => !isCoordinatorThread(th))
				.map((th) => {
					const title = parseTitleCard(th.title).text || t("thread.untitled");
					const detail = [
						th.projectId ? projectName.get(th.projectId) : undefined,
						...th.labels,
						th.worktree?.branch,
					]
						.filter(Boolean)
						.join(" · ");
					return {
						fields: [fold(title), fold(detail)],
						recency: th.lastMessageAt ?? th.updatedAt,
						demote: th.archivedAt !== undefined,
						row: (ranges) => ({
							kind: "item",
							key: `thread:${th.id}`,
							leading: <ThreadStateIcon state={th.state} />,
							label: title,
							labelRanges: ranges[0],
							detail: detail || undefined,
							detailRanges: ranges[1],
							tags: th.archivedAt ? [t("palette.archived")] : undefined,
							meta: formatRelative(th.lastMessageAt ?? th.updatedAt),
							run: () => go(`/t/${th.id}`),
						}),
					};
				}),
		[threads, projectName, t, go],
	);

	const projectCands = useMemo<Candidate[]>(
		() =>
			(projects.data?.projects ?? []).map((p) => ({
				fields: [fold(p.name)],
				recency: p.updatedAt,
				row: (ranges) => ({
					kind: "item",
					key: `project:${p.id}`,
					leading: <ProjectBadge name={p.name} icon={p.icon} size={16} />,
					label: p.name,
					labelRanges: ranges[0],
					run: () => go(`/projects/${p.id}`),
				}),
			})),
		[projects.data, go],
	);

	const spaceCands = useMemo<Candidate[]>(
		() =>
			(spaces.data?.spaces ?? []).map((s) => ({
				fields: [fold(s.name), fold(s.goal)],
				recency: s.updatedAt,
				row: (ranges) => ({
					kind: "item",
					key: `space:${s.id}`,
					leading: <Icon name="target" size={15} className="text-muted" />,
					label: s.name,
					labelRanges: ranges[0],
					detail: s.goal || undefined,
					detailRanges: ranges[1],
					run: () => go(`/space/${s.id}`),
				}),
			})),
		[spaces.data, go],
	);

	const artifactCands = useMemo<Candidate[]>(
		() =>
			(artifacts.data ?? [])
				.filter((a) => a.deletedAt === undefined)
				.map((a) => {
					const project = a.projectId
						? projectName.get(a.projectId)
						: undefined;
					const kind = t(kindLabelKey(a.kind));
					const detail = [
						project,
						kind,
						t("artifacts.viewer.version", { n: a.version }),
					]
						.filter(Boolean)
						.join(" · ");
					const at = recentAt(a);
					return {
						// 只搜标题、项目名与类型名；高亮只画在标题上
						fields: [fold(a.title), fold(project ?? ""), fold(kind)],
						recency: at,
						row: (ranges) => ({
							kind: "item",
							key: `artifact:${a.id}`,
							leading: (
								<Icon
									name={kindIcon(a.kind)}
									size={15}
									className="text-muted"
								/>
							),
							label: a.title,
							labelRanges: ranges[0],
							detail,
							meta: formatRelative(at),
							run: () => go(`/artifacts/${a.id}`),
						}),
					};
				}),
		[artifacts.data, projectName, t, go],
	);

	const settingCands = useMemo<Candidate[]>(
		() =>
			SETTINGS.filter((s) => s !== "desktop" || host.desktop !== null).map(
				(s) => {
					const label = t(`settings.${s}`);
					const icon =
						MORE_ENTRIES.find((e) => e.key === s)?.icon ?? "settings";
					return {
						fields: [fold(label), fold(s)],
						recency: 0,
						row: (ranges) => ({
							kind: "item",
							key: `setting:${s}`,
							leading: <Icon name={icon} size={15} className="text-muted" />,
							label,
							labelRanges: ranges[0],
							run: () => go(`/settings/${s}`),
						}),
					};
				},
			),
		[t, go],
	);

	const actionCands = useMemo<Candidate[]>(() => {
		const action = (
			key: string,
			icon: IconName,
			label: string,
			keywords: string,
			run: () => void,
			meta?: string,
		): Candidate => ({
			fields: [fold(label), fold(keywords)],
			recency: 0,
			row: (ranges) => ({
				kind: "item",
				key: `action:${key}`,
				leading: <Icon name={icon} size={15} className="text-muted" />,
				label,
				labelRanges: ranges[0],
				meta,
				run,
			}),
		});
		return [
			action("new", "plus", t("palette.actions.newThread"), "new thread", () =>
				go("/"),
			),
			action("inbox", "tray", t("palette.actions.inbox"), "inbox", () =>
				go("/inbox"),
			),
			action("stats", "chart", t("palette.actions.stats"), "stats usage", () =>
				go("/stats"),
			),
			action(
				"artifacts",
				"shapes",
				t("palette.actions.artifacts"),
				"artifacts",
				() => go("/artifacts"),
			),
			action(
				"boxes",
				"hardDrives",
				t("palette.actions.boxes"),
				"boxes deploy apps machine 部署 机器",
				() => go("/boxes"),
			),
			action(
				"providers-overview",
				"gauge",
				t("palette.actions.providersOverview"),
				"providers overview orbs memory storage cpu disk 节点 算力 内存 存储",
				() => go("/providers"),
			),
			action(
				"settings",
				"settings",
				t("palette.actions.settings"),
				"settings preferences",
				() => go("/settings"),
			),
			action(
				"theme",
				MODE_ICON[mode],
				t("palette.actions.theme"),
				"theme dark light mode",
				// 切换明暗不离开面板：右侧的当前值随之更新
				() => themeStore.getState().cycleMode(),
				t(`appearance.modes.${mode}`),
			),
		];
	}, [t, mode, go]);

	// ---- 对话内容：服务端全文搜索
	const debounced = useDebounced(q.trim(), DEBOUNCE_MS);
	const contentLimit = expanded === "content" ? EXPANDED_MAX : PER_GROUP + 1;
	const content = useQuery({
		queryKey: ["search", debounced, contentLimit],
		// 查询键一变，旧请求的观察者随之撤走，TanStack Query 经 signal 取消它
		queryFn: ({ signal }) =>
			unwrap<SearchResults>(
				api.GET("/api/v1/search", {
					params: { query: { q: debounced, limit: contentLimit } },
					signal,
				}),
			),
		enabled: debounced !== "",
		staleTime: 30_000,
		placeholderData: keepPreviousData,
	});

	const contentRows = useMemo<ItemRow[]>(
		() =>
			(content.data?.hits ?? []).map((h) => {
				const title =
					parseTitleCard(h.threadTitle).text || t("thread.untitled");
				const tags: string[] = [];
				if (h.archived) tags.push(t("palette.archived"));
				if (h.copies > 1)
					tags.push(t("palette.forks", { count: h.copies - 1 }));
				return {
					kind: "item",
					key: `content:${h.threadId}:${h.entryId}`,
					leading: (
						<Icon
							name={h.role === "user" ? "user" : "robot"}
							size={15}
							className="text-muted"
						/>
					),
					label: title,
					detail: h.snippet,
					detailRanges: h.highlights,
					multiline: true,
					tags,
					meta: h.ts > 0 ? formatRelative(h.ts) : undefined,
					run: () => {
						go(`/t/${h.threadId}`);
						revealWhenReady(h.threadId, { entryId: h.entryId, seq: h.seq });
					},
				};
			}),
		[content.data, t, go],
	);

	// ---- 分组
	const groups = useMemo<Group[]>(() => {
		if (!searching) {
			const recent = threadCands
				.filter((c) => !c.demote)
				.sort((a, b) => b.recency - a.recency)
				.slice(0, RECENT)
				.map((c) => c.row([]));
			const recentArtifacts = artifactCands
				.toSorted((a, b) => b.recency - a.recency)
				.slice(0, RECENT_ARTIFACTS)
				.map((c) => c.row([]));
			return [
				{ key: "recent" as const, rows: recent },
				{ key: "recentArtifacts" as const, rows: recentArtifacts },
				{ key: "actions" as const, rows: actionCands.map((c) => c.row([])) },
			].filter((g) => g.rows.length > 0);
		}
		const contentNote =
			content.isFetching || debounced !== q.trim() ? (
				<Spinner size={11} />
			) : content.data?.indexing ? (
				<span className="text-faint">{t("palette.indexing")}</span>
			) : undefined;
		const all: { key: GroupKey; rows: ItemRow[]; more: boolean }[] = [
			{ key: "threads", rows: rank(threadCands, terms), more: false },
			{ key: "artifacts", rows: rank(artifactCands, terms), more: false },
			{
				key: "content",
				rows: contentRows,
				more:
					contentRows.length > PER_GROUP ||
					content.data?.nextCursor !== undefined,
			},
			{ key: "projects", rows: rank(projectCands, terms), more: false },
			{ key: "spaces", rows: rank(spaceCands, terms), more: false },
			{ key: "settings", rows: rank(settingCands, terms), more: false },
			{ key: "actions", rows: rank(actionCands, terms), more: false },
		];
		const out: Group[] = [];
		for (const g of all) {
			const note = g.key === "content" ? contentNote : undefined;
			if (expanded && g.key !== expanded) continue;
			if (g.rows.length === 0 && !note) continue;
			if (expanded) {
				out.push({ key: g.key, rows: g.rows.slice(0, EXPANDED_MAX), note });
				continue;
			}
			const rows: Row[] = g.rows.slice(0, PER_GROUP);
			if (g.more || g.rows.length > PER_GROUP)
				rows.push({
					kind: "more",
					key: `more:${g.key}`,
					group: g.key,
					label:
						g.key === "content"
							? t("palette.moreContent")
							: t("palette.showAll", {
									count: Math.min(g.rows.length, EXPANDED_MAX),
								}),
				});
			out.push({ key: g.key, rows, note });
		}
		return out;
	}, [
		searching,
		terms,
		expanded,
		threadCands,
		artifactCands,
		projectCands,
		spaceCands,
		settingCands,
		actionCands,
		contentRows,
		content.isFetching,
		content.data,
		debounced,
		q,
		t,
	]);

	const flat = useMemo(() => groups.flatMap((g) => g.rows), [groups]);
	const activeIndex = Math.min(active, Math.max(flat.length - 1, 0));
	// biome-ignore lint/correctness/useExhaustiveDependencies: 查询或展开变化时回到第一项
	useEffect(() => setActive(0), [q, expanded]);
	// biome-ignore lint/correctness/useExhaustiveDependencies: 高亮项变化即滚到可见
	useEffect(() => {
		listRef.current
			?.querySelector<HTMLElement>("[aria-selected=true]")
			?.scrollIntoView?.({ block: "nearest" });
	}, [activeIndex, groups]);

	const activate = (row: Row | undefined) => {
		if (!row) return;
		if (row.kind === "more") {
			setExpanded(row.group);
			inputRef.current?.focus();
			return;
		}
		row.run();
	};

	const onKeyDown = (e: KeyboardEvent<HTMLInputElement>) => {
		// 输入法组字期间（含 Safari 在 compositionend 之后补发的 229）不处理任何键
		if (e.nativeEvent.isComposing || e.keyCode === 229) return;
		const last = flat.length - 1;
		switch (e.key) {
			case "ArrowDown":
				e.preventDefault();
				setActive(activeIndex >= last ? 0 : activeIndex + 1);
				break;
			case "ArrowUp":
				e.preventDefault();
				setActive(activeIndex <= 0 ? Math.max(last, 0) : activeIndex - 1);
				break;
			case "Home":
				if (!q) {
					e.preventDefault();
					setActive(0);
				}
				break;
			case "End":
				if (!q) {
					e.preventDefault();
					setActive(Math.max(last, 0));
				}
				break;
			case "Enter":
				e.preventDefault();
				activate(flat[activeIndex]);
				break;
			case "Escape":
				e.preventDefault();
				e.stopPropagation();
				if (expanded) setExpanded(null);
				else closePalette();
				break;
			case "Tab":
				if (panelRef.current) trapTab(e, panelRef.current);
				break;
		}
	};

	const groupTitle = (key: GroupKey) => t(`palette.groups.${key}`);
	const activeId = flat[activeIndex] ? `${listId}-${activeIndex}` : undefined;
	let index = -1;

	return createPortal(
		<div
			ref={hostRef}
			className="fixed inset-0 z-50 flex items-start justify-center px-2 pt-[max(0.5rem,env(safe-area-inset-top))] sm:px-4 sm:pt-[12dvh]"
		>
			<button
				type="button"
				tabIndex={-1}
				aria-label={t("common.close")}
				className="absolute inset-0 bg-scrim backdrop-blur-[3px]"
				onClick={() => closePalette()}
			/>
			<div
				ref={panelRef}
				role="dialog"
				aria-modal="true"
				aria-label={t("palette.title")}
				className="pop-in relative flex max-h-[min(36rem,calc(100dvh-1rem))] w-full max-w-xl flex-col overflow-hidden rounded-xl bg-panel shadow-xl ring-1 ring-[var(--glass-line)] sm:max-h-[min(36rem,76dvh)]"
			>
				<div className="flex h-14 shrink-0 items-center gap-3 border-border border-b pr-3 pl-5">
					<Icon name="search" size={16} className="shrink-0 text-faint" />
					<input
						ref={inputRef}
						value={q}
						onChange={(e) => {
							setQ(e.target.value);
							if (!e.target.value.trim()) setExpanded(null);
						}}
						onKeyDown={onKeyDown}
						maxLength={200}
						placeholder={t("palette.placeholder")}
						role="combobox"
						aria-expanded="true"
						aria-controls={listId}
						aria-activedescendant={activeId}
						aria-autocomplete="list"
						autoComplete="off"
						spellCheck={false}
						enterKeyHint="go"
						// 16px：iOS 不因聚焦放大页面
						className="h-full min-w-0 flex-1 bg-transparent text-base text-fg outline-none placeholder:text-faint focus-visible:outline-none"
					/>
					{compact ? (
						<IconButton
							icon="close"
							size={14}
							box="sm"
							label={t("common.close")}
							className="rounded-full! bg-inset"
							onClick={() => closePalette()}
						/>
					) : (
						<Kbd>Esc</Kbd>
					)}
				</div>
				<div
					ref={listRef}
					id={listId}
					role="listbox"
					aria-label={t("palette.title")}
					className="min-h-0 flex-1 scroll-py-2 overflow-y-auto overscroll-contain p-2"
				>
					{expanded && (
						<button
							type="button"
							tabIndex={-1}
							onMouseDown={(e) => e.preventDefault()}
							onClick={() => setExpanded(null)}
							className="mb-1 flex h-8 items-center gap-1.5 rounded-lg px-2.5 text-muted text-xs hover:bg-hover hover:text-fg"
						>
							<Icon name="back" size={13} />
							{t("palette.back")}
						</button>
					)}
					{groups.length === 0 && (
						<p className="px-2.5 py-6 text-center text-faint text-sm">
							{t("palette.empty")}
						</p>
					)}
					{groups.map((g) => (
						// biome-ignore lint/a11y/useSemanticElements: listbox 里的选项分组（WAI-ARIA group），不是表单的 fieldset
						<div
							key={g.key}
							role="group"
							aria-label={groupTitle(g.key)}
							className="pb-1"
						>
							<div className="flex h-7 items-center gap-2 px-2.5 font-semibold text-muted text-xs">
								<span className="truncate">{groupTitle(g.key)}</span>
								{g.note && (
									<span className="flex min-w-0 items-center font-normal">
										{g.note}
									</span>
								)}
							</div>
							{g.key === "content" && content.isError && (
								<p className="px-2.5 py-1.5 text-err text-xs">
									{t("palette.searchFailed", {
										error: describeError(content.error, t),
									})}
								</p>
							)}
							{g.rows.map((row) => {
								index++;
								const i = index;
								const selected = i === activeIndex;
								return (
									<button
										key={row.key}
										id={`${listId}-${i}`}
										type="button"
										role="option"
										tabIndex={-1}
										aria-selected={selected}
										// 不抢输入框的焦点；指针移动（而非 enter）才改高亮，列表滚动经过指针时不跳
										onMouseDown={(e) => e.preventDefault()}
										onPointerMove={() => {
											if (!selected) setActive(i);
										}}
										onClick={() => activate(row)}
										className={`flex w-full items-center gap-2.5 rounded-lg px-2.5 text-left text-sm transition-colors duration-100 ${selected ? "bg-hover" : ""} ${row.kind === "more" ? "h-8 text-muted text-xs" : "min-h-10 py-1.5"}`}
									>
										{row.kind === "more" ? (
											<>
												<span className="grid w-4 shrink-0 place-items-center">
													<Icon name="chevronDown" size={13} />
												</span>
												<span className="truncate">{row.label}</span>
											</>
										) : (
											<>
												{/* 两行的项（带 detail）图标与时间对齐第一行，单行项居中 */}
												<span
													className={`grid w-4 shrink-0 place-items-center ${row.detail ? "self-start pt-0.5" : ""}`}
												>
													{row.leading}
												</span>
												<span className="flex min-w-0 flex-1 flex-col">
													<span className="flex min-w-0 items-baseline gap-2">
														<span className="truncate text-fg">
															<Highlighted
																text={row.label}
																ranges={row.labelRanges}
															/>
														</span>
														{row.tags?.map((tag) => (
															<span
																key={tag}
																className="shrink-0 text-2xs text-faint"
															>
																{tag}
															</span>
														))}
													</span>
													{row.detail && (
														<span
															className={`text-muted text-xs ${row.multiline ? "line-clamp-2 break-words" : "truncate"}`}
														>
															<Highlighted
																text={row.detail}
																ranges={row.detailRanges}
															/>
														</span>
													)}
												</span>
												{row.meta && (
													<span
														className={`shrink-0 text-faint text-xs ${row.detail ? "self-start pt-0.5" : ""}`}
													>
														{row.meta}
													</span>
												)}
											</>
										)}
									</button>
								);
							})}
						</div>
					))}
				</div>
				{!compact && (
					<div className="flex h-9 shrink-0 items-center gap-4 border-border border-t px-5 text-faint text-xs">
						<span className="flex items-center gap-1.5">
							<Kbd>↑↓</Kbd>
							{t("palette.hints.navigate")}
						</span>
						<span className="flex items-center gap-1.5">
							<Kbd>↵</Kbd>
							{t("palette.hints.open")}
						</span>
						<span className="flex items-center gap-1.5">
							<Kbd>Esc</Kbd>
							{expanded ? t("palette.back") : t("palette.hints.close")}
						</span>
					</div>
				)}
			</div>
		</div>,
		document.body,
	);
}
