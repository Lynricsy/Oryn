/**
 * 排队条：停靠在输入坞输入框上方，显示 steering（引导）与 follow-up（排队）队列。
 * - 支持乐观项：用户在忙时发送引导/排队消息后立即可见，待权威 `queue_update` 到达无缝接管。
 * - 支持编辑、删除、拖动排序（跨队列）、提前发送、清空。
 */
import { type ReactNode, useState } from "react";
import { useTranslation } from "react-i18next";
import type { OmpCommand, RpcQueuedMessage } from "../../omp/types";
import { realtime } from "../../realtime/socket";
import { getThreadStore, useThread } from "../../state/stores";
import type {
	OptimisticQueuedMessage,
	QueueLane,
} from "../../state/thread-reducer";
import { Badge } from "../ui/data";
import { Icon } from "../ui/icons";
import { Button, IconButton, TextInput } from "../ui/primitives";

type DisplayItem = RpcQueuedMessage & { pending?: boolean };

async function run(threadId: string, frame: OmpCommand) {
	await realtime.cmd(threadId, frame);
	const data = await realtime.cmd(threadId, { type: "get_queue" });
	getThreadStore(threadId).getState().patchLocal({ queueItems: data });
}

function mergeLane(
	lane: QueueLane,
	authoritative: RpcQueuedMessage[] | undefined,
	texts: readonly string[] | undefined,
	optimistic: readonly OptimisticQueuedMessage[],
): DisplayItem[] {
	const base: DisplayItem[] =
		authoritative ??
		(texts ?? []).map((text, i) => ({
			id: `${lane === "steering" ? "s" : "f"}${i}`,
			text,
			editable: false,
			timestamp: 0,
		}));
	const mine = optimistic.filter((o) => o.lane === lane);
	if (mine.length === 0) return base;
	const counts = new Map<string, number>();
	for (const b of base) counts.set(b.text, (counts.get(b.text) ?? 0) + 1);
	const extra: DisplayItem[] = [];
	for (const o of mine) {
		const left = counts.get(o.text) ?? 0;
		if (left > 0) {
			counts.set(o.text, left - 1);
			continue;
		}
		extra.push({
			id: o.id,
			text: o.text,
			images: o.images,
			editable: false,
			timestamp: o.timestamp,
			pending: true,
		});
	}
	return [...base, ...extra];
}

function Item({
	threadId,
	item,
	lane,
	index,
}: {
	threadId: string;
	item: DisplayItem;
	lane: QueueLane;
	index: number;
}) {
	const { t } = useTranslation();
	const [editing, setEditing] = useState<string | null>(null);
	const isSteer = lane === "steering";
	return (
		<li
			draggable={item.editable}
			data-queue-lane={lane}
			data-queue-pending={item.pending ? "true" : undefined}
			onDragStart={(e) =>
				e.dataTransfer.setData(
					"text/x-oryn-queue",
					JSON.stringify({ id: item.id }),
				)
			}
			onDragOver={(e) => e.preventDefault()}
			onDrop={(e) => {
				e.preventDefault();
				if (item.pending) return;
				const raw = e.dataTransfer.getData("text/x-oryn-queue");
				if (!raw) return;
				const { id } = JSON.parse(raw) as { id: string };
				if (id !== item.id)
					void run(threadId, {
						type: "queue_move",
						queueId: id,
						toIndex: index,
						toLane: lane,
					});
			}}
			className={`flex items-center gap-1.5 rounded-lg border py-1.5 pr-1.5 pl-2.5 text-xs transition-colors duration-150 ${
				isSteer
					? "border-accent/25 bg-accent-soft/55"
					: "border-border bg-panel/70"
			} ${item.pending ? "opacity-80" : ""}`}
		>
			{item.editable && (
				<span
					className="cursor-grab select-none text-ghost active:cursor-grabbing"
					aria-hidden="true"
				>
					⋮⋮
				</span>
			)}
			<span
				className={`inline-flex shrink-0 items-center ${isSteer ? "text-accent-ink" : "text-faint"}`}
				aria-hidden="true"
			>
				<Icon
					name={isSteer ? "bolt" : "clock"}
					size={12}
					weight={isSteer ? "fill" : "regular"}
				/>
			</span>
			{editing !== null ? (
				<form
					className="flex min-w-0 flex-1 gap-1"
					onSubmit={(e) => {
						e.preventDefault();
						void run(threadId, {
							type: "queue_edit",
							queueId: item.id,
							text: editing,
						}).then(() => setEditing(null));
					}}
				>
					<TextInput
						autoFocus
						className="h-6 py-0 text-xs"
						value={editing}
						onChange={(e) => setEditing(e.target.value)}
					/>
					<Button size="sm" type="submit">
						{t("common.save")}
					</Button>
				</form>
			) : (
				<span className="min-w-0 flex-1 truncate text-fg" title={item.text}>
					{item.text}
				</span>
			)}
			{item.images && item.images.length > 0 && editing === null && (
				<span className="inline-flex shrink-0 items-center gap-0.5 rounded bg-inset px-1.5 py-0.5 text-2xs text-muted">
					<Icon name="image" size={11} />
					{t("queue.imagesCount", { count: item.images.length })}
				</span>
			)}
			{item.pending && (
				<span className="shrink-0 pr-1 text-2xs text-faint">
					{t("queue.sending")}
				</span>
			)}
			{item.editable && editing === null && (
				<div className="flex shrink-0 items-center gap-0.5">
					{lane === "followUp" && (
						<IconButton
							icon="bolt"
							size={13}
							box="xs"
							label={t("queue.promote")}
							onClick={() =>
								void run(threadId, {
									type: "promote_queued_message",
									message: item.text,
								})
							}
						/>
					)}
					<IconButton
						icon="edit"
						size={13}
						box="xs"
						label={t("queue.edit")}
						onClick={() => setEditing(item.text)}
					/>
					<IconButton
						icon="close"
						size={13}
						box="xs"
						label={t("queue.remove")}
						onClick={() =>
							void run(threadId, { type: "queue_remove", queueId: item.id })
						}
					/>
				</div>
			)}
		</li>
	);
}

