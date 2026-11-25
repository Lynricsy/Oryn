/**
 * 重新设计的 `ask` / `form` 交互问卷视图。
 *
 * 特性：
 * - 完整支持 omp `ask` 工具的 HTML 富文本、表格、代码块、引用、折叠块、图片（外链 / data URI / 工作区本地路径）与主题自适应内联 SVG；
 * - 卡片化单选/多选控件（自动拆解主标题与副说明、融入式「其他」自由输入）、精致下拉框与全类型输入校验；
 * - 独立可滚动内容区 + 固定底栏（进度读数、快捷键提交、清晰主次按钮）。
 */
import {
	type CSSProperties,
	Fragment,
	type ReactNode,
	useEffect,
	useId,
	useMemo,
	useRef,
	useState,
} from "react";
import { useTranslation } from "react-i18next";
import type { Orb } from "../../api/client";
import {
	type AskChoiceField,
	type AskFieldValue,
	type AskFormBlock,
	type AskFormDoc,
	type AskFormValues,
	type AskTextField,
	type AskValidationError,
	buildAskFormData,
	createInitialFormValues,
	isFieldAnswered,
	type RichNode,
	resolveAskFormDoc,
	resolveFormImageSource,
	richNodesToPlainText,
	sanitizeSvgMarkup,
	splitOptionLabel,
	validateAskForm,
} from "../../lib/ask-form";
import { orbFileSource } from "../../lib/file-source";
import { rasterMime } from "../../lib/image-src";
import { keyed } from "../../lib/keys";
import type {
	ExtensionUiRequestOf,
	ExtensionUiResponse,
} from "../../omp/types";
import { indexStore, peekThreadStore, useThread } from "../../state/stores";
import { ImageViewer } from "../ui/ImageViewer";
import { Icon } from "../ui/icons";
import { Button, Select, TextArea, TextInput } from "../ui/primitives";

type Answer = (response: ExtensionUiResponse) => void;

/** 将 omp SVG 规范约定的主题变量映射到 Oryn 当前明暗/主题色令牌。 */
const SVG_THEME_VARS = {
	"--fg": "var(--text)",
	"--muted": "var(--text-muted)",
	"--border": "var(--line)",
	"--surface": "var(--panel)",
	"--accent": "var(--brand)",
	"--success": "var(--good)",
	"--warning": "var(--caution)",
	"--error": "var(--bad)",
	"--c1": "var(--viz-1)",
	"--c2": "var(--viz-2)",
	"--c3": "var(--viz-3)",
	"--c4": "var(--viz-4)",
	"--c5": "var(--viz-5)",
	"--c6": "var(--viz-6)",
} as CSSProperties;

function getThreadOrbInfo(
	threadId: string,
): { orbId: string; offline: boolean; cwd?: string } | null {
	const s = peekThreadStore(threadId)?.getState();
	const orbId = s?.thread?.orbId ?? s?.orb?.id;
	if (!orbId) return null;
	const fromIndex: Orb | undefined = indexStore.getState().orbs[orbId];
	const fromThread = s?.orb?.id === orbId ? s.orb : undefined;
	const orb =
		fromIndex && fromThread
			? fromIndex.stateChangedAt >= fromThread.stateChangedAt
				? fromIndex
				: fromThread
			: (fromIndex ?? fromThread);
	if (!orb || orb.state === "deleted") return null;
	return {
		orbId,
		offline: orb.state === "stopped",
		cwd: s?.thread?.cwd,
	};
}

function RichContent({ nodes }: { nodes: readonly RichNode[] }): ReactNode {
	return (
		<>
			{nodes.map((node, i) => {
				const key = `${node.kind}-${i}`;
				switch (node.kind) {
					case "text":
						return <Fragment key={key}>{node.text}</Fragment>;
					case "code":
						return (
							<code key={key} className="md-inline-code">
								{node.text}
							</code>
						);
					case "strong":
						return (
							<strong key={key} className="font-semibold text-fg">
								<RichContent nodes={node.children} />
							</strong>
						);
					case "em":
						return (
							<em key={key}>
								<RichContent nodes={node.children} />
							</em>
						);
					case "link":
						return (
							<a
								key={key}
								href={node.href}
								target="_blank"
								rel="noopener noreferrer"
								className="text-accent-ink underline decoration-accent/40 underline-offset-2 transition-colors duration-150 hover:decoration-accent"
							>
								<RichContent nodes={node.children} />
							</a>
						);
					case "br":
						return <br key={key} />;
					case "image":
						return (
							<span
								key={key}
								className="inline-flex items-center gap-1 rounded bg-inset px-1.5 py-0.5 text-muted text-xs"
							>
								<Icon name="image" size={12} />
								{node.alt || node.src}
							</span>
						);
					default:
						return null;
				}
			})}
		</>
	);
}

