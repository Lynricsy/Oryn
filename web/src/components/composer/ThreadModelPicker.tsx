/**
 * 线程的模型选择：锚在输入坞模型按钮上弹出（窄屏是底部抽屉），内容是统一选择器（`ModelPicker`）。
 * 点模型按钮或 `/model` 打开（`uiStore` 的 `{ kind: "model" }`，`/model <选择器>` 认不出时带筛选词）。
 * 对象区是角色（与 Ctrl+P 同一套取数与切换），其下是中央目录里的模型。选中项 = 当前角色（认不出角色时是当前模型）。
 * 思考级别：改已选中项的级别 = 就地 `set_thinking_level`，弹层不关；点别的角色 / 模型的级别 = 连同该级别切过去并关闭。
 */
import { type ReactNode, useState } from "react";
import { useTranslation } from "react-i18next";
import { parseModelInput } from "../../lib/model-picker";
import type { ModelRef } from "../../lib/model-roles";
import { selectModel } from "../../lib/model-select";
import { realtime } from "../../realtime/socket";
import { runModeCommand } from "../../state/actions";
import { useThread } from "../../state/stores";
import {
	entryKey,
	ModelPicker,
	modelKey,
	type PickerEntry,
	type PickerThinking,
	type PickTarget,
	targetKey,
} from "../model-picker/ModelPicker";
import { ModelPickerPopover } from "../model-picker/ModelPickerPopover";
import { Spinner } from "../ui/primitives";
import {
	refreshState,
	requestRole,
	roleEntries,
	useRoleAvatar,
	useRoleView,
} from "./RoleTrack";

export function ThreadModelPicker({
	threadId,
	initialQuery,
	onClose,
}: {
	threadId: string;
	/** 打开时预填的筛选词（`/model <选择器>` 解析不出时带过来） */
	initialQuery?: string;
	onClose: () => void;
}): ReactNode {
	const { t } = useTranslation();
	const state = useThread(threadId, (s) => s.state);
	const view = useRoleView(threadId, true, true);
	const avatar = useRoleAvatar();
	const [error, setError] = useState<unknown>(null);
	const slots = [...view.cycle, ...view.others, ...view.more];
	// 角色还在读：对象区先放一行占位（点了不做任何事），读完换成角色行
	const entries: PickerEntry[] = view.isPending
		? [
				{
					key: "loading",
					name: t("roles.loading"),
					face: <Spinner size={12} />,
				},
			]
		: roleEntries(view, avatar);
	const selected = view.currentId
		? entryKey(view.currentId)
		: state?.model
			? modelKey(state.model)
			: undefined;
	const slotOf = (target: PickTarget) =>
		target.kind === "entry"
			? slots.find((s) => s.id === target.entry.key)
			: undefined;
	const modelOf = (target: PickTarget): ModelRef | undefined =>
		target.kind === "model"
			? target.model
			: target.kind === "custom"
				? parseModelInput(target.value)
				: undefined;
	// 模型列表里直接选 = 手动选择（来源记录 `manual`，回合头回落）；旧 Core 自动回退到 set_model。失败时弹层不关
	const chooseModel = async (m: ModelRef, level?: string) => {
		setError(null);
		try {
			await selectModel(threadId, { provider: m.provider, id: m.id });
			if (level)
				await realtime.cmd(threadId, {
					type: "set_thinking_level",
					level: level as "auto",
				});
			await refreshState(threadId);
			onClose();
		} catch (e) {
			setError(e);
		}
	};
	const setLevel = async (level: string) => {
		setError(null);
		try {
			await runModeCommand(threadId, {
				type: "set_thinking_level",
				level: level as "auto",
			});
		} catch (e) {
			setError(e);
		}
	};
	const thinking: PickerThinking = {
		value: (target) =>
			targetKey(target) === selected
				? view.currentLevel
				: slotOf(target)?.thinkingLevel,
		preset: (target) => slotOf(target)?.thinkingLevel,
		onChange: (target, level) => {
			if (targetKey(target) === selected) {
				void setLevel(level);
				return;
			}
			if (target.kind === "entry") {
				const slot = slotOf(target);
				if (slot?.status !== "ok") return;
				requestRole(threadId, slot, level);
				onClose();
				return;
			}
			const m = modelOf(target);
			if (m) void chooseModel(m, level);
		},
	};
	return (
		<ModelPickerPopover
			title={t("model.title")}
			side="top"
			// 模型按钮在输入坞右侧：弹层右缘对齐按钮，向左展开，不压到右侧信息面板
			align="end"
			onClose={onClose}
			testId="thread-model-picker"
		>
			<ModelPicker
				entries={entries}
				entriesLabel={t("model.roles")}
				selected={selected}
				thinking={thinking}
				initialQuery={initialQuery}
				error={error ?? view.error}
				onPick={(target) => {
					if (target.kind === "entry") {
						const slot = slotOf(target);
						if (!slot) return;
						if (slot.id !== view.currentId) requestRole(threadId, slot);
						onClose();
						return;
					}
					const m = modelOf(target);
					if (m) void chooseModel(m);
				}}
			/>
		</ModelPickerPopover>
	);
}
