/**
 * 侧栏线程行与收件箱行的上下文菜单（M22 步骤 4.1）：`threadRowGroups` 生成菜单组，`ThreadRowDialogs` 渲染菜单打开的对话框。
 * 状态一律读传入的线程对象（索引里的摘要），不读线程 store——非当前线程的 store 是空的。
 * 改名 / 标签 / fork 对话框的初值读线程 store，所以打开前先把线程摘要灌进去（与 `patchThread` 的做法相同）。
 */
import type { TFunction } from "i18next";
import { lazy, type ReactNode, Suspense } from "react";
import type { NavigateFunction } from "react-router";
import { useStore } from "zustand";
import { createStore } from "zustand/vanilla";
import type { Thread } from "../../api/client";
import { copyWithToast } from "../../lib/context-menu/selection-groups";
import type { MenuGroup } from "../../lib/context-menu/types";
import { host } from "../../lib/host";
import { deleteThread, patchThread } from "../../state/actions";
import { getThreadStore } from "../../state/stores";
import { showToast } from "../../state/toasts";
import { describeError, type MenuItem } from "../ui/primitives";
import { ArchiveDialog } from "./ArchiveDialog";
import { snoozeAt, THREAD_COLORS } from "./ThreadHeader";

// Dialogs.tsx 很大：只在菜单真的打开对话框时再加载
const RenameDialog = lazy(() =>
	import("./Dialogs").then((m) => ({ default: m.RenameDialog })),
);
const LabelsDialog = lazy(() =>
	import("./Dialogs").then((m) => ({ default: m.LabelsDialog })),
);
const ForkDialog = lazy(() =>
	import("./Dialogs").then((m) => ({ default: m.ForkDialog })),
);

type RowDialog =
	| { kind: "rename" | "labels" | "fork"; threadId: string }
	| { kind: "archive"; thread: Thread };

const dialogStore = createStore<{ dialog: RowDialog | null }>(() => ({
	dialog: null,
}));

function closeDialog(): void {
	dialogStore.setState({ dialog: null });
}

function openThreadDialog(
	kind: "rename" | "labels" | "fork",
	thread: Thread,
): void {
	getThreadStore(thread.id)
		.getState()
		.apply([{ type: "thread", thread }]);
	dialogStore.setState({ dialog: { kind, threadId: thread.id } });
}

const SNOOZE = ["1h", "3h", "tomorrow", "week"] as const;

export function threadRowGroups(
	thread: Thread,
	o: { to: string; navigate: NavigateFunction; t: TFunction },
): MenuGroup[] {
	const { t } = o;
	const id = thread.id;
	// 直接动作：失败时提示（菜单已关，没有别处显示错误）
	const run = (fn: () => Promise<unknown>) => () => {
		fn().catch((e: unknown) =>
			showToast({ text: describeError(e, t), tone: "err" }),
		);
	};
	const open: MenuItem[] = [
		{
			key: "open",
			icon: "message",
			label: t("contextMenu.open"),
			onSelect: () => o.navigate(o.to),
		},
	];
	// 两种壳里 window.open 都会跳去系统浏览器，只在网页里给
	if (host.kind === "web")
		open.push({
			key: "openNewTab",
			icon: "external",
			label: t("contextMenu.openNewTab"),
			onSelect: () => void window.open(o.to, "_blank", "noopener"),
		});
	const snoozeItems: MenuItem[] = SNOOZE.map((k) => ({
		key: k,
		label: t(`thread.snoozeFor.${k}`),
		onSelect: run(() => patchThread(id, { snoozedUntil: snoozeAt(k) })),
	}));
	if ((thread.snoozedUntil ?? 0) > Date.now())
		snoozeItems.push({
			key: "clear",
			label: t("thread.snoozeClear"),
			onSelect: run(() => patchThread(id, { snoozedUntil: 0 })),
		});
	const link = `${location.origin}/t/${id}`;
	return [
		{ key: "thread.open", items: open },
		{
			key: "thread.state",
			items: [
				{
					key: "unread",
					icon: thread.unread ? "check" : "circle",
					label: thread.unread
						? t("inbox.markRead")
						: t("contextMenu.markUnread"),
					onSelect: run(() => patchThread(id, { unread: !thread.unread })),
				},
				{
					key: "pin",
					icon: "pin",
					label: thread.pinned ? t("thread.unpin") : t("thread.pin"),
					onSelect: run(() => patchThread(id, { pinned: !thread.pinned })),
				},
				{
					key: "snooze",
					icon: "clock",
					label: t("thread.snooze"),
					children: snoozeItems,
				},
				{
					key: "archive",
					icon: "archive",
					label: thread.archivedAt
						? t("thread.unarchive")
						: t("thread.archive"),
					onSelect: thread.archivedAt
						? run(() => patchThread(id, { archived: false }))
						: thread.worktree
							? () =>
									dialogStore.setState({ dialog: { kind: "archive", thread } })
							: run(() => patchThread(id, { archived: true })),
				},
			],
		},
		{
			key: "thread.edit",
			items: [
				{
					key: "rename",
					icon: "edit",
					label: t("thread.rename"),
					onSelect: () => openThreadDialog("rename", thread),
				},
				{
					key: "labels",
					icon: "list",
					label: t("thread.labels"),
					onSelect: () => openThreadDialog("labels", thread),
				},
				{
					key: "fork",
					icon: "fork",
					label: t("thread.fork"),
					onSelect: () => openThreadDialog("fork", thread),
				},
				{
					key: "color",
					icon: "target",
					label: t("thread.color"),
					children: [...THREAD_COLORS, "none" as const].map((c) => ({
						key: c,
						label: t(`color.${c}`),
						onSelect: run(() =>
							patchThread(id, { color: c === "none" ? "" : c }),
						),
					})),
				},
				{
					key: "link",
					icon: "copy",
					label: t("thread.copyLink"),
					onSelect: () => void copyWithToast(link),
				},
			],
		},
		{
			key: "thread.danger",
			items: [
				{
					key: "delete",
					icon: "trash",
					danger: true,
					label: t("thread.delete"),
					onSelect: () => {
						if (!window.confirm(t("thread.deleteConfirm"))) return;
						run(async () => {
							await deleteThread(id);
							if (location.pathname === `/t/${id}`) o.navigate("/");
						})();
					},
				},
			],
		},
	];
}

/** 线程行菜单打开的对话框；挂在 AppShell 里（与 PaletteHost 并列）。 */
export function ThreadRowDialogs(): ReactNode {
	const dialog = useStore(dialogStore, (s) => s.dialog);
	if (!dialog) return null;
	if (dialog.kind === "archive")
		return (
			<ArchiveDialog
				key={dialog.thread.id}
				pending={{ thread: dialog.thread, shipped: false }}
				onClose={closeDialog}
			/>
		);
	const { threadId } = dialog;
	return (
		<Suspense fallback={null}>
			{dialog.kind === "rename" ? (
				// 只 PATCH：控制面改标题时自己会把 set_session_name 排给 agent；
				// renameThread 再发一遍命令，对已冻结的非当前线程要等 orb 唤醒才应答，对话框会一直不关
				<RenameDialog
					key={threadId}
					threadId={threadId}
					onClose={closeDialog}
					submit={(title) => patchThread(threadId, { title })}
				/>
			) : dialog.kind === "labels" ? (
				<LabelsDialog
					key={threadId}
					threadId={threadId}
					onClose={closeDialog}
				/>
			) : (
				<ForkDialog key={threadId} threadId={threadId} onClose={closeDialog} />
			)}
		</Suspense>
	);
}