export function QueueBar({ threadId }: { threadId: string }): ReactNode {
	const { t } = useTranslation();
	const queue = useThread(threadId, (s) => s.queue);
	const items = useThread(threadId, (s) => s.queueItems);
	const optimistic = useThread(threadId, (s) => s.optimisticQueue);
	const [open, setOpen] = useState(true);
	const steering = mergeLane(
		"steering",
		items?.steering,
		queue?.steering,
		optimistic,
	);
	const followUp = mergeLane(
		"followUp",
		items?.followUp,
		queue?.followUp,
		optimistic,
	);
	const total = steering.length + followUp.length;
	if (total === 0) return null;
	const hasAuthoritative =
		(queue?.steering.length ?? 0) + (queue?.followUp.length ?? 0) > 0 ||
		(items?.steering.length ?? 0) + (items?.followUp.length ?? 0) > 0;
	const lanes: { lane: QueueLane; list: DisplayItem[] }[] = [
		{ lane: "steering", list: steering },
		{ lane: "followUp", list: followUp },
	];
	return (
		<div data-testid="queue-bar" className="px-3.5 py-2.5">
			<div className="flex items-center gap-2 text-muted text-xs">
				<button
					type="button"
					className="-ml-1 flex items-center gap-1.5 rounded-md px-1 py-0.5 font-medium transition-colors duration-150 hover:bg-hover hover:text-fg"
					onClick={() => setOpen((v) => !v)}
					aria-expanded={open}
				>
					<Icon name={open ? "chevronDown" : "chevronRight"} size={12} />
					{t("queue.heading")}
					<Badge tone={steering.length > 0 ? "accent" : "neutral"}>
						{total}
					</Badge>
				</button>
				<div className="flex-1" />
				{hasAuthoritative && (
					<Button
						size="sm"
						variant="ghost"
						onClick={() => void run(threadId, { type: "queue_clear" })}
					>
						{t("queue.clear")}
					</Button>
				)}
			</div>
			{open && (
				<div className="mt-2 flex flex-col gap-2">
					{lanes.map(({ lane, list }) =>
						list.length === 0 ? null : (
							<div key={lane}>
								<div className="mb-1 flex items-center gap-1.5 font-medium text-2xs text-faint">
									<span>{t(`queue.${lane}`)}</span>
									<span className="tabular-nums opacity-75">{list.length}</span>
								</div>
								<ul className="flex flex-col gap-1">
									{list.map((item, i) => (
										<Item
											key={item.id}
											threadId={threadId}
											item={item}
											lane={lane}
											index={i}
										/>
									))}
								</ul>
							</div>
						),
					)}
				</div>
			)}
		</div>
	);
}