/** 安全渲染清洗后的内联 SVG，并桥接 omp 主题变量。 */
function AskFormSvg({
	markup,
	alt,
}: {
	markup: string;
	alt: string;
}): ReactNode {
	const hostRef = useRef<HTMLDivElement>(null);
	useEffect(() => {
		if (hostRef.current) hostRef.current.innerHTML = markup;
	}, [markup]);
	return (
		<figure
			className="overflow-hidden rounded-lg border border-border/80 bg-inset/65 p-3"
			style={SVG_THEME_VARS}
		>
			<div
				ref={hostRef}
				role="img"
				aria-label={alt || undefined}
				className="flex max-h-72 items-center justify-center overflow-auto text-fg [&_svg]:block [&_svg]:h-auto [&_svg]:max-h-64 [&_svg]:max-w-full"
			/>
			{alt && (
				<figcaption className="mt-2 text-center text-faint text-xs">
					{alt}
				</figcaption>
			)}
		</figure>
	);
}

/** 渲染表单中的图片：支持外链、安全栅格 data URI、data SVG 与 orb 工作区本地图片；支持灯箱放大。 */
function AskFormImage({
	threadId,
	src,
	alt,
	width,
	height,
}: {
	threadId: string;
	src: string;
	alt: string;
	width?: string;
	height?: string;
}): ReactNode {
	const { t } = useTranslation();
	const cwd = useThread(threadId, (s) => s.thread?.cwd);
	const resolved = useMemo(() => resolveFormImageSource(src, cwd), [src, cwd]);
	const [blobUrl, setBlobUrl] = useState<string | null>(null);
	const [orbSvg, setOrbSvg] = useState<string | null>(null);
	const [loading, setLoading] = useState(resolved.kind === "orb");
	const [failed, setFailed] = useState(resolved.kind === "invalid");
	const [zoomOpen, setZoomOpen] = useState(false);

	useEffect(() => {
		if (resolved.kind !== "orb") {
			setBlobUrl(null);
			setOrbSvg(null);
			setLoading(false);
			setFailed(resolved.kind === "invalid");
			return;
		}
		const info = getThreadOrbInfo(threadId);
		if (!info) {
			setLoading(false);
			setFailed(true);
			return;
		}
		let alive = true;
		let createdUrl: string | null = null;
		setLoading(true);
		setFailed(false);
		setOrbSvg(null);
		void orbFileSource(info.orbId, threadId, { offline: info.offline })
			.readBlob(resolved.path)
			.then(async (blob) => {
				if (!alive) return;
				const lowerPath = resolved.path.toLowerCase();
				if (
					lowerPath.endsWith(".svg") ||
					blob.type.toLowerCase().includes("image/svg")
				) {
					const clean = sanitizeSvgMarkup(await blob.text());
					if (!alive) return;
					if (clean) {
						setOrbSvg(clean);
						setBlobUrl(null);
					} else {
						setFailed(true);
					}
					setLoading(false);
					return;
				}
				const safeMime =
					blob.type && rasterMime(blob.type) ? blob.type : undefined;
				const typed = safeMime ? new Blob([blob], { type: safeMime }) : blob;
				createdUrl = URL.createObjectURL(typed);
				setBlobUrl(createdUrl);
				setLoading(false);
			})
			.catch(() => {
				if (!alive) return;
				setFailed(true);
				setLoading(false);
			});
		return () => {
			alive = false;
			if (createdUrl) URL.revokeObjectURL(createdUrl);
		};
	}, [resolved, threadId]);

	if (resolved.kind === "svg") {
		return <AskFormSvg markup={resolved.markup} alt={alt} />;
	}
	if (orbSvg) {
		return <AskFormSvg markup={orbSvg} alt={alt} />;
	}

	const imgUrl =
		resolved.kind === "external" || resolved.kind === "data"
			? resolved.url
			: blobUrl;

	if (loading) {
		return (
			<div className="flex h-28 items-center justify-center rounded-lg border border-border/80 bg-inset/65 text-faint text-xs">
				<Icon name="image" size={18} className="mr-2 opacity-60" />
				{alt || src}
			</div>
		);
	}

	if (failed || !imgUrl) {
		return (
			<div className="flex items-center gap-2 rounded-lg border border-border/80 bg-inset/65 px-3 py-2.5 text-muted text-xs">
				<Icon name="image" size={15} className="shrink-0 text-faint" />
				<div className="min-w-0 flex-1">
					<div className="truncate font-medium text-fg">
						{alt || t("ui.imageLoadFailed")}
					</div>
					<div className="truncate font-mono text-2xs text-faint">{src}</div>
				</div>
			</div>
		);
	}

	return (
		<figure className="overflow-hidden rounded-lg border border-border/80 bg-inset/65 p-2">
			<button
				type="button"
				onClick={() => setZoomOpen(true)}
				title={t("ui.imageZoomHint")}
				className="group relative mx-auto flex max-w-full cursor-zoom-in items-center justify-center rounded-md focus-visible:outline-2 focus-visible:outline-[var(--ring)] focus-visible:outline-offset-2"
			>
				<img
					src={imgUrl}
					alt={alt}
					referrerPolicy={
						resolved.kind === "external" ? "no-referrer" : undefined
					}
					style={{ width, height }}
					onError={() => setFailed(true)}
					className="block max-h-60 max-w-full rounded-md object-contain"
				/>
				<span
					aria-hidden="true"
					className="pointer-events-none absolute right-1.5 bottom-1.5 inline-flex items-center gap-1 rounded-md bg-panel/90 px-1.5 py-0.5 text-2xs text-muted opacity-0 shadow-xs backdrop-blur-xs transition-opacity duration-150 group-hover:opacity-100"
				>
					<Icon name="zoomIn" size={11} />
				</span>
			</button>
			{alt && (
				<figcaption className="mt-1.5 text-center text-faint text-xs">
					{alt}
				</figcaption>
			)}
			{zoomOpen && (
				<ImageViewer
					src={imgUrl}
					alt={alt || src}
					onClose={() => setZoomOpen(false)}
				/>
			)}
		</figure>
	);
}

