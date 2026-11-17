/**
 * 停靠在输入坞托盘里的 extension UI 对话框（select / confirm / input / editor / ask / askDialog / form）。
 * 多端镜像：每个订阅者都显示同一请求；任一端作答后控制面广播 `ui.resolved`，其余端自动关闭（先答者生效）。
 * `ask` 超时：回 `{cancelled, timedOut}`，由 omp 取 recommended。
 */
import type { ParseKeys } from "i18next";
import { type ReactNode, useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import i18n from "../../i18n";
import { coreLabelKey, OMFG_SAVE_TITLE } from "../../lib/core-labels";
import {
	DESKTOP_ASK_DONE,
	DESKTOP_ASK_SKIP,
	parseDesktopAsk,
} from "../../lib/desktop-ask";
import { keyed } from "../../lib/keys";
import { splitConfirmRule } from "../../lib/omfg-rule";
import type {
	AskQuestion,
	ExtensionUiRequest,
	ExtensionUiRequestOf,
	ExtensionUiResponse,
	FormField,
	FormSection,
	FormSpan,
} from "../../omp/types";
import { answerUi } from "../../state/actions";
import { uiStore } from "../../state/ui";
import { Markdown } from "../markdown/Markdown";
import { escapeAngles, noticeBodyFormat } from "../transcript/NoticeLine";
import { Badge } from "../ui/data";
import { Icon } from "../ui/icons";
import { Button, Checkbox, Radio, TextArea, TextInput } from "../ui/primitives";
import { RuleCard } from "./RuleCard";

/** 选项行：细描边半透明底，选中时强调色描边 + 浅强调底。 */
const OPTION =
	"rounded-lg border px-3 py-2 text-left text-sm transition-colors duration-150";
const OPTION_IDLE = "border-border bg-panel/60 hover:bg-hover";
const OPTION_ON = "border-accent bg-accent-soft";

type Answer = (response: ExtensionUiResponse) => void;

function useCountdown(
	timeoutMs: number | undefined,
	onExpire: () => void,
): number | null {
	const [left, setLeft] = useState<number | null>(
		timeoutMs ? Math.ceil(timeoutMs / 1000) : null,
	);
	const expire = useRef(onExpire);
	expire.current = onExpire;
	useEffect(() => {
		if (!timeoutMs) return;
		const deadline = Date.now() + timeoutMs;
		const timer = setInterval(() => {
			const s = Math.max(0, Math.ceil((deadline - Date.now()) / 1000));
			setLeft(s);
			if (s === 0) {
				clearInterval(timer);
				expire.current();
			}
		}, 500);
		return () => clearInterval(timer);
	}, [timeoutMs]);
	return left;
}

function Spans({ spans }: { spans: readonly FormSpan[] }) {
	return (
		<>
			{spans.map((s, i) => {
				const key = `${i}-${s.text.slice(0, 8)}`;
				if (s.style === "code")
					return (
						<code key={key} className="md-inline-code">
							{s.text}
						</code>
					);
				if (s.style === "strong") return <strong key={key}>{s.text}</strong>;
				if (s.style === "em") return <em key={key}>{s.text}</em>;
				return (
					<span key={key} className="whitespace-pre-wrap">
						{s.text}
					</span>
				);
			})}
		</>
	);
}

function SelectBody({
	req,
	answer,
}: {
	req: ExtensionUiRequestOf<"select">;
	answer: Answer;
}) {
	const { t } = useTranslation();
	// /omfg 的保存位置：Core 的固定英文选项显示为中文，回传的 value 仍是原文
	const labelOf = (opt: string) => {
		const key = req.title === OMFG_SAVE_TITLE ? coreLabelKey(opt) : undefined;
		return key ? t(key) : opt;
	};
	return (
		<div className="flex flex-col gap-1.5">
			{req.options.map((opt, i) => (
				<button
					key={opt}
					type="button"
					className={`${OPTION} ${OPTION_IDLE}`}
					onClick={() =>
						answer({ type: "extension_ui_response", id: req.id, value: opt })
					}
				>
					<div>{labelOf(opt)}</div>
					{req.optionDetails?.[i]?.description && (
						<div className="mt-0.5 text-muted text-xs">
							{req.optionDetails[i]?.description}
						</div>
					)}
				</button>
			))}
		</div>
	);
}

/** agent 的 `desktop_ask_user`：原因、提示与「完成 / 跳过 / 打开桌面」（作答值仍是原文选项）。 */
function DesktopAskBody({
	req,
	reason,
	answer,
}: {
	req: ExtensionUiRequestOf<"select">;
	reason: string;
	answer: Answer;
}) {
	const { t } = useTranslation();
	const reply = (value: string) =>
		answer({ type: "extension_ui_response", id: req.id, value });
	const openDesktop = () => {
		const ui = uiStore.getState();
		ui.setPanelTab("desktop");
		ui.setPanel(true);
	};
	return (
		<div data-testid="desktop-ask-card" className="flex flex-col gap-2">
			<p className="whitespace-pre-wrap break-words text-sm">{reason}</p>
			<p className="text-muted text-xs">{t("deskPanel.control.askHint")}</p>
			<div className="flex flex-wrap gap-1.5">
				<Button
					size="sm"
					variant="primary"
					icon="check"
					onClick={() => reply(DESKTOP_ASK_DONE)}
				>
					{t("deskPanel.control.done")}
				</Button>
				<Button size="sm" onClick={() => reply(DESKTOP_ASK_SKIP)}>
					{t("deskPanel.control.skip")}
				</Button>
				<Button size="sm" variant="ghost" icon="monitor" onClick={openDesktop}>
					{t("deskPanel.control.openDesktop")}
				</Button>
			</div>
		</div>
	);
}

function ConfirmBody({
	req,
	answer,
}: {
	req: ExtensionUiRequestOf<"confirm">;
	answer: Answer;
}) {
	const { t } = useTranslation();
	// /omfg 的 confirm 在 message 尾部拼着候选规则全文：拆出来用规则卡展示
	const ruled = splitConfirmRule(req.message);
	return (
		<div className="flex flex-col gap-3">
			<p className="whitespace-pre-wrap text-sm">
				{ruled ? ruled.message : req.message}
			</p>
			{ruled && (
				<div className="max-h-48 overflow-y-auto rounded-md border border-border bg-panel/60 px-3 py-2.5">
					<RuleCard rule={ruled.rule} />
				</div>
			)}
			<div className="flex justify-end gap-2">
				<Button
					onClick={() =>
						answer({
							type: "extension_ui_response",
							id: req.id,
							confirmed: false,
						})
					}
				>
					{t("common.no")}
				</Button>
				<Button
					variant="accent"
					onClick={() =>
						answer({
							type: "extension_ui_response",
							id: req.id,
							confirmed: true,
						})
					}
				>
					{t("common.yes")}
				</Button>
			</div>
		</div>
	);
}

function InputBody({
	req,
	answer,
}: {
	req: ExtensionUiRequestOf<"input">;
	answer: Answer;
}) {
	const { t } = useTranslation();
	const [value, setValue] = useState("");
	const submit = () => {
		if (!value && !req.allowEmpty) return;
		answer({ type: "extension_ui_response", id: req.id, value });
	};
	return (
		<form
			className="flex gap-2"
			onSubmit={(e) => {
				e.preventDefault();
				submit();
			}}
		>
			<TextInput
				autoFocus
				type={req.secret ? "password" : "text"}
				placeholder={req.placeholder}
				value={value}
				onChange={(e) => setValue(e.target.value)}
			/>
			<Button
				type="submit"
				variant="accent"
				disabled={!value && !req.allowEmpty}
			>
				{t("common.submit")}
			</Button>
		</form>
	);
}

function EditorBody({
	req,
	answer,
}: {
	req: ExtensionUiRequestOf<"editor">;
	answer: Answer;
}) {
	const { t } = useTranslation();
	const [value, setValue] = useState(req.prefill ?? "");
	return (
		<div className="flex flex-col gap-2">
			<TextArea
				rows={8}
				autoFocus
				value={value}
				onChange={(e) => setValue(e.target.value)}
			/>
			<div className="flex justify-end">
				<Button
					variant="accent"
					onClick={() =>
						answer({ type: "extension_ui_response", id: req.id, value })
					}
				>
					{t("common.submit")}
				</Button>
			</div>
		</div>
	);
}

interface QuestionAnswer {
	selected: string[];
	custom: string;
}

function QuestionBlock({
	q,
	value,
	onChange,
}: {
	q: AskQuestion;
	value: QuestionAnswer;
	onChange: (v: QuestionAnswer) => void;
}) {
	const { t } = useTranslation();
	return (
		<fieldset className="flex flex-col gap-1.5">
			{q.header && (
				<div className="font-medium text-2xs text-faint">{q.header}</div>
			)}
			<legend className="mb-1 font-medium text-sm">{q.question}</legend>
			{q.options.map((opt, i) => {
				const checked = value.selected.includes(opt.label);
				const Control = q.multi ? Checkbox : Radio;
				return (
					// biome-ignore lint/a11y/noLabelWithoutControl: 控件为 Checkbox / Radio 组件（内含原生 input）
					<label
						key={opt.label}
						className={`flex cursor-pointer items-start gap-2.5 ${OPTION} ${checked ? OPTION_ON : OPTION_IDLE}`}
					>
						<Control
							name={q.id}
							className="mt-0.5"
							checked={checked}
							onChange={() =>
								onChange({
									...value,
									selected: q.multi
										? checked
											? value.selected.filter((l) => l !== opt.label)
											: [...value.selected, opt.label]
										: [opt.label],
								})
							}
						/>
						<span className="min-w-0 flex-1">
							<span>{opt.label}</span>
							{q.recommended === i && (
								<span className="ml-2 inline-flex align-middle">
									<Badge tone="accent">{t("ui.recommended")}</Badge>
								</span>
							)}
							{opt.description && (
								<span className="mt-0.5 block text-muted text-xs">
									{opt.description}
								</span>
							)}
							{opt.preview && (
								<pre className="mt-1.5 max-h-32 overflow-auto rounded-md bg-inset p-2 font-mono text-2xs">
									{opt.preview}
								</pre>
							)}
						</span>
					</label>
				);
			})}
			<TextInput
				placeholder={t("ui.otherAnswer")}
				value={value.custom}
				onChange={(e) => onChange({ ...value, custom: e.target.value })}
			/>
		</fieldset>
	);
}

function AskBody({
	req,
	answer,
}: {
	req: ExtensionUiRequestOf<"ask"> | ExtensionUiRequestOf<"askDialog">;
	answer: Answer;
}) {
	const { t } = useTranslation();
	const questions = req.questions as AskQuestion[];
	const [answers, setAnswers] = useState<Record<string, QuestionAnswer>>(() =>
		Object.fromEntries(
			questions.map((q) => [q.id, { selected: [], custom: "" }]),
		),
	);
	const complete = questions.every((q) => {
		const a = answers[q.id];
		return a && (a.selected.length > 0 || a.custom.trim() !== "");
	});
	const submit = () => {
		if (req.method === "ask") {
			answer({
				type: "extension_ui_response",
				id: req.id,
				answers: questions.map((q) => {
					const a = answers[q.id] ?? { selected: [], custom: "" };
					return a.custom.trim()
						? {
								id: q.id,
								selectedOptions: a.selected,
								customInput: a.custom.trim(),
							}
						: { id: q.id, selectedOptions: a.selected };
				}),
			});
			return;
		}
		answer({
			type: "extension_ui_response",
			id: req.id,
			askDialog: {
				kind: "submit",
				results: questions.map((q) => {
					const a = answers[q.id] ?? { selected: [], custom: "" };
					return {
						id: q.id,
						question: q.question,
						options: q.options.map((o) => o.label),
						multi: q.multi === true,
						selectedOptions: a.selected,
						...(a.custom.trim() ? { customInput: a.custom.trim() } : {}),
					};
				}),
			},
		});
	};
	return (
		<div className="flex flex-col gap-4">
			{questions.map((q) => (
				<QuestionBlock
					key={q.id}
					q={q}
					value={answers[q.id] ?? { selected: [], custom: "" }}
					onChange={(v) => setAnswers((prev) => ({ ...prev, [q.id]: v }))}
				/>
			))}
			<div className="flex justify-end">
				<Button variant="accent" disabled={!complete} onClick={submit}>
					{t("common.submit")}
				</Button>
			</div>
		</div>
	);
}

function FieldInput({
	field,
	value,
	onChange,
}: {
	field: FormField;
	value: string | string[] | undefined;
	onChange: (v: string | string[]) => void;
}) {
	const { t } = useTranslation();
	if (field.kind === "text") {
		const v = typeof value === "string" ? value : field.value;
		return (
			// biome-ignore lint/a11y/noLabelWithoutControl: 控件为 TextInput / TextArea 组件
			<label className="flex flex-col gap-1 text-sm">
				<span>
					<Spans spans={field.label} />
				</span>
				{field.multiline ? (
					<TextArea
						rows={field.rows || 3}
						value={v}
						placeholder={field.placeholder}
						onChange={(e) => onChange(e.target.value)}
					/>
				) : (
					<TextInput
						type={field.type || "text"}
						value={v}
						placeholder={field.placeholder}
						onChange={(e) => onChange(e.target.value)}
					/>
				)}
			</label>
		);
	}
	const selected = Array.isArray(value)
		? value
		: typeof value === "string"
			? [value]
			: field.options.filter((o) => o.checked).map((o) => o.value);
	const known = new Set(field.options.map((o) => o.value));
	const other = selected.find((v) => !known.has(v)) ?? "";
	const emit = (vals: string[]) =>
		onChange(field.multi ? vals : (vals[0] ?? ""));
	return (
		<fieldset className="flex flex-col gap-1 text-sm">
			{field.label.length > 0 && (
				<legend className="mb-1">
					<Spans spans={field.label} />
				</legend>
			)}
			{field.options.map((o) => {
				const checked = selected.includes(o.value);
				const Control = field.multi ? Checkbox : Radio;
				return (
					// biome-ignore lint/a11y/noLabelWithoutControl: 控件为 Checkbox / Radio 组件（内含原生 input）
					<label key={o.value} className="flex items-center gap-2">
						<Control
							name={field.name}
							checked={checked}
							disabled={o.disabled}
							onChange={() =>
								emit(
									field.multi
										? checked
											? selected.filter((v) => v !== o.value)
											: [...selected, o.value]
										: [o.value],
								)
							}
						/>
						<Spans spans={o.label} />
					</label>
				);
			})}
			{field.other && (
				<TextInput
					placeholder={t("ui.otherAnswer")}
					value={other}
					onChange={(e) => {
						const rest = selected.filter((v) => known.has(v));
						emit(
							e.target.value
								? [...(field.multi ? rest : []), e.target.value]
								: rest,
						);
					}}
				/>
			)}
		</fieldset>
	);
}

function FormBody({
	req,
	answer,
}: {
	req: ExtensionUiRequestOf<"form">;
	answer: Answer;
}) {
	const { t } = useTranslation();
	const [data, setData] = useState<Record<string, string | string[]>>({});
	const sections = req.sections as FormSection[];
	return (
		<form
			className="flex flex-col gap-4"
			onSubmit={(e) => {
				e.preventDefault();
				const formData: Record<string, unknown> = {};
				for (const sec of sections) {
					for (const b of sec.blocks) {
						if (b.kind !== "field") continue;
						const f = b.field;
						formData[f.name] =
							data[f.name] ??
							(f.kind === "text"
								? f.value
								: f.multi
									? f.options.filter((o) => o.checked).map((o) => o.value)
									: (f.options.find((o) => o.checked)?.value ?? ""));
					}
				}
				answer({ type: "extension_ui_response", id: req.id, formData });
			}}
		>
			{sections.map((sec, si) => (
				// biome-ignore lint/suspicious/noArrayIndexKey: 表单分节无 id
				<section key={si} className="flex flex-col gap-2">
					{sec.title.length > 0 && (
						<h3 className="font-medium text-sm">
							<Spans spans={sec.title} />
						</h3>
					)}
					{sec.blocks.map((b, bi) => {
						const key = `${si}-${bi}`;
						switch (b.kind) {
							case "text":
								return (
									<p
										key={key}
										className={
											b.heading ? "font-medium text-sm" : "text-muted text-sm"
										}
									>
										<Spans spans={b.spans} />
									</p>
								);
							case "list":
								return (
									<ul
										key={key}
										className={`${b.ordered ? "list-decimal" : "list-disc"} pl-5 text-sm`}
									>
										{b.items.map((it, ii) => (
											// biome-ignore lint/suspicious/noArrayIndexKey: 列表项无 id
											<li key={ii}>
												<Spans spans={it} />
											</li>
										))}
									</ul>
								);
							case "pre":
								return (
									<pre
										key={key}
										className="overflow-auto rounded-md bg-inset p-2 font-mono text-xs"
									>
										{b.text}
									</pre>
								);
							case "table":
								return (
									<div
										key={key}
										className="overflow-x-auto rounded-lg border border-border"
									>
										<table className="text-xs">
											<tbody>
												{keyed(b.rows, "r").map(
													({ key: rk, item: row }, ri) => (
														<tr
															key={rk}
															className={`${ri < b.headerRows ? "bg-raised font-medium" : ""} ${ri > 0 ? "border-border border-t" : ""}`}
														>
															{keyed(row, "c").map(
																({ key: ck, item: cell }) => (
																	<td key={ck} className="px-2.5 py-1.5">
																		<Spans spans={cell} />
																	</td>
																),
															)}
														</tr>
													),
												)}
											</tbody>
										</table>
									</div>
								);
							case "field":
								return (
									<FieldInput
										key={key}
										field={b.field}
										value={data[b.field.name]}
										onChange={(v) =>
											setData((prev) => ({ ...prev, [b.field.name]: v }))
										}
									/>
								);
							default:
								return null;
						}
					})}
				</section>
			))}
			<div className="flex justify-end">
				<Button type="submit" variant="accent">
					{t("common.submit")}
				</Button>
			</div>
		</form>
	);
}

function titleOf(req: ExtensionUiRequest, t: (k: ParseKeys) => string): string {
	if ("title" in req && typeof req.title === "string") {
		const key = coreLabelKey(req.title);
		return key ? t(key) : req.title;
	}
	if (req.method === "form") return t("ui.formTitle");
	return t("ui.askTitle");
}

/**
 * 控制面发起的确认卡（M19：助手的删除与对外公开动作）的标题：帧带 `oryn: {action, targetLabel}` 时用语言包
 * `assistant.confirm.<action>`；没有对应文案时返回 null，回落到帧的英文 `title`。
 */
function orynConfirmTitle(req: ExtensionUiRequest): string | null {
	if (req.method !== "confirm" || !("oryn" in req)) return null;
	const oryn = req.oryn;
	if (typeof oryn !== "object" || oryn === null) return null;
	if (!("action" in oryn) || !("targetLabel" in oryn)) return null;
	const { action, targetLabel } = oryn;
	if (typeof action !== "string" || typeof targetLabel !== "string")
		return null;
	const key = `assistant.confirm.${action}`;
	return i18n.exists(key)
		? i18n.t(key as "assistant.confirm.delete_thread", { targetLabel })
		: null;
}

/** Core `approvalHeader`（tools/approval.ts）在工具名之后的固定行。 */
const APPROVAL_ROW_RE = /^(Origin|Reason): (.+)$/;

/**
 * 多行标题首行以外的部分。工具审批（首行 `Allow tool: <名>`）的 `Origin:` / `Reason:` 是固定行，拆成键值；
 * 之后是各工具自己拼的参数（命令、路径、JSON），等宽原样。其余对话框的多行标题按命令输出同一套判定。
 */
function TitleDetail({ head, text }: { head: string; text: string }) {
	const { t } = useTranslation();
	const pre = (body: string) => (
		<pre className="m-0 max-h-48 overflow-auto whitespace-pre-wrap rounded-md bg-inset px-2.5 py-2 font-mono text-2xs text-fg leading-relaxed [overflow-wrap:anywhere]">
			{body}
		</pre>
	);
	if (head.startsWith("Allow tool: ")) {
		const lines = text.split("\n");
		const rows: { key: string; value: string }[] = [];
		while (rows.length < lines.length) {
			const m = APPROVAL_ROW_RE.exec(lines[rows.length] ?? "");
			if (!m) break;
			rows.push({ key: m[1] ?? "", value: m[2] ?? "" });
		}
		const rest = lines.slice(rows.length).join("\n").trim();
		return (
			<div className="mb-2.5 flex flex-col gap-2">
				{rows.length > 0 && (
					<dl className="grid grid-cols-[max-content_minmax(0,1fr)] gap-x-3 gap-y-1 text-xs">
						{rows.map((r) => (
							<div key={r.key} className="contents">
								<dt className="text-faint">
									{r.key === "Origin"
										? t("ui.approvalOrigin")
										: t("ui.approvalReason")}
								</dt>
								<dd className="m-0 min-w-0 whitespace-pre-wrap text-fg">
									{r.value}
								</dd>
							</div>
						))}
					</dl>
				)}
				{rest && pre(rest)}
			</div>
		);
	}
	return (
		<div className="mb-2.5">
			{noticeBodyFormat(text) === "markdown" ? (
				<div className="text-fg [&_.md]:text-sm! [&_.md]:leading-relaxed!">
					<Markdown text={escapeAngles(text)} />
				</div>
			) : (
				pre(text)
			)}
		</div>
	);
}

export function UiRequestCard({
	threadId,
	req,
	queued,
}: {
	threadId: string;
	req: ExtensionUiRequest;
	queued: number;
}): ReactNode {
	const { t } = useTranslation();
	const answer: Answer = (response) => answerUi(threadId, response);
	const timeout =
		"timeout" in req && typeof req.timeout === "number"
			? req.timeout
			: undefined;
	const left = useCountdown(timeout, () =>
		answer({
			type: "extension_ui_response",
			id: req.id,
			cancelled: true,
			timedOut: true,
		}),
	);
	const desk =
		req.method === "select" ? parseDesktopAsk(req.title, req.options) : null;
	let body: ReactNode = null;
	switch (req.method) {
		case "select":
			body = desk ? (
				<DesktopAskBody req={req} reason={desk.reason} answer={answer} />
			) : (
				<SelectBody req={req} answer={answer} />
			);
			break;
		case "confirm":
			body = <ConfirmBody req={req} answer={answer} />;
			break;
		case "input":
			body = <InputBody req={req} answer={answer} />;
			break;
		case "editor":
			body = <EditorBody req={req} answer={answer} />;
			break;
		case "ask":
		case "askDialog":
			body = <AskBody req={req} answer={answer} />;
			break;
		case "form":
			body = <FormBody req={req} answer={answer} />;
			break;
		default:
			return null;
	}
	// 标题可能是多行：Core 在 RPC 下没有审批对话框，工具审批走 select，整段提示（`Allow tool: …` + 参数）
	// 都在 title 里。标题栏只放首行，其余作正文，免得命令 / 路径被截掉、盲批。
	const [head = "", ...more] = (
		desk
			? t("deskPanel.control.askTitle", {
					kind: t(`deskPanel.control.kind.${desk.kind}`),
				})
			: (orynConfirmTitle(req) ?? titleOf(req, t))
	).split("\n");
	const detail = more
		.join("\n")
		.replace(/^(?:[ \t]*\n)+/, "")
		.trimEnd();
	return (
		<div
			role="dialog"
			aria-label={head}
			className="max-h-[55dvh] overflow-y-auto px-3.5 py-3"
		>
			<div className="mb-2.5 flex items-center gap-2">
				<span className="grid h-5 w-5 shrink-0 place-items-center rounded-md bg-accent-soft text-accent">
					<Icon name={desk ? "monitor" : "message"} size={12} weight="fill" />
				</span>
				<h3
					className="min-w-0 flex-1 truncate font-medium text-sm"
					title={head}
				>
					{head}
				</h3>
				{queued > 0 && (
					<span className="text-faint text-xs">
						{t("ui.moreQueued", { count: queued })}
					</span>
				)}
				{left !== null && (
					<Badge icon="timer" tone={left <= 10 ? "warn" : "neutral"}>
						{t("ui.timeLeft", { s: left })}
					</Badge>
				)}
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
					{t("common.dismiss")}
				</Button>
			</div>
			{detail && <TitleDetail head={head} text={detail} />}
			{body}
		</div>
	);
}
