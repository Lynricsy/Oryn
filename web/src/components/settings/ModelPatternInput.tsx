/**
 * 设置 → Agent 里的模型选择（子 agent 的 `model`、`task.agentModelOverrides`，模型角色的 `modelRoles`）：
 * 输入框可以自由填 omp 的模型模式（`provider/id`、通配、`:level` 后缀），下拉补全来自中央模型目录的对话模型，
 * 给了 `roles` 时另补 `@<role>`（引用角色模型）。
 * - `ModelPatternInput`：单个值，失焦或回车时提交（未改动不提交），Esc 放弃；
 * - `ModelPatternList`：多个值（按顺序回退），每项一枚可移除的标签，输入框回车或「添加」追加一项。
 */
import { type KeyboardEvent, type ReactNode, useId, useState } from "react";
import { useTranslation } from "react-i18next";
import { chatModels, useModelCatalog } from "../../api/model-catalog-queries";
import { Button, IconButton, TextInput } from "../ui/primitives";

/** 补全项：`@role` 在前，再是目录里的对话模型。 */
function ModelOptions({
	id,
	roles,
}: {
	id: string;
	roles?: readonly string[];
}): ReactNode {
	const catalog = useModelCatalog();
	return (
		<datalist id={id}>
			{roles?.map((r) => (
				<option key={`@${r}`} value={`@${r}`} />
			))}
			{chatModels(catalog.data).map((m) => (
				<option key={m.selector} value={m.selector}>
					{m.name || m.id}
				</option>
			))}
		</datalist>
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
	/** 无障碍名称 */
	label: string;
	placeholder?: string;
	roles?: readonly string[];
	disabled?: boolean;
}): ReactNode {
	const listId = useId();
	/** 正在编辑的草稿；不在编辑时跟随 `value`。 */
	const [draft, setDraft] = useState<string | null>(null);
	const commit = () => {
		if (draft === null) return;
		setDraft(null);
		const next = draft.trim();
		if (next !== value.trim()) onCommit(next);
	};
	return (
		<>
			<TextInput
				aria-label={label}
				placeholder={placeholder}
				list={listId}
				className="font-mono"
				value={draft ?? value}
				disabled={disabled}
				onChange={(e) => setDraft(e.target.value)}
				onBlur={commit}
				onKeyDown={(e) => {
					if (e.key === "Enter") {
						e.preventDefault();
						commit();
					} else if (e.key === "Escape" && draft !== null) {
						e.preventDefault();
						e.stopPropagation();
						setDraft(null);
					}
				}}
			/>
			<ModelOptions id={listId} roles={roles} />
		</>
	);
}

export function ModelPatternList({
	value,
	onChange,
	label,
	placeholder,
	roles,
	disabled,
}: {
	value: readonly string[];
	onChange(next: string[]): void;
	/** 输入框的无障碍名称 */
	label: string;
	placeholder?: string;
	roles?: readonly string[];
	disabled?: boolean;
}): ReactNode {
	const { t } = useTranslation();
	const listId = useId();
	const [draft, setDraft] = useState("");
	const add = () => {
		const next = draft.trim();
		if (!next) return;
		setDraft("");
		if (!value.includes(next)) onChange([...value, next]);
	};
	const onKeyDown = (e: KeyboardEvent<HTMLInputElement>) => {
		// 在表单里回车只追加，不提交整个表单
		if (e.key === "Enter") {
			e.preventDefault();
			add();
		}
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
			<div className="flex gap-1.5">
				<TextInput
					aria-label={label}
					placeholder={placeholder}
					list={listId}
					className="font-mono"
					value={draft}
					disabled={disabled}
					onChange={(e) => setDraft(e.target.value)}
					onKeyDown={onKeyDown}
				/>
				<Button icon="plus" disabled={disabled || !draft.trim()} onClick={add}>
					{t("agentPages.models.add")}
				</Button>
			</div>
			<ModelOptions id={listId} roles={roles} />
		</div>
	);
}