/** 单选圆环 / 多选方框指示器（与工具流问卷视觉一致，命中强调色）。 */
function ChoiceIndicator({
	multi,
	checked,
}: {
	multi: boolean;
	checked: boolean;
}): ReactNode {
	return (
		<span
			aria-hidden="true"
			className={`mt-0.5 grid h-4 w-4 shrink-0 place-items-center border transition-colors duration-150 ${
				multi ? "rounded-[5px]" : "rounded-full"
			} ${
				checked
					? "border-accent bg-accent text-accent-fg"
					: "border-border-strong bg-panel group-hover:border-muted"
			}`}
		>
			{checked &&
				(multi ? (
					<Icon name="check" size={10} weight="bold" />
				) : (
					<span className="h-1.5 w-1.5 rounded-full bg-accent-fg" />
				))}
		</span>
	);
}

function FieldErrorLine({ error }: { error?: AskValidationError }): ReactNode {
	const { t } = useTranslation();
	if (!error) return null;
	return (
		<p role="alert" className="mt-1 flex items-center gap-1.5 text-err text-xs">
			<Icon name="alert" size={13} weight="fill" className="shrink-0" />
			<span>
				{error.params
					? t(`ui.validation.${error.key}`, error.params)
					: t(`ui.validation.${error.key}`)}
			</span>
		</p>
	);
}

function QuestionHeader({
	index,
	showIndex,
	label,
	required,
	multi,
	hint,
}: {
	index: number;
	showIndex: boolean;
	label: readonly RichNode[];
	required: boolean;
	multi?: boolean;
	hint?: string;
}): ReactNode {
	const { t } = useTranslation();
	if (label.length === 0 && !required && !multi && !hint) return null;
	return (
		<div className="mb-2 flex items-start gap-2">
			{showIndex && (
				<span className="mt-0.5 grid h-5 min-w-5 shrink-0 place-items-center rounded-md bg-inset px-1 font-medium font-mono text-2xs text-muted tabular-nums">
					{index}
				</span>
			)}
			<div className="min-w-0 flex-1 text-[13.5px] text-fg leading-snug">
				{label.length > 0 && (
					<span className="font-medium">
						<RichContent nodes={label} />
					</span>
				)}
				{required && (
					<span
						className="ml-1.5 inline-block text-err text-xs"
						title={t("ui.required")}
					>
						*
					</span>
				)}
				{multi && (
					<span className="ml-2 inline-block rounded bg-inset px-1.5 py-0.5 align-middle font-normal text-2xs text-faint">
						{t("ui.multiChoice")}
					</span>
				)}
				{hint && (
					<span className="ml-2 font-mono text-2xs text-faint">{hint}</span>
				)}
			</div>
		</div>
	);
}

