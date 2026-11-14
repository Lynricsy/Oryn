/**
 * Profile 被另一台运行中的 orb 独占持有（409 `profile_in_use`）时的选择对话框（主人决定 2026-11-14）：
 * 显示持有者线程与开始时间，两个选项各带一句后果——
 * 「只读使用」：用最近一次保存的版本，这里的改动不会保存；「接管」：先保存对方的登录态，对方转为只读。
 * 新建线程框、Desktop 面板的 Profile 菜单与「被占用」横幅共用。
 */
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { formatDateTime, formatRelative } from "../../lib/format";
import { Icon, type IconName } from "../ui/icons";
import { Button, Dialog, ErrorText, Spinner } from "../ui/primitives";
import { holderTitle, type ProfileConflict } from "./profile-status";

export type ConflictChoice = "readonly" | "takeover";

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

export function ProfileConflictDialog({
	conflict,
	busy = null,
	error,
	allowReadonly = true,
	onChoose,
	onClose,
}: {
	conflict: ProfileConflict | null;
	/** 正在提交的选项（按钮转圈、两个都禁用） */
	busy?: ConflictChoice | null;
	error?: unknown;
	/** 已经是只读副本时不再给「只读使用」 */
	allowReadonly?: boolean;
	onChoose: (choice: ConflictChoice) => void;
	onClose: () => void;
}): ReactNode {
	const { t } = useTranslation();
	if (!conflict) return null;
	const { holder } = conflict;
	return (
		<Dialog
			open
			title={t("deskPanel.conflict.title", { name: conflict.profileName })}
			onClose={onClose}
			footer={
				<Button onClick={onClose} disabled={busy !== null}>
					{t("common.cancel")}
				</Button>
			}
		>
			<div data-testid="profile-conflict" className="flex flex-col gap-3">
				<p className="text-muted text-sm">
					{t("deskPanel.conflict.body", {
						thread: holderTitle(holder, t),
						since: formatDateTime(holder.since),
						ago: formatRelative(holder.since),
					})}
				</p>
				<div className="flex flex-col gap-2">
					{allowReadonly && (
						<Choice
							testId="profile-conflict-readonly"
							icon="eye"
							title={t("deskPanel.conflict.readonly")}
							consequence={t("deskPanel.conflict.readonlyHint")}
							busy={busy === "readonly"}
							disabled={busy !== null}
							onClick={() => onChoose("readonly")}
						/>
					)}
					<Choice
						testId="profile-conflict-takeover"
						icon="key"
						title={t("deskPanel.conflict.takeover")}
						consequence={t("deskPanel.conflict.takeoverHint")}
						busy={busy === "takeover"}
						disabled={busy !== null}
						onClick={() => onChoose("takeover")}
					/>
				</div>
				<ErrorText error={error} />
			</div>
		</Dialog>
	);
}
