/**
 * Space 的线程（右列 / 窄屏「线程」标签）：本 Space 的工作线程分三段——「需要你」（等待输入、出错）、「进行中」、
 * 「已结束（近 7 天归档）」，每项是实时的 `ThreadCard`（数据取线程索引，随推送变化）。卡片菜单有「移出空间」与
 * 「归档为已交付」；底部「加入线程」搜索自己的线程并 `PATCH {spaceId}`。
 */
import { type ReactNode, useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import type { Thread } from "../../api/client";
import { isCoordinatorThread } from "../../lib/thread-kind";
import { parseTitleCard } from "../../lib/title";
import { patchThread } from "../../state/actions";
import { useIndex } from "../../state/stores";
import { Icon } from "../ui/icons";
import {
	Button,
	describeError,
	Menu,
	TextInput,
	ThreadStateIcon,
} from "../ui/primitives";
import { ThreadCard } from "./ThreadCard";

const DONE_WINDOW_MS = 7 * 24 * 3600_000;

export interface SpaceThreadGroups {
	needYou: Thread[];
	active: Thread[];
	done: Thread[];
}

/** 本 Space 的工作线程分三段（纯函数）：未归档的按最近活动排序，已结束的按归档时间排序。 */
export function groupSpaceThreads(
	threads: readonly Thread[],
	spaceId: string,
	now: number,
): SpaceThreadGroups {
	const mine = threads.filter(
		(t) => t.spaceId === spaceId && !isCoordinatorThread(t),
	);
	const recent = (t: Thread) => t.lastMessageAt ?? t.updatedAt;
	const open = mine
		.filter((t) => !t.archivedAt)
		.sort((a, b) => recent(b) - recent(a));
	return {
		needYou: open.filter(
			(t) => t.state === "awaiting_input" || t.state === "error",
		),
		active: open.filter(
			(t) => t.state !== "awaiting_input" && t.state !== "error",
		),
		done: mine
			.filter((t) => t.archivedAt && now - t.archivedAt <= DONE_WINDOW_MS)
			.sort((a, b) => (b.archivedAt ?? 0) - (a.archivedAt ?? 0)),
	};
}

function CardMenu({ thread }: { thread: Thread }): ReactNode {
	const { t } = useTranslation();
	const [error, setError] = useState<unknown>(null);
	const act = (fn: () => Promise<unknown>) => () => {
		setError(null);
		fn().catch(setError);
	};
	return (
		<span className="flex items-center gap-1">
			{error ? (
				<span className="max-w-32 truncate text-err text-xs">
					{describeError(error, t)}
				</span>
			) : null}
			<Menu
				items={[
					...(thread.archivedAt
						? []
						: [
								{
									key: "ship",
									icon: "rocket" as const,
									label: t("thread.archiveShipped"),
									onSelect: act(() =>
										patchThread(thread.id, { archived: true, shipped: true }),
									),
								},
							]),
					{
						key: "remove",
						icon: "close",
						label: t("space.removeThread"),
						onSelect: act(() => patchThread(thread.id, { spaceId: "" })),
					},
				]}
				trigger={(p) => (
					<button
						type="button"
						aria-label={t("space.threadMenu")}
						className="inline-flex h-7 w-7 items-center justify-center rounded-md text-muted opacity-70 transition-[opacity,background-color] duration-150 hover:bg-hover hover:text-fg hover:opacity-100 focus-visible:opacity-100 group-hover:opacity-100"
						{...p}
					>
						<Icon name="more" size={15} />
					</button>
				)}
			/>
		</span>
	);
}

function Section({
	title,
	threads,
}: {
	title: string;
	threads: readonly Thread[];
}): ReactNode {
	if (threads.length === 0) return null;
	return (
		<section className="flex flex-col gap-1.5">
			<h3 className="flex items-center gap-1.5 font-semibold text-muted text-xs">
				{title}
				<span className="text-faint tabular-nums">{threads.length}</span>
			</h3>
			{threads.map((th) => (
				<ThreadCard
					key={th.id}
					threadId={th.id}
					actions={<CardMenu thread={th} />}
				/>
			))}
		</section>
	);
}

/** 「加入线程」：在自己的未归档工作线程里搜索，点选即加入本 Space。 */
function AddThread({ spaceId }: { spaceId: string }): ReactNode {
	const { t } = useTranslation();
	const threads = useIndex((s) => s.threads);
	const [open, setOpen] = useState(false);
	const [q, setQ] = useState("");
	const [error, setError] = useState<unknown>(null);
	const candidates = useMemo(() => {
		const query = q.trim().toLowerCase();
		return Object.values(threads)
			.filter(
				(th) =>
					!th.archivedAt &&
					!isCoordinatorThread(th) &&
					th.spaceId !== spaceId &&
					(!query || th.title.toLowerCase().includes(query)),
			)
			.sort(
				(a, b) =>
					(b.lastMessageAt ?? b.updatedAt) - (a.lastMessageAt ?? a.updatedAt),
			)
			.slice(0, 8);
	}, [threads, q, spaceId]);
	if (!open)
		return (
			<Button size="sm" icon="plus" onClick={() => setOpen(true)}>
				{t("space.addThread")}
			</Button>
		);
	return (
		<div className="flex flex-col gap-1.5 rounded-lg border border-border bg-raised p-2">
			<span className="flex items-center gap-1.5">
				<TextInput
					autoFocus
					aria-label={t("space.addThreadSearch")}
					placeholder={t("space.addThreadSearch")}
					value={q}
					onChange={(e) => setQ(e.target.value)}
				/>
				<Button size="sm" variant="ghost" onClick={() => setOpen(false)}>
					{t("common.close")}
				</Button>
			</span>
			{candidates.length === 0 && (
				<p className="px-1 text-faint text-xs">{t("space.addThreadEmpty")}</p>
			)}
			{candidates.map((th) => (
				<button
					key={th.id}
					type="button"
					className="flex min-h-8 items-center gap-2 rounded-md px-2 text-left text-sm hover:bg-hover"
					onClick={() => {
						setError(null);
						patchThread(th.id, { spaceId }).then(() => setQ(""), setError);
					}}
				>
					<ThreadStateIcon state={th.state} size={13} />
					<span className="min-w-0 truncate">
						{parseTitleCard(th.title).text || t("thread.untitled")}
					</span>
				</button>
			))}
			{error ? (
				<p role="alert" className="px-1 text-err text-xs">
					{describeError(error, t)}
				</p>
			) : null}
		</div>
	);
}

export function SpaceThreads({ spaceId }: { spaceId: string }): ReactNode {
	const { t } = useTranslation();
	const threads = useIndex((s) => s.threads);
	const groups = useMemo(
		() => groupSpaceThreads(Object.values(threads), spaceId, Date.now()),
		[threads, spaceId],
	);
	const empty =
		groups.needYou.length + groups.active.length + groups.done.length === 0;
	return (
		<div data-testid="space-threads" className="flex flex-col gap-4">
			{empty && <p className="text-faint text-sm">{t("space.noThreads")}</p>}
			<Section title={t("space.needYou")} threads={groups.needYou} />
			<Section title={t("space.active")} threads={groups.active} />
			<Section title={t("space.done")} threads={groups.done} />
			<div>
				<AddThread spaceId={spaceId} />
			</div>
		</div>
	);
}
