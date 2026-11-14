/**
 * Space 页（M9）：Space = 名称 + 目标 + 默认项目 + 一个协调聊天。
 * expanded：聊天列（`ThreadScreen variant="coordinator"`）+ 右列 320px（进度图 → 定时 → 线程）。
 * medium / compact：页头下一条两项标签「聊天 | 线程」，「线程」标签里是进度图、定时与线程列表；聊天切走时保持挂载。
 * 页头：Space 名、可点开编辑的目标、项目徽标、菜单（编辑 / 归档 / 删除）。
 * 进度图在本 Space 线程的归档 / 交付 / 新建变化时重取（Space 没有实时推送，线程索引有）。
 */
import { useQueryClient } from "@tanstack/react-query";
import { type ReactNode, useEffect, useMemo, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { useNavigate, useParams } from "react-router";
import { useProjects } from "../api/queries";
import {
	type Space,
	useDeleteSpace,
	useSpace,
	useSpaceProgress,
	useUpdateSpace,
} from "../api/space-queries";
import { SpaceDialog } from "../components/space/SpaceDialog";
import { SpaceProgress } from "../components/space/SpaceProgress";
import { SpaceSchedules } from "../components/space/SpaceSchedules";
import { SpaceThreads } from "../components/space/SpaceThreads";
import { CoordinatorHeader } from "../components/thread/CoordinatorHeader";
import { HEADER_VEIL } from "../components/thread/ThreadHeader";
import { ProjectBadge, Sheet } from "../components/ui/data";
import { Icon } from "../components/ui/icons";
import {
	describeError,
	EmptyState,
	Menu,
	Spinner,
	tabClass,
} from "../components/ui/primitives";
import { useBreakpoint } from "../lib/viewport";
import { useIndex } from "../state/stores";
import { useSidebarShown } from "../state/ui";
import {
	DC_SHEET_RIGHT,
	type ScreenHeaderLayout,
	TAB_ROW,
	ThreadScreen,
} from "./ThreadPage";

type SpaceTab = "chat" | "threads";

/** 本 Space 线程的「进度签名」：归档、交付、增减时变化，用来让进度图重取。 */
function useProgressRefresh(spaceId: string) {
	const qc = useQueryClient();
	const sig = useIndex((s) =>
		Object.values(s.threads)
			.filter((t) => t.spaceId === spaceId)
			.map((t) => `${t.id}:${t.archivedAt ?? 0}:${t.shippedAt ?? 0}`)
			.sort()
			.join(","),
	);
	const seen = useRef<string | null>(null);
	useEffect(() => {
		// 首次只记下签名（查询自己会拉）；之后签名变化才重取
		if (seen.current !== null && seen.current !== sig)
			void qc.invalidateQueries({ queryKey: ["spaces", "progress", spaceId] });
		seen.current = sig;
	}, [sig, spaceId, qc]);
}

function SpaceTabs({
	tab,
	onTab,
}: {
	tab: SpaceTab;
	onTab: (tab: SpaceTab) => void;
}): ReactNode {
	const { t } = useTranslation();
	return (
		<div
			role="tablist"
			aria-label={t("space.tabs")}
			data-testid="space-tabs"
			className="flex items-center gap-1"
		>
			{(["chat", "threads"] as const).map((k) => (
				<button
					key={k}
					type="button"
					role="tab"
					aria-selected={tab === k}
					data-space-tab={k}
					className={`${tabClass(tab === k)} ${tab === k ? "bg-hover ring-1 ring-border" : ""}`}
					onClick={() => onTab(k)}
				>
					<Icon name={k === "chat" ? "chats" : "list"} size={14} aria-hidden />
					{t(`space.tab.${k}`)}
				</button>
			))}
		</div>
	);
}

function SpaceHeader({
	space,
	layout,
	onEdit,
}: {
	space: Space;
	layout: ScreenHeaderLayout;
	onEdit: () => void;
}): ReactNode {
	const { t } = useTranslation();
	const navigate = useNavigate();
	const projects = useProjects();
	const update = useUpdateSpace(space.id);
	const del = useDeleteSpace();
	const project = space.projectId
		? projects.data?.projects?.find((p) => p.id === space.projectId)
		: undefined;
	const error = update.error ?? del.error;
	return (
		<CoordinatorHeader
			layout={layout}
			title={
				<>
					<Icon
						name="target"
						size={16}
						aria-hidden
						className="shrink-0 text-faint"
					/>
					<h1 className="min-w-0 shrink truncate font-semibold text-sm tracking-tight">
						{space.name}
					</h1>
					{space.archivedAt && (
						<span className="shrink-0 text-faint text-xs">
							{t("space.archived")}
						</span>
					)}
					<button
						type="button"
						data-testid="space-goal"
						onClick={onEdit}
						title={space.goal || t("space.goalEmpty")}
						className="hidden min-w-0 shrink truncate rounded-md px-1.5 py-0.5 text-left text-muted text-xs hover:bg-hover hover:text-fg @lg/header:inline"
					>
						{space.goal || t("space.goalEmpty")}
					</button>
					{project && (
						<span
							className="hidden shrink-0 items-center gap-1.5 text-muted text-xs @md/header:inline-flex"
							title={project.name}
						>
							<ProjectBadge name={project.name} icon={project.icon} size={16} />
							<span className="max-w-28 truncate">{project.name}</span>
						</span>
					)}
				</>
			}
			actions={
				<>
					{error ? (
						<span className="max-w-40 truncate text-err text-xs">
							{describeError(error, t)}
						</span>
					) : null}
					<Menu
						items={[
							{
								key: "edit",
								icon: "edit",
								label: t("space.edit"),
								onSelect: onEdit,
							},
							{
								key: "archive",
								icon: "archive",
								label: space.archivedAt
									? t("space.unarchive")
									: t("space.archive"),
								onSelect: () => update.mutate({ archived: !space.archivedAt }),
							},
							{
								key: "delete",
								icon: "trash",
								danger: true,
								label: t("space.delete"),
								onSelect: () => {
									if (
										!window.confirm(
											t("space.deleteConfirm", { name: space.name }),
										)
									)
										return;
									void del.mutateAsync(space.id).then(
										() => navigate("/"),
										() => undefined,
									);
								},
							},
						]}
						trigger={(p) => (
							<button
								type="button"
								aria-label={t("space.menu")}
								className="inline-flex h-8 w-8 items-center justify-center rounded-md text-muted transition-colors duration-150 hover:bg-hover hover:text-fg"
								{...p}
							>
								<Icon name="more" size={16} />
							</button>
						)}
					/>
				</>
			}
		/>
	);
}

/** 右列 / 「线程」标签：进度图、定时、线程。 */
function SpaceSide({ space }: { space: Space }): ReactNode {
	const { t } = useTranslation();
	const progress = useSpaceProgress(space.id);
	return (
		<div className="flex flex-col gap-5">
			<section className="flex flex-col gap-2">
				<h3 className="font-semibold text-muted text-xs">
					{t("space.progress.title")}
				</h3>
				{progress.data ? (
					<SpaceProgress days={progress.data.days} />
				) : progress.isError ? (
					<p className="text-err text-xs">{describeError(progress.error, t)}</p>
				) : (
					<Spinner size={12} />
				)}
			</section>
			<SpaceSchedules space={space} />
			<SpaceThreads spaceId={space.id} />
		</div>
	);
}

export function SpacePage(): ReactNode {
	const { spaceId = "" } = useParams();
	const { t } = useTranslation();
	const bp = useBreakpoint();
	const expanded = bp === "expanded";
	const chromeLeft = !useSidebarShown();
	const space = useSpace(spaceId);
	const [tab, setTab] = useState<SpaceTab>("chat");
	const [editing, setEditing] = useState(false);
	useProgressRefresh(spaceId);
	// biome-ignore lint/correctness/useExhaustiveDependencies: 换 Space 时回到聊天标签
	useEffect(() => setTab("chat"), [spaceId]);
	const s = space.data;
	const header = useMemo(
		() =>
			s
				? (layout: ScreenHeaderLayout) => (
						<SpaceHeader
							space={s}
							layout={layout}
							onEdit={() => setEditing(true)}
						/>
					)
				: undefined,
		[s],
	);
	if (!s || !header)
		return (
			<Sheet
				inset={expanded}
				className="flex h-full min-w-0 flex-1 items-center justify-center"
			>
				{space.isPending ? (
					<span className="text-muted">
						<Spinner size={18} />
					</span>
				) : (
					<EmptyState icon="target" title={t("space.notFound")}>
						{space.error ? describeError(space.error, t) : null}
					</EmptyState>
				)}
			</Sheet>
		);
	const dialog = editing && (
		<SpaceDialog space={s} onClose={() => setEditing(false)} />
	);
	if (expanded)
		return (
			<div data-testid="space-page" className="flex h-full min-w-0 flex-1">
				<ThreadScreen
					threadId={s.threadId}
					variant="coordinator"
					header={header}
					edgeRight={false}
				/>
				<aside data-testid="space-side" className="ml-2 h-full w-80 shrink-0">
					<Sheet
						inset
						className={`h-full overflow-y-auto p-4 ${DC_SHEET_RIGHT}`}
					>
						<SpaceSide space={s} />
					</Sheet>
				</aside>
				{dialog}
			</div>
		);
	const tabs = <SpaceTabs tab={tab} onTab={setTab} />;
	const away = tab !== "chat";
	return (
		<div
			data-testid="space-page"
			className="relative flex h-full min-w-0 flex-1"
		>
			{/*
			 * 切到「线程」时聊天不用 display:none（会让 react-virtual 把在屏行测成 0、回来时阅读位置漂移，
			 * 见 ThreadPage 的转录面板）：叠在原处、仍参与布局，但透明且 inert（M9 审查 V1）。
			 */}
			<div
				data-testid="space-chat"
				inert={away}
				className={`flex h-full min-w-0 flex-1 ${away ? "pointer-events-none absolute inset-0 opacity-0" : "relative"}`}
			>
				<ThreadScreen
					threadId={s.threadId}
					variant="coordinator"
					header={header}
					subheader={tabs}
				/>
			</div>
			{tab === "threads" && (
				<Sheet inset={false} className="flex h-full min-w-0 flex-1 flex-col">
					<div className="relative z-10 shrink-0">
						{header({
							sidebarToggle: true,
							veil: HEADER_VEIL + TAB_ROW,
							chromeLeft,
							chromeRight: true,
						})}
						<div className="veil-host relative flex h-10 items-center px-2 sm:px-3">
							{tabs}
						</div>
					</div>
					<div
						data-testid="space-side"
						className="min-h-0 flex-1 overflow-y-auto px-3 pt-2 pb-safe sm:px-5"
					>
						<div className="mx-auto max-w-2xl pb-6">
							<SpaceSide space={s} />
						</div>
					</div>
				</Sheet>
			)}
			{dialog}
		</div>
	);
}