function ChoiceFieldView({
	field,
	index,
	showIndex,
	sectionTitleUsed,
	value,
	error,
	onChange,
}: {
	field: AskChoiceField;
	index: number;
	showIndex: boolean;
	sectionTitleUsed: boolean;
	value: AskFieldValue;
	error?: AskValidationError;
	onChange: (next: AskFieldValue) => void;
}): ReactNode {
	const { t } = useTranslation();
	const groupId = useId();
	const otherInputRef = useRef<HTMLInputElement>(null);

	// `<select>` 单选：使用项目统一的 Select 控件（支持 optgroup）
	if (field.select && !field.multi) {
		const groups: { name?: string; opts: typeof field.options }[] = [];
		for (const opt of field.options) {
			const last = groups[groups.length - 1];
			if (last && last.name === opt.group) last.opts.push(opt);
			else groups.push({ name: opt.group, opts: [opt] });
		}
		const current = value.selected[0] ?? "";
		return (
			<div data-field-name={field.name} className="flex flex-col">
				{!sectionTitleUsed && (
					<QuestionHeader
						index={index}
						showIndex={showIndex}
						label={field.label}
						required={field.required}
					/>
				)}
				<Select
					id={groupId}
					name={field.name}
					value={current}
					aria-invalid={error ? true : undefined}
					className={error ? "border-err!" : ""}
					onChange={(e) =>
						onChange({
							...value,
							selected: e.target.value ? [e.target.value] : [],
						})
					}
				>
					{!current && (
						<option value="" disabled={field.required}>
							{t("ui.selectPlaceholder")}
						</option>
					)}
					{groups.map((g) =>
						g.name ? (
							<optgroup
								key={`g:${g.name}:${g.opts[0]?.value ?? ""}`}
								label={g.name}
							>
								{g.opts.map((opt) => (
									<option
										key={opt.value}
										value={opt.value}
										disabled={opt.disabled}
									>
										{opt.plainLabel}
									</option>
								))}
							</optgroup>
						) : (
							g.opts.map((opt) => (
								<option
									key={opt.value}
									value={opt.value}
									disabled={opt.disabled}
								>
									{opt.plainLabel}
								</option>
							))
						),
					)}
				</Select>
				<FieldErrorLine error={error} />
			</div>
		);
	}

	const toggleOption = (optValue: string) => {
		if (field.multi) {
			const has = value.selected.includes(optValue);
			onChange({
				...value,
				selected: has
					? value.selected.filter((v) => v !== optValue)
					: [...value.selected, optValue],
			});
		} else {
			onChange({
				...value,
				selected: [optValue],
				otherActive: false,
			});
		}
	};

	const otherChecked = field.multi
		? value.text.trim() !== ""
		: value.otherActive === true;

	return (
		<fieldset
			data-field-name={field.name}
			aria-invalid={error ? true : undefined}
			className="m-0 flex flex-col border-0 p-0"
		>
			{!sectionTitleUsed && field.label.length > 0 && (
				<legend className="w-full p-0">
					<QuestionHeader
						index={index}
						showIndex={showIndex}
						label={field.label}
						required={field.required}
						multi={field.multi && !field.standalone}
					/>
				</legend>
			)}
			<div className="flex flex-col gap-1.5">
				{field.options.map((opt) => {
					const checked =
						!(!field.multi && value.otherActive) &&
						value.selected.includes(opt.value);
					const hasFormatting = opt.label.some((n) => n.kind !== "text");
					const split = hasFormatting ? null : splitOptionLabel(opt.plainLabel);
					return (
						<label
							key={opt.value}
							className={`group relative flex cursor-pointer items-start gap-2.5 rounded-lg border px-3 py-2 text-left transition-[background-color,border-color,box-shadow] duration-150 focus-within:ring-2 focus-within:ring-accent/25 ${
								opt.disabled ? "cursor-not-allowed opacity-50" : ""
							} ${
								checked
									? "border-accent bg-accent-soft/80 shadow-[inset_0_0_0_1px_color-mix(in_oklab,var(--brand)_20%,transparent)]"
									: error
										? "border-err/40 bg-panel/75 hover:border-err/70 hover:bg-hover/65"
										: "border-border/90 bg-panel/75 hover:border-border-strong hover:bg-hover/65"
							}`}
						>
							<input
								type={field.multi ? "checkbox" : "radio"}
								name={field.name}
								value={opt.value}
								checked={checked}
								disabled={opt.disabled}
								onChange={() => toggleOption(opt.value)}
								className="sr-only"
							/>
							<ChoiceIndicator multi={field.multi} checked={checked} />
							<span className="min-w-0 flex-1">
								<span className="flex flex-wrap items-baseline gap-x-2 gap-y-0.5">
									<span
										className={`text-[13.5px] leading-snug ${checked ? "font-medium text-fg" : "text-fg"}`}
									>
										{split ? split.title : <RichContent nodes={opt.label} />}
									</span>
									{opt.checked && !field.standalone && (
										<span className="rounded bg-accent-soft px-1.5 py-0.5 font-medium text-2xs text-accent-ink">
											{t("ui.recommended")}
										</span>
									)}
								</span>
								{split?.description && (
									<span className="mt-0.5 block text-muted text-xs leading-relaxed">
										{split.description}
									</span>
								)}
							</span>
						</label>
					);
				})}
				{field.other && (
					// biome-ignore lint/a11y/useKeyWithClickEvents: 内部包含真实 input 与单选/复选语义，点击整行聚焦输入框
					// biome-ignore lint/a11y/noStaticElementInteractions: 同上
					<div
						onClick={() => {
							if (!field.multi && !value.otherActive) {
								onChange({ ...value, otherActive: true });
							}
							otherInputRef.current?.focus();
						}}
						className={`group flex cursor-text items-center gap-2.5 rounded-lg border px-3 py-2 transition-[background-color,border-color,box-shadow] duration-150 focus-within:ring-2 focus-within:ring-accent/25 ${
							otherChecked
								? "border-accent bg-accent-soft/80 shadow-[inset_0_0_0_1px_color-mix(in_oklab,var(--brand)_20%,transparent)]"
								: "border-border/80 border-dashed bg-panel/55 hover:border-border-strong hover:bg-hover/55"
						}`}
					>
						<ChoiceIndicator multi={field.multi} checked={otherChecked} />
						<input
							ref={otherInputRef}
							type="text"
							aria-label={`${richNodesToPlainText(field.label) || field.name} - ${t("ui.otherAnswer")}`}
							placeholder={t("ui.otherAnswer")}
							value={value.text}
							onFocus={() => {
								if (!field.multi && !value.otherActive) {
									onChange({ ...value, otherActive: true });
								}
							}}
							onChange={(e) => {
								const nextText = e.target.value;
								onChange({
									...value,
									text: nextText,
									otherActive: !field.multi
										? nextText !== "" || value.otherActive
										: undefined,
								});
							}}
							className="min-w-0 flex-1 bg-transparent text-[13.5px] text-fg outline-none placeholder:text-faint"
						/>
						{value.text && (
							<button
								type="button"
								aria-label={t("common.clear")}
								onClick={(e) => {
									e.stopPropagation();
									onChange({
										...value,
										text: "",
										otherActive: false,
									});
								}}
								className="inline-flex h-5 w-5 shrink-0 items-center justify-center rounded text-faint hover:bg-hover hover:text-fg"
							>
								<Icon name="close" size={11} />
							</button>
						)}
					</div>
				)}
			</div>
			<FieldErrorLine error={error} />
		</fieldset>
	);
}

