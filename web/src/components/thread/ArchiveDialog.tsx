/**
 * 归档线程（M11b）：线程有 Oryn 建的 worktree（runner 线程）时先弹对话框，多一个默认勾选的「同时移除 worktree」；
 * 没有 worktree 时直接归档，与以前一样。先归档、再移除：移除失败（有未提交改动、runner 离线等）时线程仍是归档的，
 * 对话框留着并把 git 的报错原样显示。`shipped` = 「归档为已交付」。
 */
import { type ReactNode, useState } from "react";
import { useTranslation } from "react-i18next";
import { api, type Thread, unwrap } from "../../api/client";
import { patchThread } from "../../state/actions";
import { Icon } from "../ui/icons";
import {
	Button,
	Checkbox,
	Dialog,
	describeError,
	ErrorText,
	Spinner,
} from "../ui/primitives";

/** 在 runner 上移除线程的 worktree（`git worktree remove`，不带 `--force`）。 */
export async function removeThreadWorktree(threadId: string): Promise<void> {
	await unwrap(
		api.POST("/api/v1/threads/{id}/worktree/remove", {
			params: { path: { id: threadId } },
		}),
	);
}
/** 归档只需要线程 id 与它的 worktree（没有 worktree 就直接归档）。 */
type ArchiveTarget = Pick<Thread, "id" | "worktree">;

interface Pending {
	thread: ArchiveTarget;
	shipped: boolean;
}

export function ArchiveDialog({
	pending,
	onClose,
}: {
	pending: Pending;
	onClose: () => void;
}): ReactNode {
	const { t } = useTranslation();
	const { thread, shipped } = pending;
	const worktree = thread.worktree;
	const [remove, setRemove] = useState(true);
	const [busy, setBusy] = useState(false);
	const [archived, setArchived] = useState(false);
	const [error, setError] = useState<unknown>(null);
	const run = async () => {
		setBusy(true);
		setError(null);
		try {
			if (!archived) {
				await patchThread(thread.id, {
					archived: true,
					...(shipped ? { shipped: true } : {}),
				});
				setArchived(true);
			}
			if (remove && worktree) await removeThreadWorktree(thread.id);
			onClose();
		} catch (e) {
			setError(e);
		} finally {
			setBusy(false);
		}
	};
	return (
		<Dialog
			open
			title={t(shipped ? "archive.titleShipped" : "archive.title")}
			onClose={busy ? () => {} : onClose}
			footer={
				archived ? (
					<Button onClick={onClose} disabled={busy}>
						{t("common.close")}
					</Button>
				) : (
					<>
						<Button onClick={onClose} disabled={busy}>
							{t("common.cancel")}
						</Button>
						<Button
							variant="primary"
							disabled={busy}
							onClick={() => void run()}
						>
							{busy && <Spinner size={13} />}
							{t(shipped ? "thread.archiveShipped" : "thread.archive")}
						</Button>
					</>
				)
			}
		>
			<div data-testid="archive-dialog" className="flex flex-col gap-4">
				<p className="text-muted text-sm leading-relaxed">
					{t("archive.body")}
				</p>
				{worktree && (
					// 选项卡片：勾选时垫一层浅底；worktree 目录是卡片底部一枚等宽字标签（分支名已在说明里）
					// biome-ignore lint/a11y/noLabelWithoutControl: 控件为 Checkbox 组件（内含原生 input）
					<label className="flex cursor-pointer items-start gap-3 rounded-md border border-border p-3.5 transition-colors duration-150 hover:bg-raised has-checked:bg-raised has-disabled:cursor-default">
						<Checkbox
							className="mt-px"
							checked={remove}
							disabled={busy || archived}
							onChange={(e) => setRemove(e.target.checked)}
						/>
						<span className="min-w-0 flex-1">
							<span className="block font-medium text-fg text-sm">
								{t("archive.removeWorktree")}
							</span>
							<span className="mt-1 block text-muted text-xs leading-relaxed">
								{t("archive.removeWorktreeHint", { branch: worktree.branch })}
							</span>
							<code className="mt-3 inline-flex max-w-full items-start gap-1.5 rounded-sm bg-inset px-2 py-1 font-mono text-2xs text-muted">
								<Icon
									name="folder"
									size={12}
									aria-hidden
									className="mt-px shrink-0 text-faint"
								/>
								<span className="break-all">{worktree.dir}</span>
							</code>
						</span>
					</label>
				)}
				{error !== null &&
					(archived ? (
						<div role="alert" className="flex flex-col gap-1.5">
							<p className="flex items-start gap-1.5 text-err text-sm">
								<Icon name="alert" size={13} className="mt-[3px] shrink-0" />
								<span>{t("archive.removeFailed")}</span>
							</p>
							{/* git 的报错原样显示（常见：有未提交的改动） */}
							<pre className="max-h-40 overflow-auto whitespace-pre-wrap break-all rounded-md bg-inset px-2.5 py-2 font-mono text-fg text-xs">
								{describeError(error, t)}
							</pre>
						</div>
					) : (
						<ErrorText error={error} />
					))}
			</div>
		</Dialog>
	);
}

/**
 * `archive(thread, shipped?)`：没有 worktree 时直接归档（返回 PATCH 的 promise，错误交给调用方）；
 * 有 worktree 时打开对话框（立即 resolve）。调用方负责渲染 `dialog`。
 */
export function useArchiveThread(): {
	archive(thread: ArchiveTarget, shipped?: boolean): Promise<unknown>;
	dialog: ReactNode;
} {
	const [pending, setPending] = useState<Pending | null>(null);
	return {
		archive: async (thread, shipped = false) => {
			if (!thread.worktree)
				return patchThread(thread.id, {
					archived: true,
					...(shipped ? { shipped: true } : {}),
				});
			setPending({ thread, shipped });
		},
		dialog: pending && (
			<ArchiveDialog
				key={pending.thread.id}
				pending={pending}
				onClose={() => setPending(null)}
			/>
		),
	};
}
