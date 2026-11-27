/**
 * 设置 → Agent 里的模型选择（子 agent 的 `model`、`task.agentModelOverrides`，模型角色的 `modelRoles`，
 * 子 agent 的 advisor / prewalk）：都打开统一选择器（`ModelPicker free="pattern"`），目录里的对话模型可直接选，
 * 搜索框里也能写任何 omp 的模型模式（`provider/id`、通配、`:level` 后缀、回退链，经「使用 …」行提交）；
 * 给了 `roles` 时对象区列出 `@<role>`（引用角色模型）。
 * - `ModelPatternInput`：单个值，外观同 `Select` 的触发按钮，选中即提交（未改动不提交），旁边的清除按钮提交空串；
 * - `ModelPatternList`：多个值（按顺序回退），每项一枚可移除的标签，「添加」打开选择器，选中即追加一项。
 */
import { type ReactNode, useState } from "react";
import { useTranslation } from "react-i18next";
import { modelSelector, parseModelInput } from "../../lib/model-picker";
import { ModelField } from "../model-picker/ModelField";
import {
	customKey,
	entryKey,
	ModelPicker,
	modelKey,
	type PickerEntry,
} from "../model-picker/ModelPicker";
import { ModelPickerPopover } from "../model-picker/ModelPickerPopover";
import { Icon } from "../ui/icons";
import { Button, IconButton } from "../ui/primitives";

/** 当前值在选择器里对应的行：`@role` → 对象行，恰好是 `provider/id` → 模型行，其余 → 自由输入。 */
function selectedKey(value: string): string | undefined {
	if (!value) return undefined;
	if (value.startsWith("@")) return entryKey(value);
	const ref = parseModelInput(value);
	return ref && modelSelector(ref) === value ? modelKey(ref) : customKey(value);
}

/** 模式选择器：`@role` 对象行 + 目录模型 + 任意模式的自由输入；选中项回调它的模式文本。 */
function PatternPicker({
	value,
	roles,
	onPick,
}: {
	value: string;
	roles?: readonly string[];
	onPick(pattern: string): void;
}): ReactNode {
	const entries: PickerEntry[] = (roles ?? []).map((r) => ({
		key: `@${r}`,
		name: `@${r}`,
		face: <Icon name="user" size={14} />,
	}));
	return (
		<ModelPicker
			free="pattern"
			entries={entries}
			selected={selectedKey(value)}
			onPick={(target) =>
				onPick(
					target.kind === "entry"
						? target.entry.key
						: target.kind === "model"
							? target.model.selector
							: target.value,
				)
			}
		/>
	);
}

export function ModelPatternInput({
	value,
	onCommit,
	label,
	placeholder,
	roles,
	disabled,
}: {
	/** 当前值（未设置为空串） */
	value: string;
	/** 提交去空白后的值；空串表示清除 */
	onCommit(next: string): void;
	/** 无障碍名称（触发按钮的 aria-label） */
	label: string;
	placeholder?: string;
	roles?: readonly string[];
	disabled?: boolean;
}): ReactNode {
	const { t } = useTranslation();
	const current = value.trim();
	return (
		<div className="flex min-w-0 flex-1 items-center gap-1.5">
			<ModelField
				aria-label={label}
				title={label}
				value={current}
				placeholder={placeholder}
				disabled={disabled}
			>
				{(close) => (
					<PatternPicker
						value={current}
						roles={roles}
						onPick={(picked) => {
							close();
							const next = picked.trim();
							if (next !== current) onCommit(next);
						}}
					/>
				)}
			</ModelField>
			{current && (
				<IconButton
					icon="close"
					label={t("agentPages.models.clear", { label })}
					disabled={disabled}
					onClick={() => onCommit("")}
				/>
			)}
		</div>
	);
}

export function ModelPatternList({
	value,
	onChange,
	label,
	roles,
	disabled,
}: {
	value: readonly string[];
	onChange(next: string[]): void;
	/** 「添加」按钮的无障碍名称 */
	label: string;
	roles?: readonly string[];
	disabled?: boolean;
}): ReactNode {
	const { t } = useTranslation();
	const [open, setOpen] = useState(false);
	const add = (picked: string) => {
		setOpen(false);
		const next = picked.trim();
		if (next && !value.includes(next)) onChange([...value, next]);
	};
	return (
		<div className="flex flex-col gap-1.5">
			{value.length > 0 && (
				<ul className="flex flex-wrap gap-1">
					{value.map((m) => (
						<li
							key={m}
							className="inline-flex h-6 max-w-full items-center gap-0.5 rounded-xs border border-border bg-raised pr-0.5 pl-2 font-mono text-xs"
						>
							<span className="min-w-0 truncate" title={m}>
								{m}
							</span>
							<IconButton
								icon="close"
								box="xs"
								size={11}
								label={t("agentPages.models.remove", { model: m })}
								disabled={disabled}
								onClick={() => onChange(value.filter((v) => v !== m))}
							/>
						</li>
					))}
				</ul>
			)}
			<div className="relative inline-flex self-start">
				<Button
					icon="plus"
					aria-label={label}
					aria-haspopup="dialog"
					aria-expanded={open}
					disabled={disabled}
					onClick={() => setOpen((v) => !v)}
				>
					{t("agentPages.models.add")}
				</Button>
				{open && (
					<ModelPickerPopover title={label} onClose={() => setOpen(false)}>
						<PatternPicker value="" roles={roles} onPick={add} />
					</ModelPickerPopover>
				)}
			</div>
		</div>
	);
}
