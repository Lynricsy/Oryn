/**
 * 全局助手页（M9，`Ctrl/Cmd+/`）：`/assistant` 跳到最近一个未归档的助手对话（不含主线），没有就新建一个；
 * `/assistant/<id>` 是该对话（`ThreadScreen variant="coordinator"`）。页头是助手的圆形小头像 + 标题卡图标 + 对话标题 + 标题卡代码徽标 +
 * 文件库按钮（M13，对话框里是 `LibraryPanel`）+ 动作菜单（新对话、归档此对话；助手记忆可用时还有「不进助手记忆」切换，M17）；切换对话在侧栏的助手分区里做（M9.6）。协调宿主不可用（`GET coordinator` 的
 * `available === false`）时不创建对话：页面主体是「全局助手暂不可用」+ 原因 + 指向设置 → 关于；
 * 已有对话仍可只读浏览（输入区禁用，托盘里同样提示）。
 * 主线（M19）：页头标题是助手的名字，菜单只有「新对话」，另有「备忘」（1500 字，助手每段开头都会读到）、「提醒」
 * （主线上的多条线程内定时）与「跟进」（M26，后台研究的话题，`FollowUpsDialog`）三个按钮；订阅只收最近
 * {@link MAIN_TAIL} 条，转录顶部逐页加载更早的，转录用主线模式。
 */
