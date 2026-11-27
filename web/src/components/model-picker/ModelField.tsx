/**
 * 设置页通用的模型字段：外观同 `Select` 的触发器（字段高、圆角、右侧 caret），点开统一选择器（`ModelPickerPopover`）。
 * 按钮里显示当前值：候选里有的模型 → logo + 名字 + 淡色等宽原文（带 `:级别` 后缀也认）；没有 → 等宽原文；
 * 空值 → placeholder。调用方可用 `display` 覆盖（如「跟随默认」）。
 * `children(close)` 放一个 `ModelPicker`，选中后由调用方调 `close` 收起。
 */
import { type ReactNode, useCallback, useMemo, useState } from "react";
import { useModelCatalog } from "../../api/model-catalog-queries";
import {
	modelSelector,
	type PickerModel,
	parseModelInput,
} from "../../lib/model-picker";
import type { ModelRef } from "../../lib/model-roles";
import { Icon } from "../ui/icons";
import { ModelLogo } from "../ui/ModelLogo";
import { FIELD } from "../ui/primitives";
import { catalogPickerModels, type PickTarget } from "./ModelPicker";
import { ModelPickerPopover } from "./ModelPickerPopover";

/** 选择器选中项里的模型（模型行，或自由输入的 `provider/id`）；对象行不算。 */
export function pickedModel(target: PickTarget): ModelRef | undefined {
	if (target.kind === "model")
		return { provider: target.model.provider, id: target.model.id };
	if (target.kind === "custom") return parseModelInput(target.value);
	return undefined;
}

export function ModelField({
	value,
	display,
	placeholder,
	title,
	models,
	disabled,
	className = "w-full",
	children,
	...aria
}: {
	/** 当前值（`provider/id` 或任意模式）；空串 = 未设置 */
	value: string;
	/** 覆盖按钮里的显示内容 */
	display?: ReactNode;
	placeholder?: string;
	/** 窄屏抽屉的标题 */
	title: string;
	/** 用来认出当前值的候选；缺省 = 中央目录里的对话模型 */
	models?: readonly PickerModel[];
	disabled?: boolean;
	className?: string;
	children: (close: () => void) => ReactNode;
	id?: string;
	"aria-label"?: string;
	"aria-labelledby"?: string;
	"aria-describedby"?: string;
}): ReactNode {
	const [open, setOpen] = useState(false);
	const close = useCallback(() => setOpen(false), []);
	const catalog = useModelCatalog(!models);
	const list = useMemo(
		() => models ?? catalogPickerModels(catalog.data),
		[models, catalog.data],
	);
	const labelledBy = aria["aria-labelledby"];
	const buttonId = aria.id;
	const raw = value.trim();
	// 先按原文找（id 里本身可能带冒号，如 `ollama/qwen3:8b`），再去掉 `:级别` 后缀找
	const ref = parseModelInput(raw);
	const found = raw
		? (list.find((m) => m.selector === raw) ??
			(ref ? list.find((m) => m.selector === modelSelector(ref)) : undefined))
		: undefined;
	let content: ReactNode;
	if (display !== undefined) content = display;
	else if (!raw)
		content = <span className="truncate text-faint">{placeholder}</span>;
	else if (found)
		content = (
			<>
				<ModelLogo
					model={found.id}
					provider={found.provider}
					size={14}
					className="shrink-0"
				/>
				<span className="shrink-0 truncate">{found.name}</span>
				<span className="min-w-0 truncate font-mono text-faint text-xs">
					{raw}
				</span>
			</>
		);
	else content = <span className="truncate font-mono">{raw}</span>;
	return (
		<div className={`relative min-w-0 ${className}`}>
			<button
				type="button"
				aria-haspopup="dialog"
				aria-expanded={open}
				{...aria}
				// 同 `Select`：读出「标题 + 当前值」
				aria-labelledby={
					labelledBy && buttonId ? `${labelledBy} ${buttonId}` : labelledBy
				}
				disabled={disabled}
				title={raw || undefined}
				onClick={() => setOpen((v) => !v)}
				className={`flex h-[34px] cursor-pointer items-center justify-between gap-2 text-left disabled:cursor-default aria-expanded:-translate-y-px aria-expanded:bg-[var(--field-focus)] aria-expanded:shadow-[var(--sh-field-focus)] ${FIELD}`}
			>
				<span className="flex min-w-0 items-center gap-1.5">{content}</span>
				<Icon name="caretUpDown" size={13} className="shrink-0 text-faint" />
			</button>
			{open && (
				<ModelPickerPopover title={title} onClose={close}>
					{children(close)}
				</ModelPickerPopover>
			)}
		</div>
	);
}