function TextFieldView({
	field,
	index,
	showIndex,
	sectionTitleUsed,
	value,
	error,
	onChange,
}: {
	field: AskTextField;
	index: number;
	showIndex: boolean;
	sectionTitleUsed: boolean;
	value: AskFieldValue;
	error?: AskValidationError;
	onChange: (next: AskFieldValue) => void;
}): ReactNode {
	const inputId = useId();
	const rangeHint =
		field.type === "number" &&
		(field.min !== undefined || field.max !== undefined)
			? field.min !== undefined && field.max !== undefined
				? `${field.min}..${field.max}`
				: field.min !== undefined
					? `≥ ${field.min}`
					: `≤ ${field.max}`
			: undefined;

	return (
		<div data-field-name={field.name} className="flex flex-col">
			{!sectionTitleUsed && (
				<label htmlFor={inputId} className="block">
					<QuestionHeader
						index={index}
						showIndex={showIndex}
						label={field.label}
						required={field.required}
						hint={rangeHint}
					/>
				</label>
			)}
			{field.multiline ? (
				<TextArea
					id={inputId}
					name={field.name}
					mono={false}
					rows={field.rows}
					value={value.text}
					placeholder={field.placeholder}
					minLength={field.minLength}
					maxLength={field.maxLength}
					aria-invalid={error ? true : undefined}
					className={`rounded-lg bg-panel/80 px-3 py-2 text-[13.5px] leading-relaxed ${error ? "border-err!" : ""}`}
					onChange={(e) => onChange({ ...value, text: e.target.value })}
				/>
			) : (
				<TextInput
					id={inputId}
					name={field.name}
					type={field.type || "text"}
					value={value.text}
					placeholder={field.placeholder}
					min={field.min}
					max={field.max}
					step={field.step}
					minLength={field.minLength}
					maxLength={field.maxLength}
					pattern={field.pattern}
					aria-invalid={error ? true : undefined}
					className={`rounded-lg bg-panel/80 px-3 py-2 text-[13.5px] ${field.type === "number" ? "tabular-nums" : ""} ${error ? "border-err!" : ""}`}
					onChange={(e) => onChange({ ...value, text: e.target.value })}
				/>
			)}
			<FieldErrorLine error={error} />
		</div>
	);
}

