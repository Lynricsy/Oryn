/**
 * 元素直接编辑（计划 M14 3.6）：选择模式下选中页面元素时显示在查看器侧栏。字段：文字（仅内容全是文字的元素）、颜色、
 * 背景、字号、字重（100–900 九档）、对齐（左 / 中 / 右）、内边距、圆角；颜色框接受 3 位简写并展开成 6 位。
 *
 * - 字段一变就经帧通道发 `preview-style` / `preview-text`，页面立即显示；停手 800 ms 后保存
 *   （`POST …/element`，落成一个作者为「你」的版本）。换元素或面板关掉时把还没保存的改动立即保存。
 * - 保存请求串行排队；`baseVersion` 取「帧当前版本」与「上一次保存返回的版本」中较大的。
 * - 409（保存前来了新版本）：提示后还原预览、清除选中，排在后面的保存一并作废。
 * - 保存成功后发 `select-source`：新版本到达、帧重载后页面仍选中同一个元素（编辑不改起始标签的偏移）。
 *
 * 只有跟随最新版本、不在回收站、且元素带本次响应源码标记（`src` 非空）时可编辑，否则显示原因。
 */
import { type ReactNode, useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import type {
	PageSelection,
	ParentMessageBody,
} from "../../../sandbox/runtime/messages";
import {
	type ArtifactElementEdit,
	editArtifactElement,
	fetchArtifactFileBytes,
} from "../../api/artifact-queries";
import {
	type EditableProp,
	type EditableStyle,
	expandHex,
	MAX_EDIT_TEXT_RUNES,
	panelStyle,
	sourceTextAt,
	validEditText,
	validStyleValue,
} from "../../lib/artifacts/editable";
import { showToast } from "../../state/toasts";
import {
	describeError,
	Field,
	IconButton,
	Select,
	Spinner,
	TextArea,
	TextInput,
	tabClass,
} from "../ui/primitives";

/** 停手多久后保存。 */
export const EDIT_SAVE_DELAY_MS = 800;

/** 不能编辑的原因（由查看器给出）：看的是旧版本、artifact 在回收站里，或是设计画布的源码帧。 */
export type EditBlock = "old-version" | "deleted" | "design-source";

export interface EditPanelProps {
	artifactId: string;
	selection: PageSelection;
	/** 帧当前显示的版本（跟随最新时 = 最新版本）。 */
	version: number;
	block: EditBlock | null;
	/** 发给帧里的页面；帧重建期间为 `null`。 */
	post: ((body: ParentMessageBody) => void) | null;
	/** 保存冲突后清除选中。 */
	onConflict(): void;
	onClose(): void;
}

type SaveStatus =
	| { kind: "idle" }
	| { kind: "saving" }
	| { kind: "saved"; version: number }
	| { kind: "failed"; reason: string };

type ElementEdit = Omit<ArtifactElementEdit, "baseVersion">;

const WEIGHTS = ["100", "200", "300", "400", "500", "600", "700", "800", "900"];
const ALIGNS = ["left", "center", "right"] as const;
const PX_PROPS: readonly EditableProp[] = ["font-size", "border-radius"];

/** 输入框里的原文换成要写回的值；不合规（含空）回 `null`。字号与圆角的输入框不带单位。 */
export function fieldValue(prop: EditableProp, raw: string): string | null {
	const s = raw.trim();
	if (s === "") return null;
	let v = s;
	if (prop === "color" || prop === "background-color") v = expandHex(s) ?? "";
	else if (PX_PROPS.includes(prop)) v = `${s}px`;
	return validStyleValue(prop, v) ? v : null;
}

/** 面板初始值（页面回报的计算值规整后）换成输入框的原文。 */
function initialRaw(style: EditableStyle): EditableStyle {
	const out = panelStyle(style);
	for (const prop of PX_PROPS) out[prop] = out[prop].replace(/px$/, "");
	return out;
}

/** 解码 HTML 实体（原文里已确认没有标签）；DOMParser 生成的文档是惰性的，不执行任何东西。 */
function decodeEntities(raw: string): string {
	if (!raw.includes("&")) return raw;
	const doc = new DOMParser().parseFromString(
		`<!doctype html><body><div>${raw}</div>`,
		"text/html",
	);
	return doc.body.textContent ?? raw;
}

export function EditPanel({
	artifactId,
	selection,
	version,
	block,
	post,
	onConflict,
	onClose,
}: EditPanelProps): ReactNode {
	const { t } = useTranslation();
	const [status, setStatus] = useState<SaveStatus>({ kind: "idle" });
	const queue = useRef<Promise<void>>(Promise.resolve());
	const lastSaved = useRef(0);
	const inflight = useRef(0);
	// 冲突后递增：排在后面的保存作废
	const epoch = useRef(0);
	const latest = useRef({ artifactId, version, post, onConflict, t });
	latest.current = { artifactId, version, post, onConflict, t };

	const save = (edit: ElementEdit) => {
		const at = epoch.current;
		inflight.current++;
		setStatus({ kind: "saving" });
		queue.current = queue.current.then(async () => {
			const cur = latest.current;
			let next: SaveStatus | null = null;
			try {
				if (at !== epoch.current) return;
				const baseVersion = Math.max(cur.version, lastSaved.current);
				const r = await editArtifactElement(cur.artifactId, {
					...edit,
					baseVersion,
				});
				if (r.kind === "conflict") {
					epoch.current++;
					showToast({ tone: "err", text: cur.t("artifacts.edit.conflict") });
					latest.current.post?.({ kind: "preview-reset" });
					latest.current.onConflict();
					next = { kind: "idle" };
					return;
				}
				lastSaved.current = Math.max(lastSaved.current, r.version);
				latest.current.post?.({ kind: "select-source", src: edit.offset });
				next = { kind: "saved", version: r.version };
			} catch (e) {
				latest.current.post?.({ kind: "preview-reset" });
				next = { kind: "failed", reason: describeError(e, cur.t) };
			} finally {
				inflight.current--;
				// 后面还有排队的保存时仍显示「正在保存…」
				setStatus(
					inflight.current > 0
						? { kind: "saving" }
						: (next ?? { kind: "idle" }),
				);
			}
		});
	};

	let body: ReactNode;
	if (block === "old-version") body = t("artifacts.edit.oldVersion");
	else if (block === "deleted") body = t("artifacts.edit.deleted");
	else if (block === "design-source" || selection.loc)
		body = t("artifacts.edit.designSource");
	else if (selection.src === null) body = t("artifacts.edit.scripted");

	return (
		<div data-testid="edit-panel" className="flex min-h-0 flex-1 flex-col">
			<div className="flex h-10 shrink-0 items-center gap-2 border-border border-b pr-1.5 pl-3">
				<h2 className="min-w-0 flex-1 truncate font-semibold text-sm">
					{t("artifacts.edit.title")}
				</h2>
				<code className="shrink-0 rounded bg-inset px-1.5 font-mono text-2xs text-muted leading-5">
					{`<${selection.tag}>`}
				</code>
				<IconButton
					icon="close"
					box="sm"
					size={14}
					label={t("artifacts.edit.close")}
					onClick={onClose}
				/>
			</div>
			{body !== undefined || selection.src === null ? (
				<p
					data-testid="edit-blocked"
					className="px-3 py-6 text-center text-muted text-sm"
				>
					{body}
				</p>
			) : (
				<ElementForm
					key={`${selection.path}:${selection.src}`}
					artifactId={artifactId}
					selection={selection}
					offset={selection.src}
					version={version}
					post={post}
					save={save}
				/>
			)}
			<div
				role="status"
				aria-live="polite"
				data-testid="edit-status"
				className="flex min-h-9 shrink-0 items-center gap-2 border-border border-t px-3 text-muted text-xs"
			>
				{status.kind === "saving" && (
					<>
						<Spinner size={12} />
						{t("artifacts.edit.saving")}
					</>
				)}
				{status.kind === "saved" &&
					t("artifacts.edit.saved", { n: status.version })}
				{status.kind === "failed" && (
					<span className="text-err">
						{t("artifacts.edit.failed", { reason: status.reason })}
					</span>
				)}
			</div>
		</div>
	);
}

type TextState =
	| { kind: "loading" }
	| { kind: "ready"; value: string }
	| { kind: "unavailable" };

/** 一个元素的编辑表单；换元素 = 换 key 重建（卸载时把没保存的改动立即保存）。 */
function ElementForm({
	artifactId,
	selection,
	offset,
	version,
	post,
	save,
}: {
	artifactId: string;
	selection: PageSelection;
	offset: number;
	version: number;
	post: ((body: ParentMessageBody) => void) | null;
	save(edit: ElementEdit): void;
}): ReactNode {
	const { t } = useTranslation();
	const [raw, setRaw] = useState(() => initialRaw(selection.style));
	const [text, setText] = useState<TextState>(() =>
		selection.textOnly ? { kind: "loading" } : { kind: "unavailable" },
	);
	// 还没保存的改动（只含合规的值）
	const pendingStyle = useRef<Partial<EditableStyle>>({});
	const pendingText = useRef<string | undefined>(undefined);
	const timer = useRef<number | undefined>(undefined);
	const live = useRef({ post, save, selection, offset });
	live.current = { post, save, selection, offset };

	const flush = useRef(() => {
		clearTimeout(timer.current);
		timer.current = undefined;
		const style = pendingStyle.current;
		const txt = pendingText.current;
		const hasStyle = Object.keys(style).length > 0;
		if (!hasStyle && txt === undefined) return;
		pendingStyle.current = {};
		pendingText.current = undefined;
		const { selection: sel, offset: at } = live.current;
		live.current.save({
			path: sel.path,
			offset: at,
			tag: sel.tag,
			...(hasStyle ? { style } : {}),
			...(txt !== undefined ? { text: txt } : {}),
		});
	});

	// 卸载（换元素、关面板）时立即保存没保存的改动
	useEffect(() => {
		const f = flush.current;
		return () => f();
	}, []);

	// 帧重载后页面回报同一元素：把还没保存的预览重新发过去
	useEffect(() => {
		void selection;
		const style = pendingStyle.current;
		if (Object.keys(style).length > 0)
			live.current.post?.({ kind: "preview-style", style });
		if (pendingText.current !== undefined)
			live.current.post?.({ kind: "preview-text", text: pendingText.current });
	}, [selection]);

	// 文字框要填完整的原文（页面回报的只是前 120 个字符的摘录）
	const textOnly = selection.textOnly;
	const path = selection.path;
	const tag = selection.tag;
	// biome-ignore lint/correctness/useExhaustiveDependencies: 只在建表单时读一次原文；之后的版本是自己保存出来的
	useEffect(() => {
		if (!textOnly) return;
		let alive = true;
		fetchArtifactFileBytes(artifactId, version, path).then(
			(bytes) => {
				if (!alive) return;
				const src = sourceTextAt(bytes, offset, tag);
				setText(
					src === null
						? { kind: "unavailable" }
						: { kind: "ready", value: decodeEntities(src) },
				);
			},
			() => {
				if (alive) setText({ kind: "unavailable" });
			},
		);
		return () => {
			alive = false;
		};
	}, [artifactId, path, offset, tag, textOnly]);

	const schedule = () => {
		clearTimeout(timer.current);
		timer.current = window.setTimeout(
			() => flush.current(),
			EDIT_SAVE_DELAY_MS,
		);
	};

	const change = (prop: EditableProp, value: string) => {
		setRaw((r) => ({ ...r, [prop]: value }));
		const v = fieldValue(prop, value);
		if (v === null) return;
		pendingStyle.current = { ...pendingStyle.current, [prop]: v };
		post?.({ kind: "preview-style", style: { [prop]: v } });
		schedule();
	};

	const changeText = (value: string) => {
		setText({ kind: "ready", value });
		if (!validEditText(value)) return;
		pendingText.current = value;
		post?.({ kind: "preview-text", text: value });
		schedule();
	};

	const invalid = (prop: EditableProp) =>
		raw[prop].trim() !== "" && fieldValue(prop, raw[prop]) === null;

	const colorField = (prop: "color" | "background-color", label: string) => {
		const hex = fieldValue(prop, raw[prop]);
		return (
			<Field label={label}>
				<span className="flex items-center gap-2">
					<input
						type="color"
						aria-label={label}
						data-testid={`edit-${prop}-swatch`}
						value={hex ? hex.slice(0, 7) : "#000000"}
						onChange={(e) => change(prop, e.target.value)}
						className="h-8 w-9 shrink-0 cursor-pointer rounded-md border border-border bg-inset p-0.5"
					/>
					<TextInput
						data-testid={`edit-${prop}`}
						value={raw[prop]}
						spellCheck={false}
						placeholder="#000000"
						aria-invalid={invalid(prop) || undefined}
						onChange={(e) => change(prop, e.target.value)}
						className="font-mono"
					/>
				</span>
			</Field>
		);
	};

	const pxField = (prop: "font-size" | "border-radius", label: string) => (
		<Field label={label}>
			<span className="flex items-center gap-1.5">
				<TextInput
					data-testid={`edit-${prop}`}
					type="number"
					inputMode="decimal"
					min={prop === "font-size" ? 1 : 0}
					max={9999}
					step={1}
					value={raw[prop]}
					aria-invalid={invalid(prop) || undefined}
					onChange={(e) => change(prop, e.target.value)}
				/>
				<span className="text-faint text-xs">px</span>
			</span>
		</Field>
	);

	const tooLong = text.kind === "ready" && !validEditText(text.value);

	return (
		<div className="flex min-h-0 flex-1 flex-col gap-3 overflow-y-auto p-3">
			{selection.textOnly && (
				<Field
					label={t("artifacts.edit.text")}
					hint={
						tooLong
							? t("artifacts.edit.textTooLong", { max: MAX_EDIT_TEXT_RUNES })
							: text.kind === "unavailable"
								? t("artifacts.edit.textUnavailable")
								: undefined
					}
				>
					<TextArea
						data-testid="edit-text"
						rows={3}
						disabled={text.kind !== "ready"}
						value={text.kind === "ready" ? text.value : selection.text}
						aria-invalid={tooLong || undefined}
						onChange={(e) => changeText(e.target.value)}
						className="resize-y font-sans"
					/>
				</Field>
			)}
			{colorField("color", t("artifacts.edit.color"))}
			{colorField("background-color", t("artifacts.edit.background"))}
			<div className="grid grid-cols-2 gap-3">
				{pxField("font-size", t("artifacts.edit.fontSize"))}
				<Field label={t("artifacts.edit.fontWeight")}>
					<Select
						data-testid="edit-font-weight"
						value={raw["font-weight"]}
						onChange={(e) => change("font-weight", e.target.value)}
					>
						{raw["font-weight"] === "" && <option value="">—</option>}
						{WEIGHTS.map((w) => (
							<option key={w} value={w}>
								{w}
							</option>
						))}
					</Select>
				</Field>
			</div>
			<fieldset className="flex min-w-0 flex-col gap-1 text-sm">
				<legend className="mb-1 font-medium text-muted text-xs">
					{t("artifacts.edit.align")}
				</legend>
				<div className="flex gap-0.5 self-start rounded-full border border-border bg-inset p-0.5">
					{ALIGNS.map((a) => {
						const on = raw["text-align"] === a;
						return (
							<button
								key={a}
								type="button"
								aria-pressed={on}
								data-testid={`edit-align-${a}`}
								className={`${tabClass(on)} ${on ? "bg-panel shadow-xs" : ""}`}
								onClick={() => change("text-align", a)}
							>
								{t(`artifacts.edit.align_${a}`)}
							</button>
						);
					})}
				</div>
			</fieldset>
			<Field
				label={t("artifacts.edit.padding")}
				hint={t("artifacts.edit.paddingHint")}
			>
				<TextInput
					data-testid="edit-padding"
					value={raw.padding}
					spellCheck={false}
					placeholder="8px 16px"
					aria-invalid={invalid("padding") || undefined}
					onChange={(e) => change("padding", e.target.value)}
					className="font-mono"
				/>
			</Field>
			{pxField("border-radius", t("artifacts.edit.radius"))}
		</div>
	);
}
