/**
 * 新线程的模型与思考级别选择（首页新建线程框、「每次新建线程」定时的对话框共用）。
 * 模型选择器只选模型：自由输入 `provider/id`、「默认模型」、最近用过的模型与中央模型目录里的对话模型；
 * 思考级别是胶囊旁的独立圆圈按钮（`ModelPill`），级别取模型目录的 `thinking`（`state/model-caps.ts`），
 * 目录里没有的模型只给「跟随默认」，模型不能调节思考时没有圆圈。
 */
import { useQueryClient } from "@tanstack/react-query";
import {
	type ReactElement,
	type ReactNode,
	useEffect,
	useMemo,
	useState,
} from "react";
import { useTranslation } from "react-i18next";
import {
	chatModels,
	type ModelCatalog,
	refreshModelCatalog,
	useModelCatalog,
} from "../../api/model-catalog-queries";
import { formatDateTime } from "../../lib/format";
import { modelBrand, shortModelName } from "../../lib/model-brand";
import { type ModelCaps, useModelCaps } from "../../state/model-caps";
import { useIndex } from "../../state/stores";
import { Icon } from "./icons";
import { ModelLogo } from "./ModelLogo";
import { Option, Popover, PopoverLabel } from "./PickerPopover";
import { Button, describeError, Spinner, TextInput } from "./primitives";
import { ThinkingMenu } from "./ThinkingDial";

/**
 * 新线程可选的思考级别：空串 = 跟随 role / 默认。新线程还没有会话，级别取模型目录；
 * 目录里没有的模型（含 role 默认模型）能力未知，只给「跟随默认」，不假装全部级别都可用；
 * 模型不能调节思考时为 null（不显示圆圈）。
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

/**
 * 模型列表的页脚（模型对话框、新线程的模型弹层）：目录是旧结果时给一行淡色提示，
 * 「刷新」让控制面强制重跑目录（`?refresh=true`），结果直接写回缓存。
 */
export function ModelCatalogFooter({
	catalog,
}: {
	catalog: ModelCatalog | undefined;
}): ReactNode {
	const { t } = useTranslation();
	const qc = useQueryClient();
	const [busy, setBusy] = useState(false);
	const [error, setError] = useState<unknown>(null);
	const refresh = async () => {
		setBusy(true);
		setError(null);
		try {
			await refreshModelCatalog(qc);
		} catch (e) {
			setError(e);
		} finally {
			setBusy(false);
		}
	};
	return (
		<div className="flex w-full min-w-0 items-center gap-2 text-xs">
			<span className="min-w-0 flex-1 truncate">
				{error ? (
					<span role="alert" className="text-err">
						{describeError(error, t)}
					</span>
				) : (
					catalog?.stale && (
						<span data-testid="catalog-stale" className="text-faint">
							{t("model.catalogStale", {
								time: formatDateTime(catalog.fetchedAt),
							})}
						</span>
					)
				)}
			</span>
			<Button
				size="sm"
				variant="ghost"
				disabled={busy}
				onClick={() => void refresh()}
			>
				{busy ? <Spinner size={12} /> : <Icon name="refresh" size={13} />}
				{t("model.refresh")}
			</Button>
		</div>
	);
}

/**
 * 模型弹层的内容：自由输入框、「默认模型」、最近用过的模型、目录里的对话模型（按输入框筛选，
 * 已在「最近」里的不重复列出），页脚是目录的刷新与过期提示。
 */
export function ModelOptions({
	model,
	onModel,
}: {
	model: string;
	onModel: (model: string) => void;
}): ReactNode {
	const { t } = useTranslation();
	const recent = useRecentModels();
	const catalog = useModelCatalog();
	const q = model.trim().toLowerCase();
	const listed = useMemo(() => {
		const seen = new Set(recent);
		return chatModels(catalog.data).filter(
			(m) =>
				!seen.has(m.selector) &&
				(!q || `${m.selector} ${m.name}`.toLowerCase().includes(q)),
		);
	}, [catalog.data, recent, q]);
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
			{listed.length > 0 && <PopoverLabel>{t("model.allModels")}</PopoverLabel>}
			{listed.map((m) => (
				<Option
					key={m.selector}
					selected={model === m.selector}
					leading={<ModelLogo model={m.id} provider={m.provider} />}
					hint={m.selector}
					onClick={() => onModel(m.selector)}
				>
					{m.name || m.id}
				</Option>
			))}
			{catalog.isPending && (
				<div className="flex items-center gap-2 px-2 py-1.5 text-muted text-xs">
					<Spinner size={12} />
					{t("model.loading")}
				</div>
			)}
			<div className="px-1 pt-1">
				<ModelCatalogFooter catalog={catalog.data} />
			</div>
		</>
	);
}

/**
 * 表单里的模型 + 思考级别控件（定时对话框）：胶囊点开弹层，圆圈独立选级别。
 * 模型目录认得这个模型时，换模型后不支持的级别退回默认；认不得时（目录还没读到、目录里没有该模型）
 * 保留现有级别——清掉就成了悄悄改配置（M9 审查 W4）。
 */
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
	const known = caps !== undefined;
	const capOptions = useMemo(() => newThreadThinkingOptions(caps), [caps]);
	// 能力未知：只有「跟随默认」与现有级别两项
	const options = known || !thinking ? capOptions : ["", thinking];
	const text = modelPillText(model, t("newThread.defaultModel"));
	// 能力已知、原来选的级别新模型不支持（或不能调节）时退回「跟随默认」
	useEffect(() => {
		if (known && thinking && !capOptions?.includes(thinking)) onThinking("");
	}, [known, thinking, capOptions, onThinking]);
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
		<span className="inline-flex min-w-0 max-w-full flex-wrap items-center gap-x-2 gap-y-1">
			<span className="relative inline-flex min-w-0 max-w-full">
				{options ? (
					<ModelPill
						pill={pill}
						corner={false}
						thinking={thinking}
						options={options}
						known={known}
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
			{!known && thinking && (
				<span data-testid="thinking-unknown" className="text-faint text-xs">
					{t("schedule.thinkingUnknown")}
				</span>
			)}
		</span>
	);
}