function FormBlockView({
	threadId,
	block,
	fieldIndexMap,
	showIndex,
	sectionTitleFieldName,
	values,
	errors,
	onFieldChange,
}: {
	threadId: string;
	block: AskFormBlock;
	fieldIndexMap: Map<string, number>;
	showIndex: boolean;
	sectionTitleFieldName: string | null;
	values: AskFormValues;
	errors: Record<string, AskValidationError>;
	onFieldChange: (name: string, next: AskFieldValue) => void;
}): ReactNode {
	switch (block.kind) {
		case "heading":
			return (
				<h4 className="font-semibold text-[13.5px] text-fg leading-snug">
					<RichContent nodes={block.nodes} />
				</h4>
			);
		case "paragraph":
			return (
				<p className="text-[13px] text-muted leading-relaxed">
					<RichContent nodes={block.nodes} />
				</p>
			);
		case "list": {
			const ListTag = block.ordered ? "ol" : "ul";
			return (
				<ListTag
					className={`${block.ordered ? "list-decimal" : "list-disc"} flex flex-col gap-1 pl-5 text-[13px] text-muted leading-relaxed`}
				>
					{block.items.map((item, idx) => (
						// biome-ignore lint/suspicious/noArrayIndexKey: 列表项无稳定 id
						<li key={idx}>
							<RichContent nodes={item} />
						</li>
					))}
				</ListTag>
			);
		}
		case "pre":
			return (
				<pre className="m-0 max-h-52 overflow-auto rounded-lg border border-border/70 bg-inset px-3 py-2 font-mono text-2xs text-fg leading-relaxed">
					{block.text}
				</pre>
			);
		case "blockquote":
			return (
				<blockquote className="m-0 rounded-r-lg border-accent/50 border-l-2 bg-inset/60 py-1.5 pr-3 pl-3 text-[13px] text-muted leading-relaxed">
					<RichContent nodes={block.nodes} />
				</blockquote>
			);
		case "hr":
			return <hr className="my-1 border-0 border-border border-t" />;
		case "table":
			return (
				<div className="overflow-x-auto rounded-lg border border-border/90 bg-panel/60">
					<table className="w-full border-collapse text-xs">
						<tbody>
							{keyed(block.rows, "r").map(({ key: rk, item: row }, ri) => (
								<tr
									key={rk}
									className={`${ri < block.headerRows ? "bg-raised/85 font-medium text-fg" : "text-muted"} ${ri > 0 ? "border-border/80 border-t" : ""}`}
								>
									{keyed(row, "c").map(({ key: ck, item: cell }) => (
										<td
											key={ck}
											className="px-3 py-1.5 text-left align-top tabular-nums"
										>
											<RichContent nodes={cell} />
										</td>
									))}
								</tr>
							))}
						</tbody>
					</table>
				</div>
			);
		case "image":
			return (
				<AskFormImage
					threadId={threadId}
					src={block.src}
					alt={block.alt}
					width={block.width}
					height={block.height}
				/>
			);
		case "svg":
			return <AskFormSvg markup={block.markup} alt={block.alt} />;
		case "details":
			return (
				<details
					open={block.open}
					className="rounded-lg border border-border/80 bg-inset/50 px-3 py-2 text-[13px]"
				>
					<summary className="cursor-pointer select-none font-medium text-fg">
						<RichContent nodes={block.summary} />
					</summary>
					<div className="mt-2 flex flex-col gap-2">
						{block.blocks.map((inner, idx) => (
							<FormBlockView
								// biome-ignore lint/suspicious/noArrayIndexKey: 块无 id
								key={idx}
								threadId={threadId}
								block={inner}
								fieldIndexMap={fieldIndexMap}
								showIndex={showIndex}
								sectionTitleFieldName={null}
								values={values}
								errors={errors}
								onFieldChange={onFieldChange}
							/>
						))}
					</div>
				</details>
			);
		case "field": {
			const f = block.field;
			const val = values[f.name] ?? { text: "", selected: [] };
			const idx = fieldIndexMap.get(f.name) ?? 1;
			const sectionTitleUsed = sectionTitleFieldName === f.name;
			if (f.kind === "text") {
				return (
					<TextFieldView
						field={f}
						index={idx}
						showIndex={showIndex}
						sectionTitleUsed={sectionTitleUsed}
						value={val}
						error={errors[f.name]}
						onChange={(next) => onFieldChange(f.name, next)}
					/>
				);
			}
			return (
				<ChoiceFieldView
					field={f}
					index={idx}
					showIndex={showIndex}
					sectionTitleUsed={sectionTitleUsed}
					value={val}
					error={errors[f.name]}
					onChange={(next) => onFieldChange(f.name, next)}
				/>
			);
		}
	}
}

