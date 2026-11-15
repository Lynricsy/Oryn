/**
 * 新建 runner 线程撞上 409 `dir_in_use`（体为 `DirInUseConflict`）时的选择对话框（M11b）：
 * 列出正以该目录为工作目录的线程，选项各带一句后果——「新建 worktree（推荐）」：在仓库旁新建目录与分支，互不干扰
 * （只在目录是 git 仓库根时给出）；「仍然共用」：同一份文件可能被两个 agent 同时改。
 */
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { Link } from "react-router";
import { ApiError, type Schemas } from "../../api/client";
import { parseTitleCard } from "../../lib/title";
import { useIndex } from "../../state/stores";
import { Icon, type IconName } from "../ui/icons";
import { Button, Dialog, ErrorText, Spinner } from "../ui/primitives";

export type DirChoice = "worktree" | "share";

export interface DirConflict {
	dir: string;
	threadIds: string[];
	/** 冲突的目录是 git 仓库根，可以在它旁边新建 worktree */
	canWorktree: boolean;
}

/** 从 409 错误里取出 `DirInUseConflict`；不是这种错误时为 null。 */
export function dirConflictOf(
	e: unknown,
	dir: string,
	canWorktree: boolean,
): DirConflict | null {
	if (!(e instanceof ApiError) || e.code !== "dir_in_use") return null;
	const body = e.body as Partial<Schemas["DirInUseConflict"]> | undefined;
	return { dir, threadIds: body?.threadIds ?? [], canWorktree };
}

function Choice({
	icon,
	title,
	consequence,
	busy,
	disabled,
	onClick,
	testId,
}: {
	icon: IconName;
	title: string;
	consequence: string;
	busy: boolean;
	disabled: boolean;
	onClick: () => void;
	testId: string;
}) {
	return (
		<button
			type="button"
			data-testid={testId}
			disabled={disabled}
			onClick={onClick}
			className="flex w-full items-start gap-3 rounded-md border border-border bg-raised px-3 py-2.5 text-left transition-colors duration-150 hover:bg-hover disabled:opacity-50"
		>
			<span className="mt-0.5 grid h-7 w-7 shrink-0 place-items-center rounded-full bg-inset text-muted">
				{busy ? <Spinner size={13} /> : <Icon name={icon} size={14} />}
			</span>
			<span className="min-w-0">
				<span className="block font-medium text-fg text-sm">{title}</span>
				<span className="mt-0.5 block text-muted text-xs">{consequence}</span>
			</span>
		</button>
	);
}

function ThreadLink({ id }: { id: string }): ReactNode {
	const { t } = useTranslation();
	const title = useIndex((s) => s.threads[id]?.title);
	return (
		<Link to={`/t/${id}`} className="truncate text-accent-ink hover:underline">
			{title ? parseTitleCard(title).text || t("thread.untitled") : id}
		</Link>
	);
}

export function DirInUseDialog({
	conflict,
	busy = null,
	error,
	onChoose,
	onClose,
}: {
	conflict: DirConflict | null;
	/** 正在提交的选项（按钮转圈、两个都禁用） */
	busy?: DirChoice | null;
	error?: unknown;
	onChoose: (choice: DirChoice) => void;
	onClose: () => void;
}): ReactNode {
	const { t } = useTranslation();
	if (!conflict) return null;
	return (
		<Dialog
			open
			title={t("runner.dirInUse.title")}
			onClose={onClose}
			footer={
				<Button onClick={onClose} disabled={busy !== null}>
					{t("common.cancel")}
				</Button>
			}
		>
			<div data-testid="dir-in-use" className="flex flex-col gap-3">
				<p className="text-muted text-sm">
					{t("runner.dirInUse.body", { count: conflict.threadIds.length })}
				</p>
				<code className="block break-all rounded-sm bg-inset px-2 py-1 font-mono text-fg text-xs">
					{conflict.dir}
				</code>
				{conflict.threadIds.length > 0 && (
					<ul className="flex flex-col gap-1 text-sm">
						{conflict.threadIds.map((id) => (
							<li key={id} className="flex min-w-0 items-center gap-1.5">
								<Icon
									name="message"
									size={13}
									aria-hidden
									className="shrink-0 text-faint"
								/>
								<ThreadLink id={id} />
							</li>
						))}
					</ul>
				)}
				<div className="flex flex-col gap-2">
					{conflict.canWorktree && (
						<Choice
							testId="dir-in-use-worktree"
							icon="branch"
							title={t("runner.dirInUse.worktree")}
							consequence={t("runner.dirInUse.worktreeHint")}
							busy={busy === "worktree"}
							disabled={busy !== null}
							onClick={() => onChoose("worktree")}
						/>
					)}
					<Choice
						testId="dir-in-use-share"
						icon="folder"
						title={t("runner.dirInUse.share")}
						consequence={t("runner.dirInUse.shareHint")}
						busy={busy === "share"}
						disabled={busy !== null}
						onClick={() => onChoose("share")}
					/>
				</div>
				<ErrorText error={error} />
			</div>
		</Dialog>
	);
}
