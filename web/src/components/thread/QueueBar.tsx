/**
 * 排队条：显示 steering / follow-up 队列，支持编辑、删除、拖动排序（跨队列）、提前发送、清空。
 * 带 id 的队列项来自 `get_queue`（`queue_update` 后自动刷新）。
 */
import { type ReactNode, useState } from "react";
import { useTranslation } from "react-i18next";
import type { OmpCommand, RpcQueuedMessage } from "../../omp/types";
import { realtime } from "../../realtime/socket";
import { getThreadStore, useThread } from "../../state/stores";
import { Icon } from "../ui/icons";
import { Button, IconButton, TextInput } from "../ui/primitives";

type Lane = "steering" | "followUp";

async function run(threadId: string, frame: OmpCommand) {
	await realtime.cmd(threadId, frame);
	const data = await realtime.cmd(threadId, { type: "get_queue" });
	getThreadStore(threadId).getState().patchLocal({ queueItems: data });
}

function Item({
	threadId,
	item,
	lane,
	index,
}: {
	threadId: string;
	item: RpcQueuedMessage;
	lane: Lane;
	index: number;
}) {
	const { t } = useTranslation();
	const [editing, setEditing] = useState<string | null>(null);
	return (
		<li
			draggable={item.editable}
			onDragStart={(e) =>
				e.dataTransfer.setData(
					"text/x-oryn-queue",
					JSON.stringify({ id: item.id }),
				)
			}
			onDragOver={(e) => e.preventDefault()}
			onDrop={(e) => {
				e.preventDefault();
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
			className="flex items-center gap-1.5 rounded-md border border-border bg-panel px-2 py-1 text-xs"
		>
			{item.editable && (
				<span className="cursor-grab text-faint" aria-hidden="true">
					⋮⋮
				</span>
			)}
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
				<span className="min-w-0 flex-1 truncate">{item.text}</span>
			)}
			{item.editable && editing === null && (
				<>
					{lane === "followUp" && (
						<IconButton
							icon="bolt"
							size={13}
							className="h-6 w-6"
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
						className="h-6 w-6"
						label={t("queue.edit")}
						onClick={() => setEditing(item.text)}
					/>
					<IconButton
						icon="close"
						size={13}
						className="h-6 w-6"
						label={t("queue.remove")}
						onClick={() =>
							void run(threadId, { type: "queue_remove", queueId: item.id })
						}
					/>
				</>
			)}
		</li>
	);
}

export function QueueBar({ threadId }: { threadId: string }): ReactNode {
	const { t } = useTranslation();
	const queue = useThread(threadId, (s) => s.queue);
	const items = useThread(threadId, (s) => s.queueItems);
	const [open, setOpen] = useState(true);
	const total = (queue?.steering.length ?? 0) + (queue?.followUp.length ?? 0);
	if (total === 0) return null;
	const lanes: { lane: Lane; list: RpcQueuedMessage[] }[] = [
		{
			lane: "steering",
			list:
				items?.steering ??
				(queue?.steering ?? []).map((text, i) => ({
					id: `s${i}`,
					text,
					editable: false,
					timestamp: 0,
				})),
		},
		{
			lane: "followUp",
			list:
				items?.followUp ??
				(queue?.followUp ?? []).map((text, i) => ({
					id: `f${i}`,
					text,
					editable: false,
					timestamp: 0,
				})),
		},
	];
	return (
		<div className="mx-auto mb-2 w-full max-w-3xl rounded-lg border border-border bg-raised px-2 py-1.5">
			<div className="flex items-center gap-2 text-muted text-xs">
				<button
					type="button"
					className="flex items-center gap-1"
					onClick={() => setOpen((v) => !v)}
					aria-expanded={open}
				>
					<Icon name={open ? "chevronDown" : "chevronRight"} size={12} />
					{t("queue.title", { count: total })}
				</button>
				<div className="flex-1" />
				<Button
					size="sm"
					variant="ghost"
					onClick={() => void run(threadId, { type: "queue_clear" })}
				>
					{t("queue.clear")}
				</Button>
			</div>
			{open && (
				<div className="mt-1 flex flex-col gap-1.5">
					{lanes.map(({ lane, list }) =>
						list.length === 0 ? null : (
							<div key={lane}>
								<div className="mb-0.5 text-2xs text-faint uppercase tracking-wide">
									{t(`queue.${lane}`)}
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
