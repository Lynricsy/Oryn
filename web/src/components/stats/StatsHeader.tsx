/**
 * 统计页页头（M10 计划 5.3）：标题与三个标签（用量 / Orb / 产出，NavLink 保留当前查询串）；工具行是范围选择
 * （expanded 为分段按钮，其余为菜单；每项带 `data-range`）、已选筛选的胶囊（各带清除）、预算入口与导出菜单
 * （`data-testid="stats-export"`，三项都是带 `download` 的同源链接）。
 */
import { type ReactNode, useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { NavLink } from "react-router";
import { useProjects } from "../../api/queries";
import { useSpaces } from "../../api/space-queries";
import { useBreakpoint } from "../../lib/viewport";
import { useIndex } from "../../state/stores";
import {
	uiStore,
	useSidebarShown,
	useSidebarToggleShown,
} from "../../state/ui";
import { ChromeLead, WindowControls } from "../layout/WindowControls";
import { Floating } from "../ui/Floating";
import { Icon } from "../ui/icons";
import {
	Button,
	Dialog,
	Field,
	IconButton,
	Menu,
	TextInput,
	tabClass,
} from "../ui/primitives";
import { groupName } from "./format";
import {
	FILTER_KEYS,
	type FilterKey,
	localDate,
	RANGES,
	type RangeKey,
	STATS_TABS,
	type StatsParams,
	type StatsTab,
	statsSearch,
} from "./statsParams";

export interface ExportLinks {
	viewCsv: string;
	viewJson: string;
	recordsCsv: string;
}

export function StatsTitleBar({
	tab,
	params,
}: {
	tab: StatsTab;
	params: StatsParams;
}): ReactNode {
	const { t } = useTranslation();
	const bp = useBreakpoint();
	const sidebarToggle = useSidebarToggleShown();
	const chromeLeft = !useSidebarShown();
	const search = statsSearch(params);
	const tabs = (
		<nav
			aria-label={t("stats.title")}
			className="flex min-w-0 items-center gap-0.5"
		>
			{STATS_TABS.map((k) => (
				<NavLink
					key={k}
					to={`/stats/${k}${search ? `?${search}` : ""}`}
					className={`${tabClass(k === tab)} ${k === tab ? "bg-hover" : ""}`}
				>
					{t(`stats.tabs.${k}`)}
				</NavLink>
			))}
		</nav>
	);
	return (
		<header
			data-window-drag=""
			className="flex h-12 shrink-0 items-center gap-2 border-border border-b px-3"
		>
			<ChromeLead show={chromeLeft} />
			{sidebarToggle && (
				<IconButton
					icon={bp === "expanded" ? "sidebar" : "menu"}
					label={t("nav.sidebar")}
					onClick={() => uiStore.getState().toggleSidebar()}
				/>
			)}
			<Icon name="chart" size={16} className="shrink-0 text-faint" />
			<h1 className="shrink-0 font-semibold text-sm">{t("stats.title")}</h1>
			<span aria-hidden className="text-ghost text-sm">
				/
			</span>
			{tabs}
			<span className="min-w-0 flex-1" />
			<WindowControls side="right" className="-mr-1" />
		</header>
	);
}

function CustomRangeDialog({
	params,
	onApply,
	onClose,
}: {
	params: StatsParams;
	onApply: (from: string, to: string) => void;
	onClose: () => void;
}): ReactNode {
	const { t } = useTranslation();
	const today = localDate(Date.now());
	const [from, setFrom] = useState(
		params.from ?? localDate(Date.now() - 6 * 86_400_000),
	);
	const [to, setTo] = useState(params.to ?? today);
	const valid = Boolean(from && to && from <= to);
	return (
		<Dialog
			open
			title={t("stats.custom.title")}
			onClose={onClose}
			footer={
				<>
					<Button variant="ghost" onClick={onClose}>
						{t("common.cancel")}
					</Button>
					<Button
						variant="primary"
						disabled={!valid}
						onClick={() => onApply(from, to)}
					>
						{t("stats.custom.apply")}
					</Button>
				</>
			}
		>
			<div className="grid grid-cols-2 gap-3">
				<Field label={t("stats.custom.from")}>
					<TextInput
						type="date"
						value={from}
						max={today}
						onChange={(e) => setFrom(e.target.value)}
					/>
				</Field>
				<Field label={t("stats.custom.to")}>
					<TextInput
						type="date"
						value={to}
						max={today}
						onChange={(e) => setTo(e.target.value)}
					/>
				</Field>
			</div>
		</Dialog>
	);
}

function RangePicker({
	params,
	onChange,
}: {
	params: StatsParams;
	onChange: (patch: Partial<StatsParams>) => void;
}): ReactNode {
	const { t } = useTranslation();
	const expanded = useBreakpoint() === "expanded";
	const [custom, setCustom] = useState(false);
	const pick = (r: RangeKey) => {
		if (r === "custom") setCustom(true);
		else onChange({ range: r, from: undefined, to: undefined });
	};
	const label = (r: RangeKey) =>
		r === "custom" && params.range === "custom" && params.from && params.to
			? `${params.from} – ${params.to}`
			: t(`stats.range.${r}`);
	return (
		<>
			{expanded ? (
				// biome-ignore lint/a11y/useSemanticElements: 分段按钮（aria-pressed），不是表单字段集
				<div
					role="group"
					aria-label={t("stats.range.label")}
					className="flex shrink-0 items-center gap-0.5 rounded-md bg-inset p-0.5"
				>
					{RANGES.map((r) => (
						<button
							key={r}
							type="button"
							data-range={r}
							aria-pressed={params.range === r}
							onClick={() => pick(r)}
							className={`${tabClass(params.range === r)} h-6 ${params.range === r ? "bg-panel shadow-xs" : ""}`}
						>
							{label(r)}
						</button>
					))}
				</div>
			) : (
				<Menu
					align="left"
					className="shrink-0"
					trigger={(p) => (
						<Button size="sm" icon="clock" {...p}>
							{label(params.range)}
							<Icon name="chevronDown" size={12} className="text-faint" />
						</Button>
					)}
					items={RANGES.map((r) => ({
						key: r,
						label: t(`stats.range.${r}`),
						checked: params.range === r,
						attrs: { "data-range": r },
						onSelect: () => pick(r),
					}))}
				/>
			)}
			{custom && (
				<CustomRangeDialog
					params={params}
					onClose={() => setCustom(false)}
					onApply={(from, to) => {
						setCustom(false);
						onChange({ range: "custom", from, to });
					}}
				/>
			)}
		</>
	);
}

/** 已选筛选的胶囊：名称取项目 / 空间列表、线程索引与模型短名；`none` = 无项目 / 无空间。 */
function FilterChips({
	params,
	keys,
	onChange,
}: {
	params: StatsParams;
	keys: readonly FilterKey[];
	onChange: (patch: Partial<StatsParams>) => void;
}): ReactNode {
	const { t } = useTranslation();
	const projects = useProjects();
	const spaces = useSpaces();
	const threads = useIndex((s) => s.threads);
	const nameOf = (k: FilterKey, v: string): string => {
		if (v === "none" && (k === "project" || k === "space"))
			return t(`stats.emptyKey.${k}`);
		if (k === "project")
			return projects.data?.projects.find((p) => p.id === v)?.name ?? v;
		if (k === "space")
			return spaces.data?.spaces?.find((s) => s.id === v)?.name ?? v;
		if (k === "thread") return threads[v]?.title || v.slice(0, 12);
		return groupName("model", { key: v }, t).name;
	};
	const active = FILTER_KEYS.filter((k) => keys.includes(k) && params[k]);
	if (active.length === 0) return null;
	return (
		<ul className="flex min-w-0 flex-wrap items-center gap-1.5">
			{active.map((k) => {
				const name = nameOf(k, params[k] ?? "");
				return (
					<li
						key={k}
						data-filter={k}
						className="inline-flex h-7 min-w-0 max-w-64 items-center gap-1 rounded-full border border-border bg-panel pr-0.5 pl-2.5 text-xs"
					>
						<span className="shrink-0 text-faint">
							{t(`stats.filter.${k}`)}
						</span>
						<span className="min-w-0 truncate text-fg">{name}</span>
						<IconButton
							icon="close"
							box="xs"
							size={12}
							label={t("stats.filter.clear", { name })}
							onClick={() => onChange({ [k]: undefined })}
						/>
					</li>
				);
			})}
		</ul>
	);
}

/** 导出菜单：顶层浮层（ui/Floating）里三条带 `download` 的链接；外点 / Esc 关闭。 */
function ExportMenu({ links }: { links: ExportLinks }): ReactNode {
	const { t } = useTranslation();
	const [open, setOpen] = useState(false);
	const ref = useRef<HTMLDivElement>(null);
	useEffect(() => {
		if (!open) return;
		const onDown = (e: MouseEvent) => {
			if (ref.current && !ref.current.contains(e.target as Node))
				setOpen(false);
		};
		const onKey = (e: KeyboardEvent) => {
			if (e.key === "Escape") setOpen(false);
		};
		document.addEventListener("mousedown", onDown);
		window.addEventListener("keydown", onKey);
		return () => {
			document.removeEventListener("mousedown", onDown);
			window.removeEventListener("keydown", onKey);
		};
	}, [open]);
	const items = [
		{ key: "view-csv", href: links.viewCsv, label: t("stats.export.viewCsv") },
		{
			key: "view-json",
			href: links.viewJson,
			label: t("stats.export.viewJson"),
		},
		{
			key: "records-csv",
			href: links.recordsCsv,
			label: t("stats.export.recordsCsv"),
		},
	];
	return (
		<div ref={ref} className="relative shrink-0">
			<Button
				size="sm"
				icon="download"
				data-testid="stats-export"
				aria-haspopup="menu"
				aria-expanded={open}
				onClick={() => setOpen((v) => !v)}
			>
				{t("stats.export.button")}
			</Button>
			{open && (
				<Floating
					role="menu"
					align="end"
					className="min-w-52"
					bodyClassName="p-1"
				>
					{items.map((it) => (
						<a
							key={it.key}
							role="menuitem"
							href={it.href}
							download
							data-export={it.key}
							onClick={() => setOpen(false)}
							className="flex w-full items-center gap-2 rounded-md px-2 py-1.5 text-fg text-sm hover:bg-hover"
						>
							<Icon name="download" size={14} className="shrink-0 text-muted" />
							<span className="min-w-0 flex-1 truncate">{it.label}</span>
						</a>
					))}
				</Floating>
			)}
		</div>
	);
}

/** 工具行：范围、筛选胶囊、（可选）预算入口、导出。 */
export function StatsToolbar({
	params,
	filterKeys,
	onChange,
	links,
	extra,
}: {
	params: StatsParams;
	/** 本标签认的筛选（其余不显示胶囊） */
	filterKeys: readonly FilterKey[];
	onChange: (patch: Partial<StatsParams>) => void;
	links: ExportLinks;
	/** 导出旁的额外按钮（用量标签没设预算时的「预算…」） */
	extra?: ReactNode;
}): ReactNode {
	return (
		<div className="flex min-w-0 flex-wrap items-center gap-2">
			<RangePicker params={params} onChange={onChange} />
			<FilterChips params={params} keys={filterKeys} onChange={onChange} />
			<span className="min-w-0 flex-1" />
			{extra}
			<ExportMenu links={links} />
		</div>
	);
}
