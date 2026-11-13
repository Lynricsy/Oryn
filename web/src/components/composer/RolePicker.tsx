/**
 * 模型对话框顶部的角色列表：先按 cycleOrder，再是其余可见的 chat 角色；审阅、子任务、提交信息这类
 * 后台角色收进折叠的「更多 role」。与 Ctrl+P 共用解析、当前角色判定和切换（`RoleTrack.tsx`）。
 */
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import type { RoleSlot } from "../../lib/model-roles";
import { Badge } from "../ui/data";
import { Icon } from "../ui/icons";
import { ModelLogo } from "../ui/ModelLogo";
import { ErrorText, Spinner } from "../ui/primitives";
import {
	requestRole,
	roleLabel,
	slotHue,
	slotProblem,
	useRoleView,
} from "./RoleTrack";

function RoleRow({
	slot,
	hue,
	current,
	currentLevel,
	onPick,
}: {
	slot: RoleSlot;
	hue: string;
	current: boolean;
	/** 当前实际生效的思考级别；只对当前角色有意义 */
	currentLevel?: string;
	onPick: (slot: RoleSlot) => void;
}): ReactNode {
	const { t } = useTranslation();
	const ok = slot.status === "ok";
	const model = slot.model?.name || slot.model?.id;
	// 当前角色的预设级别被手动改过：写成「预设 → 当前 实际」，免得和下方思考级别下拉框对不上
	const overridden =
		current &&
		!!slot.thinkingLevel &&
		!!currentLevel &&
		currentLevel !== slot.thinkingLevel;
	return (
		<li>
			<button
				type="button"
				aria-current={current || undefined}
				aria-disabled={!ok || undefined}
				title={ok ? undefined : slotProblem(slot)}
				className={`role-tone flex w-full min-w-0 items-center gap-2 rounded-sm px-2 py-1.5 text-left text-sm transition-colors duration-150 ${
					current ? "bg-hover" : ok ? "hover:bg-hover" : "cursor-not-allowed"
				}`}
				style={{ "--role-h": hue } as React.CSSProperties}
				onClick={() => {
					if (ok) onPick(slot);
				}}
			>
				{/* 角色色只用在这个色点上；选中态与下方模型列表同一种（灰底 + 行尾中性勾） */}
				<span
					aria-hidden
					className={`h-2 w-2 shrink-0 rounded-full bg-[var(--role-solid)] ${ok ? "" : "opacity-40"}`}
				/>
				<span
					className={`w-20 shrink-0 truncate font-medium sm:w-24 ${ok ? "" : "text-ghost"}`}
				>
					{roleLabel(slot)}
				</span>
				{ok ? (
					<>
						<ModelLogo
							provider={slot.model?.provider}
							model={slot.model?.id}
							size={13}
							className="shrink-0"
						/>
						{/* 预设级别被手动改过时，说明放在模型名下面一行，窄屏下也不挤掉模型名 */}
						<span className="flex min-w-0 flex-1 flex-col">
							<span className="truncate text-muted text-xs">{model}</span>
							{overridden && (
								<span
									title={t("model.effectiveLevel")}
									className="truncate text-2xs text-faint"
								>
									{t("model.currentLevel", { level: currentLevel })}
								</span>
							)}
						</span>
						{slot.thinkingLevel && (
							<Badge title={t("model.presetLevel")}>{slot.thinkingLevel}</Badge>
						)}
					</>
				) : (
					<span className="min-w-0 flex-1 truncate text-ghost text-xs">
						{slot.raw ?? slotProblem(slot)}
					</span>
				)}
				<Icon
					name="check"
					size={13}
					weight="bold"
					aria-hidden
					className={`shrink-0 ${current ? "text-fg" : "invisible"}`}
				/>
			</button>
		</li>
	);
}

/** 角色列表；点一行就切到该角色（同 Ctrl+P：设模型与思考级别、写本地记忆、输入坞上方播切换动画），然后回调 `onPicked`。 */
export function RoleList({
	threadId,
	onPicked,
}: {
	threadId: string;
	onPicked: () => void;
}): ReactNode {
	const { t } = useTranslation();
	const view = useRoleView(threadId, true, true);
	const pick = (slot: RoleSlot) => {
		if (slot.id !== view.currentId) requestRole(threadId, slot);
		onPicked();
	};
	const row = (slot: RoleSlot) => (
		<RoleRow
			key={slot.id}
			slot={slot}
			hue={slotHue(slot, view)}
			current={slot.id === view.currentId}
			currentLevel={view.currentLevel}
			onPick={pick}
		/>
	);
	return (
		<section aria-label={t("model.roles")} className="mb-3">
			<h3 className="mb-1 px-2 font-medium text-2xs text-faint">
				{t("model.roles")}
			</h3>
			<ErrorText error={view.error} />
			{view.isPending && <Spinner size={12} />}
			<ul className="flex flex-col gap-px">{view.cycle.map(row)}</ul>
			{view.others.length > 0 && (
				<ul className="mt-1 flex flex-col gap-px border-border border-t pt-1">
					{view.others.map(row)}
				</ul>
			)}
			{view.more.length > 0 && (
				<details className="group mt-1 border-border border-t pt-1">
					<summary className="flex cursor-pointer list-none items-center gap-1 rounded-sm px-2 py-1 text-2xs text-faint hover:bg-hover hover:text-fg [&::-webkit-details-marker]:hidden">
						<Icon
							name="chevronRight"
							size={11}
							aria-hidden
							className="transition-transform duration-150 group-open:rotate-90"
						/>
						{t("model.moreRoles", { n: view.more.length })}
					</summary>
					<ul className="flex flex-col gap-px">{view.more.map(row)}</ul>
				</details>
			)}
		</section>
	);
}