/** 交互式 `ask` / `form` 主体：独立滚动正文 + 固定底栏。 */
export function AskFormBody({
	threadId,
	req,
	answer,
}: {
	threadId: string;
	req: ExtensionUiRequestOf<"form">;
	answer: Answer;
}): ReactNode {
	const { t } = useTranslation();
	const liveTools = useThread(threadId, (s) => s.liveTools);
	const liveToolOrder = useThread(threadId, (s) => s.liveToolOrder);
	const entries = useThread(threadId, (s) => s.entries);

	const doc: AskFormDoc = useMemo(
		() => resolveAskFormDoc(req.sections, liveTools, liveToolOrder, entries),
		[req.sections, liveTools, liveToolOrder, entries],
	);

	const [values, setValues] = useState<AskFormValues>(() =>
		createInitialFormValues(doc),
	);
	const [errors, setErrors] = useState<Record<string, AskValidationError>>({});
	const formRef = useRef<HTMLFormElement>(null);

	// 当底层解析从 RPC 升级为匹配到的 HTML 或字段集变化时，保留已有输入并补齐新字段初始值
	useEffect(() => {
		const defaults = createInitialFormValues(doc);
		setValues((prev) => {
			const merged: AskFormValues = { ...defaults };
			for (const f of doc.fields) {
				const existing = prev[f.name];
				if (existing) merged[f.name] = existing;
			}
			return merged;
		});
	}, [doc]);

	const fieldIndexMap = useMemo(() => {
		const map = new Map<string, number>();
		for (let i = 0; i < doc.fields.length; i++) {
			const f = doc.fields[i];
			if (f) map.set(f.name, i + 1);
		}
		return map;
	}, [doc.fields]);
	const totalFields = doc.fields.length;
	const answeredCount = doc.fields.filter((f) =>
		isFieldAnswered(f, values[f.name]),
	).length;
	const showIndex = totalFields > 1;
	const isMac =
		typeof navigator !== "undefined" &&
		/mac|iphone|ipad/i.test(navigator.platform || navigator.userAgent);
	const shortcutKey = isMac ? "⌘↵" : "Ctrl↵";

	const handleFieldChange = (name: string, next: AskFieldValue) => {
		setValues((prev) => ({ ...prev, [name]: next }));
		setErrors((prev) => {
			if (!prev[name]) return prev;
			const copy = { ...prev };
			delete copy[name];
			return copy;
		});
	};

	const submitForm = () => {
		const errs = validateAskForm(doc, values);
		if (Object.keys(errs).length > 0) {
			setErrors(errs);
			const firstBad = Object.keys(errs)[0];
			if (firstBad && formRef.current) {
				const formEl = formRef.current;
				requestAnimationFrame(() => {
					const target = formEl.querySelector(
						`[data-field-name="${CSS.escape(firstBad)}"]`,
					);
					target?.scrollIntoView?.({ behavior: "smooth", block: "nearest" });
				});
			}
			return;
		}
		const formData = buildAskFormData(doc, values);
		answer({ type: "extension_ui_response", id: req.id, formData });
	};

	return (
		<form
			ref={formRef}
			noValidate
			data-testid="ask-form"
			data-ask-source={doc.source}
			className="flex min-h-0 flex-1 flex-col"
			onKeyDown={(e) => {
				if ((e.metaKey || e.ctrlKey) && e.key === "Enter") {
					e.preventDefault();
					submitForm();
				}
			}}
			onSubmit={(e) => {
				e.preventDefault();
				submitForm();
			}}
		>
			<div className="min-h-0 flex-1 overflow-y-auto px-4 pt-3 pb-4">
				<div className="flex flex-col gap-4">
					{doc.sections.map((sec, si) => {
						// 若分节有标题且恰好包裹一个无自有标签的字段，把分节标题直接作为该题题干并配编号，避免双标题
						const fieldBlocks = sec.blocks.filter(
							(b): b is Extract<AskFormBlock, { kind: "field" }> =>
								b.kind === "field",
						);
						const firstField = fieldBlocks[0]?.field ?? null;
						const singleField =
							sec.title.length > 0 &&
							fieldBlocks.length === 1 &&
							firstField &&
							firstField.label.length === 0
								? firstField
								: null;
						const singleIndex = singleField
							? (fieldIndexMap.get(singleField.name) ?? 1)
							: 0;
						const multiChoice =
							singleField?.kind === "choice" &&
							singleField.multi &&
							!singleField.standalone;

						return (
							<section
								// biome-ignore lint/suspicious/noArrayIndexKey: 表单分节无 id
								key={si}
								className={
									si > 0 && sec.title.length > 0
										? "flex flex-col gap-2.5 border-border/60 border-t pt-3.5"
										: "flex flex-col gap-2.5"
								}
							>
								{sec.title.length > 0 &&
									(singleField ? (
										<QuestionHeader
											index={singleIndex}
											showIndex={showIndex}
											label={sec.title}
											required={singleField.required}
											multi={multiChoice}
										/>
									) : (
										<h4 className="font-semibold text-[13.5px] text-fg leading-snug">
											<RichContent nodes={sec.title} />
										</h4>
									))}
								{sec.blocks.map((block, bi) => (
									<FormBlockView
										// biome-ignore lint/suspicious/noArrayIndexKey: 块无稳定 id
										key={`${si}-${bi}`}
										threadId={threadId}
										block={block}
										fieldIndexMap={fieldIndexMap}
										showIndex={showIndex}
										sectionTitleFieldName={singleField?.name ?? null}
										values={values}
										errors={errors}
										onFieldChange={handleFieldChange}
									/>
								))}
							</section>
						);
					})}
				</div>
			</div>
			<div className="flex shrink-0 items-center gap-2 border-[var(--glass-line)] border-t px-4 py-2.5">
				<div className="flex min-w-0 flex-1 items-center gap-2.5 text-faint text-xs">
					{totalFields > 1 && (
						<span className="tabular-nums">
							{t("ui.formProgress", {
								answered: answeredCount,
								total: totalFields,
							})}
						</span>
					)}
					<span className="hidden text-2xs sm:inline">
						{t("ui.submitShortcut", { key: shortcutKey })}
					</span>
				</div>
				<Button
					size="sm"
					variant="ghost"
					onClick={() =>
						answer({
							type: "extension_ui_response",
							id: req.id,
							cancelled: true,
						})
					}
				>
					{t("common.cancel")}
				</Button>
				<Button type="submit" size="sm" variant="accent" icon="check">
					{t("common.submit")}
				</Button>
			</div>
		</form>
	);
}
