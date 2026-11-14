/**
 * 新线程的模型与思考级别选择（首页新建线程框、「每次新建线程」定时的对话框共用）。
 * 模型选择器只选模型：自由输入 `provider/id`、「默认模型」与最近用过的模型；思考级别是胶囊旁的独立圆圈按钮（`ModelPill`），
 * 级别取模型能力缓存（会话里见过该模型时记下），没见过的模型只给「跟随默认」，模型不推理时没有圆圈。
 */
import {
	type ReactElement,
	type ReactNode,
	useEffect,
	useMemo,
	useState,
} from "react";
import { useTranslation } from "react-i18next";
import { modelBrand, shortModelName } from "../../lib/model-brand";
import { type ModelCaps, useModelCaps } from "../../state/model-caps";
import { useIndex } from "../../state/stores";
import { Icon } from "./icons";
import { ModelLogo } from "./ModelLogo";
import { Option, Popover } from "./PickerPopover";
import { TextInput } from "./primitives";
import { ThinkingMenu } from "./ThinkingDial";

/**
 * 新线程可选的思考级别：空串 = 跟随 role / 默认。新线程还没有会话，级别取模型能力缓存；
 * 没见过的模型（含 role 默认模型）能力未知，只给「跟随默认」，不假装全部级别都可用；模型不推理时为 null（不显示圆圈）。
 */
export function newThreadThinkingOptions(
	caps: ModelCaps | undefined,
): readonly string[] | null {
	if (!caps) return [""];
	if (caps.thinking.length === 0) return null;
	return ["", "auto", ...caps.thinking];
}

/** 最近用过的模型（线程索引里出现过的 `provider/id`，至多 8 个）。 */
export function useRecentModels(): string[] {
	const threads = useIndex((s) => s.threads);
	return useMemo(() => {
		const set = new Set<string>();
		for (const th of Object.values(threads))
			if (th.model) set.add(`${th.model.provider}/${th.model.id}`);
		return [...set].slice(0, 8);
	}, [threads]);
}

/**
 * 胶囊文案：认得出品牌时去掉系列名（logo 已说明）；无障碍名与悬停提示只写模型全名。
 * 与线程输入坞的 ModelChip 同一套省空间规则。
 */
export function modelPillText(
	model: string,
	defaultLabel: string,
): { label: string; main: string } {
	const id = model.trim().split("/").pop()?.replace(/:.*$/, "") || model;
	return {
		label: model || defaultLabel,
		main: model ? shortModelName(id, modelBrand(model)) : defaultLabel,
	};
}

/**
 * 模型胶囊 + 思考级别圆圈：圆圈是单独的按钮（点开选级别），不放在模型弹层里。
 * 宽屏叠在胶囊右端（胶囊留出位置）；窄屏是模型图标右下角的角标，不占行宽。
 */
export function ModelPill({
	pill,
	corner,
	thinking,
	options,
	known,
	onThinking,
	side,
}: {
	pill: ReactElement;
	corner: boolean;
	thinking: string;
	options: readonly string[];
	/** 模型能力已知；未知时「跟随默认」项写明原因 */
	known: boolean;
	onThinking: (level: string) => void;
	side?: "top" | "bottom";
}): ReactNode {
	const { t } = useTranslation();
	return (
		<span className="relative inline-flex min-w-0 shrink">
			{pill}
			<span
				className={`absolute flex ${corner ? "-right-1 -bottom-1" : "-translate-y-1/2 top-1/2 right-0.5"}`}
			>
				<ThinkingMenu
					value={thinking}
					options={options}
					defaultLabel={t("newThread.defaultThinking")}
					defaultHint={known ? undefined : t("newThread.thinkingUnknown")}
					onSelect={onThinking}
					size={corner ? 12 : 14}
					badge={corner}
					side={side}
					className="text-muted"
				/>
			</span>
		</span>
	);
}

/** 模型弹层的内容：自由输入框、「默认模型」、最近用过的模型。 */
export function ModelOptions({
	model,
	onModel,
}: {
	model: string;
	onModel: (model: string) => void;
}): ReactNode {
	const { t } = useTranslation();
	const recent = useRecentModels();
	return (
		<>
			<div className="p-1">
				<TextInput
					autoFocus
					aria-label={t("newThread.modelPlaceholder")}
					placeholder={t("newThread.modelPlaceholder")}
					value={model}
					onChange={(e) => onModel(e.target.value)}
				/>
			</div>
			<Option
				selected={!model}
				leading={<Icon name="sparkle" size={14} />}
				onClick={() => onModel("")}
			>
				{t("newThread.defaultModel")}
			</Option>
			{recent.map((m) => (
				<Option
					key={m}
					selected={model === m}
					leading={<ModelLogo model={m} />}
					onClick={() => onModel(m)}
				>
					{m}
				</Option>
			))}
		</>
	);
}

/** 表单里的模型 + 思考级别控件（定时对话框）：胶囊点开弹层，圆圈独立选级别；换模型后不支持的级别退回默认。 */
export function ModelSelect({
	model,
	thinking,
	onModel,
	onThinking,
}: {
	model: string;
	thinking: string;
	onModel: (model: string) => void;
	onThinking: (level: string) => void;
}): ReactNode {
	const { t } = useTranslation();
	const [open, setOpen] = useState(false);
	const caps = useModelCaps(model);
	const options = newThreadThinkingOptions(caps);
	const text = modelPillText(model, t("newThread.defaultModel"));
	// 换了模型、原来选的级别新模型不支持（或不推理）时退回「跟随默认」
	useEffect(() => {
		if (thinking && !options?.includes(thinking)) onThinking("");
	}, [thinking, options, onThinking]);
	const pill = (
		<button
			type="button"
			aria-expanded={open}
			aria-label={t("schedule.model", { model: text.label })}
			title={text.label}
			onClick={() => setOpen((v) => !v)}
			className={`inline-flex h-8 min-w-0 max-w-full items-center gap-1.5 rounded-full border border-border bg-inset pl-3 text-muted text-xs transition-colors duration-150 hover:bg-hover hover:text-fg ${options ? "pr-8" : "pr-3"}`}
		>
			<span className="inline-flex shrink-0">
				<ModelLogo model={model} size={13} />
			</span>
			<span className="min-w-0 truncate">{text.main}</span>
		</button>
	);
	return (
		<span className="relative inline-flex min-w-0 max-w-full">
			{options ? (
				<ModelPill
					pill={pill}
					corner={false}
					thinking={thinking}
					options={options}
					known={caps !== undefined}
					onThinking={onThinking}
					side="bottom"
				/>
			) : (
				pill
			)}
			{open && (
				<Popover side="bottom" onClose={() => setOpen(false)}>
					<ModelOptions model={model} onModel={onModel} />
				</Popover>
			)}
		</span>
	);
}