import { type ReactNode, useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { Link, Navigate, useNavigate, useParams } from "react-router";
import {
	NOTES_LIMIT,
	useAssistantNotes,
	usePutAssistantNotes,
} from "../api/assistant-queries";
import { useCoordinatorStatus } from "../api/coordinator-queries";
import { useAssistantIdentity } from "../api/identity-queries";
import {
	useDeleteOwnerMemoryExclusion,
	useOwnerMemoryExclusions,
	useOwnerMemorySettings,
	usePutOwnerMemoryExclusion,
} from "../api/owner-memory-queries";
import { type Schedule, useSchedules } from "../api/schedule-queries";
import { createAssistantConversation } from "../api/space-queries";
import { AssistantMark } from "../components/assistant/AssistantMark";
import { FollowUpsDialog } from "../components/assistant/FollowUpsDialog";
import { LibraryPanel } from "../components/assistant/LibraryPanel";
import { ScheduleDialog } from "../components/schedule/ScheduleDialog";
import { ScheduleRow } from "../components/schedule/ScheduleRow";
import { CoordinatorHeader } from "../components/thread/CoordinatorHeader";
import { Badge, Callout, Sheet, TitleGlyph } from "../components/ui/data";
import {
	Button,
	Dialog,
	describeError,
	ErrorText,
	IconButton,
	Menu,
	type MenuItem,
	Spinner,
	TextArea,
} from "../components/ui/primitives";
import {
	useArchiveAssistantConversation,
	useAssistantConversations,
} from "../lib/assistant-conversations";
import { parseTitleCard } from "../lib/title";
import { useBreakpoint } from "../lib/viewport";
import { useIndex } from "../state/stores";
import { uiStore, useSidebarToggleShown } from "../state/ui";
import { type ScreenHeaderLayout, ThreadScreen } from "./ThreadPage";

function Unavailable({ reason }: { reason?: string }): ReactNode {
	const { t } = useTranslation();
	return (
		<Callout
			tone="warn"
			role="status"
			title={t("coordinator.unavailableTitle")}
		>
			<span className="block break-words">{reason}</span>
			<Link
				to="/settings/about"
				className="mt-1 inline-block text-accent-ink underline"
			>
				{t("coordinator.seeAbout")}
			</Link>
		</Callout>
	);
}

/** 主线订阅的尾部窗口：从头补发时只收最近这么多条。 */
const MAIN_TAIL = 600;

/** 主线的备忘：整段改写，带字数（上限同服务端）；超长时提示压缩、不能保存。 */
function NotesDialog({ onClose }: { onClose: () => void }): ReactNode {
	const { t } = useTranslation();
	const notes = useAssistantNotes();
	const put = usePutAssistantNotes();
	// null = 还没改动，显示服务端的版本
	const [draft, setDraft] = useState<string | null>(null);
	const text = draft ?? notes.data?.text ?? "";
	const count = [...text].length;
	const over = count > NOTES_LIMIT;
	return (
		<Dialog
			open
			onClose={onClose}
			title={t("mainline.notes.title")}
			footer={
				<>
					<Button onClick={onClose}>{t("common.cancel")}</Button>
					<Button
						variant="primary"
						disabled={draft === null || over || put.isPending}
						onClick={() => put.mutate(text, { onSuccess: onClose })}
					>
						{t("common.save")}
					</Button>
				</>
			}
		>
			<div data-testid="main-notes" className="flex flex-col gap-2">
				<p className="text-muted text-xs">{t("mainline.notes.hint")}</p>
				{notes.isPending ? (
					<div className="flex justify-center py-4 text-muted">
						<Spinner size={16} />
					</div>
				) : (
					<TextArea
						rows={10}
						className="font-sans"
						aria-label={t("mainline.notes.title")}
						aria-invalid={over || undefined}
						value={text}
						onChange={(e) => setDraft(e.target.value)}
					/>
				)}
				<div className="flex items-start justify-between gap-3 text-xs">
					<span role={over ? "alert" : undefined} className="text-err">
						{over ? t("mainline.notes.tooLong", { limit: NOTES_LIMIT }) : ""}
					</span>
					<span
						data-testid="notes-count"
						className={`shrink-0 tabular-nums ${over ? "text-err" : "text-faint"}`}
					>
						{count} / {NOTES_LIMIT}
					</span>
				</div>
				<ErrorText error={notes.error ?? put.error} />
			</div>
		</Dialog>
	);
}

/**
 * 主线的提醒：主线上的线程内定时（可有多条，按名字区分）。每行可启停、编辑、删除（删除先确认，再 `DELETE schedules/{id}`）；
 * 「新建」打开定时对话框（目标是主线，名称必填）。编辑时先收起这个列表，定时对话框关掉后再回来。
 */
function RemindersDialog({
	threadId,
	onClose,
}: {
	threadId: string;
	onClose: () => void;
}): ReactNode {
	const { t } = useTranslation();
	const schedules = useSchedules({ thread: threadId });
	const [editing, setEditing] = useState<Schedule | "new" | null>(null);
	const list = schedules.data?.schedules ?? [];
	return (
		<>
			<Dialog
				open={editing === null}
				wide
				onClose={onClose}
				title={t("mainline.reminders.title")}
				footer={
					<>
						<Button icon="plus" onClick={() => setEditing("new")}>
							{t("mainline.reminders.new")}
						</Button>
						<Button onClick={onClose}>{t("common.close")}</Button>
					</>
				}
			>
				<div data-testid="main-reminders" className="flex flex-col gap-2">
					<p className="text-muted text-xs">{t("mainline.reminders.hint")}</p>
					{schedules.isPending && <Spinner size={14} />}
					<ErrorText error={schedules.error} />
					{!schedules.isPending && list.length === 0 && (
						<p className="text-faint text-sm">
							{t("mainline.reminders.empty")}
						</p>
					)}
					{list.length > 0 && (
						<ul className="-mx-2 flex flex-col">
							{list.map((s) => (
								<ScheduleRow
									key={s.id}
									schedule={s}
									onEdit={() => setEditing(s)}
								/>
							))}
						</ul>
					)}
				</div>
			</Dialog>
			{editing !== null && (
				<ScheduleDialog
					{...(editing === "new" ? {} : { scheduleId: editing.id })}
					target={{ mode: "thread", threadId }}
					onClose={() => setEditing(null)}
				/>
			)}
		</>
	);
}

/** `/assistant`：选最近的对话，或在可用时新建一个。 */
function AssistantLanding(): ReactNode {
	const { t } = useTranslation();
	const navigate = useNavigate();
	const bp = useBreakpoint();
	const sidebarToggle = useSidebarToggleShown();
	const loaded = useIndex((s) => s.loaded);
	const latest = useAssistantConversations()[0];
	const { name } = useAssistantIdentity();
	const status = useCoordinatorStatus();
	const [error, setError] = useState<unknown>(null);
	const started = useRef(false);
	const available = status.data?.available;
	useEffect(() => {
		if (!loaded || latest || available !== true || started.current) return;
		started.current = true;
		createAssistantConversation().then(
			(th) => navigate(`/assistant/${th.id}`, { replace: true }),
			setError,
		);
	}, [loaded, latest, available, navigate]);
	if (latest) return <Navigate to={`/assistant/${latest.id}`} replace />;
	return (
		<Sheet
			inset={bp === "expanded"}
			className="flex h-full min-w-0 flex-1 flex-col"
		>
			<header className="flex h-12 shrink-0 items-center gap-2 px-3">
				{sidebarToggle && (
					<IconButton
						icon={bp === "expanded" ? "sidebar" : "menu"}
						label={t("nav.sidebar")}
						onClick={() => uiStore.getState().toggleSidebar()}
					/>
				)}
				<AssistantMark size={18} />
				<h1 className="min-w-0 truncate font-semibold text-sm">
					{name || t("assistant.title")}
				</h1>
			</header>
			<div className="flex flex-1 items-start justify-center overflow-y-auto p-4 sm:items-center">
				<div className="w-full max-w-md" data-testid="assistant-landing">
					{available === false ? (
						<Unavailable reason={status.data?.reason} />
					) : error ? (
						<Callout
							tone="err"
							role="alert"
							title={t("assistant.createFailed")}
						>
							{describeError(error, t)}
						</Callout>
					) : status.isError ? (
						<Callout tone="err" role="alert">
							{describeError(status.error, t)}
						</Callout>
					) : (
						<div className="flex justify-center text-muted">
							<Spinner size={18} />
						</div>
					)}
				</div>
			</div>
		</Sheet>
	);
}

function AssistantHeader({
	layout,
	threadId,
	available,
}: {
	layout: ScreenHeaderLayout;
	threadId: string;
	available: boolean;
}): ReactNode {
	const { t } = useTranslation();
	const navigate = useNavigate();
	const current = useIndex((s) => s.threads[threadId]);
	const main = current?.main === true;
	const { name } = useAssistantIdentity();
	const archive = useArchiveAssistantConversation();
	const [error, setError] = useState<unknown>(null);
	// 记下是在哪个对话里打开的：从面板里跳到别的对话后自动收起
	const [libraryFor, setLibraryFor] = useState<string | null>(null);
	const [notesFor, setNotesFor] = useState<string | null>(null);
	const [remindersFor, setRemindersFor] = useState<string | null>(null);
	const [followUpsFor, setFollowUpsFor] = useState<string | null>(null);
	// 主线标题是助手的名字；其它对话的 omp 标题卡拆成 图标 + 标题 + 代码徽标（同线程页头）
	const card = !main && current ? parseTitleCard(current.title) : undefined;
	const title = main
		? name || t("tabBar.assistant")
		: card
			? card.text || t("assistant.untitled")
			: t("assistant.title");
	// 助手记忆（M17）：工程记忆可用时，菜单里可切换本对话「不进助手记忆」（已写入的随之遗忘）
	const ownerMemory = useOwnerMemorySettings();
	const ownerMemoryAvailable = ownerMemory.data?.available === true;
	const exclusions = useOwnerMemoryExclusions(ownerMemoryAvailable);
	const exclude = usePutOwnerMemoryExclusion();
	const include = useDeleteOwnerMemoryExclusion();
	const excluded = (exclusions.data?.items ?? []).some(
		(e) => e.kind === "thread" && e.targetId === threadId,
	);
	const newItem: MenuItem = {
		key: "new",
		icon: "plus",
		label: t("assistant.new"),
		disabled: !available,
		onSelect: () => {
			setError(null);
			createAssistantConversation().then(
				(th) => navigate(`/assistant/${th.id}`),
				setError,
			);
		},
	};
	// 主线不能归档，也不进助手记忆的近况：菜单只留「新对话」
	const items: MenuItem[] = main
		? [newItem]
		: [
				newItem,
				{
					key: "archive",
					icon: "archive",
					label: t("assistant.archive"),
					onSelect: () => {
						setError(null);
						// 完成时按最新路由决定是否回 landing（等待期间用户可能已切到别的对话），并同步已归档列表
						archive(threadId).catch(setError);
					},
				},
				...(ownerMemoryAvailable && exclusions.data
					? [
							{
								key: "owner-memory",
								icon: "memory",
								label: t("assistant.ownerMemoryExclude"),
								checked: excluded,
								disabled: exclude.isPending || include.isPending,
								onSelect: () => {
									setError(null);
									const target = {
										kind: "thread",
										targetId: threadId,
									} as const;
									if (excluded) include.mutate(target, { onError: setError });
									else exclude.mutate(target, { onError: setError });
								},
							} satisfies MenuItem,
						]
					: []),
			];
	return (
		<CoordinatorHeader
			layout={layout}
			title={
				<>
					<AssistantMark size={18} />
					{card?.icon && <TitleGlyph icon={card.icon} size={18} />}
					<h1 className="min-w-0 truncate font-semibold text-sm tracking-tight">
						{title}
					</h1>
					{card?.code && (
						<span
							aria-hidden
							className="hidden shrink-0 font-normal @2xl/header:inline-flex"
						>
							<Badge>{card.code}</Badge>
						</span>
					)}
				</>
			}
			actions={
				<>
					{error ? (
						<span className="max-w-48 truncate text-err text-xs" role="alert">
							{describeError(error, t)}
						</span>
					) : null}
					{main && (
						<>
							<IconButton
								icon="fileText"
								label={t("mainline.notes.title")}
								onClick={() => setNotesFor(threadId)}
							/>
							<IconButton
								icon="clock"
								label={t("mainline.reminders.title")}
								onClick={() => setRemindersFor(threadId)}
							/>
							<IconButton
								icon="search"
								label={t("mainline.followUps.title")}
								onClick={() => setFollowUpsFor(threadId)}
							/>
						</>
					)}
					<IconButton
						icon="folder"
						label={t("library.title")}
						onClick={() => setLibraryFor(threadId)}
					/>
					<Menu
						items={items}
						trigger={(p) => (
							<IconButton icon="more" label={t("assistant.actions")} {...p} />
						)}
					/>
					{/* 对话框经 portal 渲染到 body，放在动作槽里只为就近持有状态 */}
					<Dialog
						open={libraryFor === threadId}
						wide
						onClose={() => setLibraryFor(null)}
						title={t("library.title")}
					>
						<LibraryPanel threadId={threadId} />
					</Dialog>
					{main && notesFor === threadId && (
						<NotesDialog onClose={() => setNotesFor(null)} />
					)}
					{main && remindersFor === threadId && (
						<RemindersDialog
							threadId={threadId}
							onClose={() => setRemindersFor(null)}
						/>
					)}
					{main && followUpsFor === threadId && (
						<FollowUpsDialog onClose={() => setFollowUpsFor(null)} />
					)}
				</>
			}
		/>
	);
}

export function AssistantPage(): ReactNode {
	const { threadId } = useParams();
	const status = useCoordinatorStatus();
	const main = useIndex((s) =>
		threadId ? s.threads[threadId]?.main === true : false,
	);
	const unavailable = status.data?.available === false;
	if (!threadId) return <AssistantLanding />;
	return (
		<div data-testid="assistant-page" className="flex h-full min-w-0 flex-1">
			<ThreadScreen
				threadId={threadId}
				variant="coordinator"
				disabled={unavailable}
				mainline={main}
				tail={main ? MAIN_TAIL : undefined}
				dockNotice={
					unavailable ? (
						<div className="px-3 pt-3">
							<Unavailable reason={status.data?.reason} />
						</div>
					) : undefined
				}
				header={(layout) => (
					<AssistantHeader
						layout={layout}
						threadId={threadId}
						available={!unavailable}
					/>
				)}
			/>
		</div>
	);
}
