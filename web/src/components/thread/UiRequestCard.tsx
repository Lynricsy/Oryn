/**
 * 停靠在输入区上方的 extension UI 对话框（select / confirm / input / editor / ask / askDialog / form）。
 * 多端镜像：每个订阅者都显示同一请求；任一端作答后控制面广播 `ui.resolved`，其余端自动关闭（先答者生效）。
 * `ask` 超时：回 `{cancelled, timedOut}`，由 omp 取 recommended。
 */
import { type ReactNode, useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { keyed } from "../../lib/keys";
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
import { Button, TextArea, TextInput } from "../ui/primitives";

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
	return (
		<div className="flex flex-col gap-1.5">
			{req.options.map((opt, i) => (
				<button
					key={opt}
					type="button"
					className="rounded-md border border-border px-3 py-2 text-left text-sm hover:border-accent hover:bg-hover"
					onClick={() =>
						answer({ type: "extension_ui_response", id: req.id, value: opt })
					}
				>
					<div>{opt}</div>
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

function ConfirmBody({
	req,
	answer,
}: {
	req: ExtensionUiRequestOf<"confirm">;
	answer: Answer;
}) {
	const { t } = useTranslation();
	return (
		<div className="flex flex-col gap-3">
			<p className="whitespace-pre-wrap text-sm">{req.message}</p>
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
					variant="primary"
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
				variant="primary"
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
					variant="primary"
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
				<div className="text-faint text-xs uppercase tracking-wide">
					{q.header}
				</div>
			)}
			<legend className="mb-1 font-medium text-sm">{q.question}</legend>
			{q.options.map((opt, i) => {
				const checked = value.selected.includes(opt.label);
				return (
					<label
						key={opt.label}
						className={`flex cursor-pointer items-start gap-2 rounded-md border px-3 py-2 text-sm ${checked ? "border-accent bg-hover" : "border-border"}`}
					>
						<input
							type={q.multi ? "checkbox" : "radio"}
							name={q.id}
							className="mt-1"
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
								<span className="ml-2 rounded bg-accent/15 px-1 text-[10px] text-accent">
									{t("ui.recommended")}
								</span>
							)}
							{opt.description && (
								<span className="mt-0.5 block text-muted text-xs">
									{opt.description}
								</span>
							)}
							{opt.preview && (
								<pre className="mt-1 max-h-32 overflow-auto rounded bg-inset p-2 font-mono text-[11px]">
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
				<Button variant="primary" disabled={!complete} onClick={submit}>
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
				return (
					<label key={o.value} className="flex items-center gap-2">
						<input
							type={field.multi ? "checkbox" : "radio"}
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
										className="overflow-auto rounded bg-inset p-2 font-mono text-xs"
									>
										{b.text}
									</pre>
								);
							case "table":
								return (
									<table key={key} className="text-xs">
										<tbody>
											{keyed(b.rows, "r").map(({ key: rk, item: row }, ri) => (
												<tr
													key={rk}
													className={ri < b.headerRows ? "font-medium" : ""}
												>
													{keyed(row, "c").map(({ key: ck, item: cell }) => (
														<td
															key={ck}
															className="border border-border px-2 py-1"
														>
															<Spans spans={cell} />
														</td>
													))}
												</tr>
											))}
										</tbody>
									</table>
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
				<Button type="submit" variant="primary">
					{t("common.submit")}
				</Button>
			</div>
		</form>
	);
}

function titleOf(
	req: ExtensionUiRequest,
	t: (k: "ui.askTitle" | "ui.formTitle") => string,
): string {
	if ("title" in req && typeof req.title === "string") return req.title;
	if (req.method === "form") return t("ui.formTitle");
	return t("ui.askTitle");
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
	let body: ReactNode = null;
	switch (req.method) {
		case "select":
			body = <SelectBody req={req} answer={answer} />;
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
	return (
		<div
			role="dialog"
			aria-label={titleOf(req, t)}
			className="mx-auto mb-2 max-h-[55dvh] w-full max-w-3xl overflow-y-auto rounded-xl border border-accent/50 bg-panel p-3 shadow-lg"
		>
			<div className="mb-2 flex items-center gap-2">
				<h3 className="min-w-0 flex-1 font-medium text-sm">
					{titleOf(req, t)}
				</h3>
				{queued > 0 && (
					<span className="text-faint text-xs">
						{t("ui.moreQueued", { count: queued })}
					</span>
				)}
				{left !== null && (
					<span className="font-mono text-faint text-xs">
						{t("ui.timeLeft", { s: left })}
					</span>
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
			{body}
		</div>
	);
}
