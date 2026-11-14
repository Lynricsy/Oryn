/**
 * Desktop 工具条最左的 Profile 菜单（M8 4.3）与「为 orb 选用 Profile」的流程钩子。
 * 菜单：各 Profile（当前的打勾；被别的 orb 持有的带「使用中」）、不使用 Profile、立即保存（仅独占持有时）、管理 Profile…
 * 切换前确认一句「当前浏览器会被关闭」。被占用（列表里有持有者，或 PUT 回 409 `profile_in_use`）时弹冲突对话框，
 * 由用户在「只读使用」与「接管」之间选择后重发。
 */
import { type ReactNode, useState } from "react";
import { useTranslation } from "react-i18next";
import { useNavigate } from "react-router";
import type { Schemas } from "../../api/client";
import { useBrowserProfiles, useSetOrbDesktopProfile } from "../../api/queries";
import { Badge } from "../ui/data";
import { Icon } from "../ui/icons";
import { Menu, type MenuItem } from "../ui/primitives";
import {
	type ConflictChoice,
	ProfileConflictDialog,
} from "./ProfileConflictDialog";
import {
	type OrbDesktopProfile,
	type ProfileConflict,
	profileConflictOf,
} from "./profile-status";

type Mode = Schemas["BrowserProfileMode"];

/** `useOrbProfileChoice` 的返回值。 */
export interface OrbProfileChoice {
	/** 发 PUT（`profileId` 空串 = 解除）；409 `profile_in_use` 时打开冲突对话框 */
	apply(profileId: string, mode?: Mode): Promise<void>;
	/** 已知持有者时直接打开冲突对话框 */
	open(conflict: ProfileConflict): void;
	/** 冲突对话框（调用方渲染） */
	dialog: ReactNode;
	/** 非冲突的失败 */
	error: unknown;
	clearError(): void;
	pending: boolean;
}

/**
 * 为 orb 选用 Profile：`apply(id, mode?)` 发 PUT，409 `profile_in_use` 时打开冲突对话框；`open(c)` 直接打开对话框
 * （已知持有者时）。`dialog` 由调用方渲染；`error` 是非冲突的失败。
 * `readonlyProfileId`：这台 orb 正以只读方式使用的 Profile。只有冲突目标正是它（想恢复写回）时不再给「只读使用」；
 * 换到另一个被占用的 Profile 时照常给两个选项。
 */
export function useOrbProfileChoice(
	orbId: string,
	readonlyProfileId?: string,
): OrbProfileChoice {
	const set = useSetOrbDesktopProfile(orbId);
	const [conflict, setConflict] = useState<ProfileConflict | null>(null);
	const [busy, setBusy] = useState<ConflictChoice | null>(null);
	const [dialogError, setDialogError] = useState<unknown>(null);
	const [error, setError] = useState<unknown>(null);
	const open = (c: ProfileConflict) => {
		setDialogError(null);
		setConflict(c);
	};
	const apply = async (profileId: string, mode?: Mode) => {
		setError(null);
		try {
			await set.mutateAsync({
				profileId,
				...(mode ? { profileMode: mode } : {}),
			});
		} catch (e) {
			const c = profileConflictOf(e);
			if (c) open(c);
			else setError(e);
		}
	};
	const choose = async (choice: ConflictChoice) => {
		if (!conflict) return;
		setBusy(choice);
		setDialogError(null);
		try {
			await set.mutateAsync({
				profileId: conflict.profileId,
				profileMode: choice,
			});
			setConflict(null);
		} catch (e) {
			setDialogError(e);
		} finally {
			setBusy(null);
		}
	};
	const dialog = (
		<ProfileConflictDialog
			conflict={conflict}
			busy={busy}
			error={dialogError}
			allowReadonly={conflict?.profileId !== readonlyProfileId}
			onChoose={(c) => void choose(c)}
			onClose={() => {
				if (!busy) setConflict(null);
			}}
		/>
	);
	return {
		apply,
		open,
		dialog,
		error,
		clearError: () => setError(null),
		pending: set.isPending,
	};
}

export function ProfileMenu({
	orbId,
	current,
	browserRunning,
	choice,
	onSave,
	saving,
}: {
	orbId: string;
	current: OrbDesktopProfile | undefined;
	browserRunning: boolean;
	choice: OrbProfileChoice;
	onSave: () => void;
	saving: boolean;
}): ReactNode {
	const { t } = useTranslation();
	const navigate = useNavigate();
	const profiles = useBrowserProfiles();
	// 浏览器在跑、或本地装着某个 Profile 时，切换会关闭当前浏览器
	const confirmSwitch = () =>
		!(browserRunning || current) ||
		window.confirm(t("deskPanel.menu.switchConfirm"));
	const items: MenuItem[] = [
		...(profiles.data?.profiles ?? []).map((p): MenuItem => {
			const other = p.holder && p.holder.orbId !== orbId ? p.holder : null;
			return {
				key: p.id,
				label: p.name,
				icon: "browser",
				checked: current?.id === p.id,
				hint: other ? t("deskPanel.menu.inUse") : undefined,
				onSelect: () => {
					if (current?.id === p.id || !confirmSwitch()) return;
					if (other)
						choice.open({
							profileId: p.id,
							profileName: p.name,
							holder: other,
						});
					else void choice.apply(p.id);
				},
			};
		}),
		{
			key: "none",
			label: t("deskPanel.menu.none"),
			icon: "minusCircle",
			checked: !current,
			onSelect: () => {
				if (!current || !confirmSwitch()) return;
				void choice.apply("");
			},
		},
		...(current?.status === "attached"
			? [
					{
						key: "save",
						label: t("deskPanel.menu.save"),
						icon: "upload" as const,
						disabled: saving,
						onSelect: onSave,
					},
				]
			: []),
		{
			key: "manage",
			label: t("deskPanel.menu.manage"),
			icon: "settings",
			onSelect: () => navigate("/settings/browser-profiles"),
		},
	];
	const label = current?.name ?? t("deskPanel.menu.noProfile");
	return (
		<Menu
			align="left"
			className="min-w-0 shrink"
			items={items}
			trigger={(p) => (
				<button
					type="button"
					data-testid="desktop-profile-menu"
					aria-label={t("deskPanel.menu.label", { name: label })}
					title={t("deskPanel.menu.label", { name: label })}
					disabled={choice.pending}
					className={`inline-flex h-7 min-w-0 max-w-full items-center gap-1.5 rounded-md px-2 text-xs transition-colors duration-150 hover:bg-hover hover:text-fg disabled:opacity-50 ${p["aria-expanded"] ? "bg-active text-fg" : "text-muted"}`}
					{...p}
				>
					<Icon name="browser" size={14} className="shrink-0" />
					<span className="min-w-0 truncate">{label}</span>
					{current?.mode === "readonly" && (
						<Badge>{t("deskPanel.profile.readonlyShort")}</Badge>
					)}
					<Icon name="chevronDown" size={11} className="shrink-0 text-faint" />
				</button>
			)}
		/>
	);
}
