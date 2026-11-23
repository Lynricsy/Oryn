/**
 * `ask` — an HTML form posed to the user mid-run, answered with its form data.
 * 输入段是「问卷」：逐题列出题干与选项（单选圆点、多选方框、推荐项标出）；输出段是「回执」：逐题给出用户的选择，
 * 选中的选项高亮、未选的淡出，附选项备注。等待作答时头部与输出段都给醒目的等待状态。
 * 结果形状见 oryn-core `tui/src/tools/ask.ts`（`AskToolDetails`：answers / notes / timedOut）。
 */
import { type ReactNode, useMemo } from "react";
import { useTranslation } from "react-i18next";
import { Icon } from "../../components/ui/icons";
import {
	ErrorBlock,
	InvalidArg,
	Note,
	Output,
	ResultImages,
	ResultText,
	useLiveNow,
} from "../parts";
import type { ToolRenderer, ToolRenderProps } from "../types";
import {
	detailsRecord,
	formatElapsed,
	isRecord,
	normalizeWs,
	resultTextOf,
	str,
	truncate,
} from "../util";

/** One named field's question and its answer text, as the TUI records them. */
interface AskAnswer {
	name: string;
	question: string;
	answer: string;
}

interface AskOptionNote {
	name: string;
	question: string;
	option: string;
	text: string;
}

/** 表单里的一个选项：完整标签（与 Core 的回答文本一致）、拆出的说明、是否预选（推荐）。 */
interface AskOption {
	label: string;
	main: string;
	hint: string | null;
	preset: boolean;
}

/** 表单里的一道题（一个具名控件组）。 */
interface AskQuestion {
	name: string;
	question: string;
	/** 题前的说明文字（不是题干的引言、段落）。 */
	lead: string | null;
	kind: "single" | "multi" | "text";
	options: AskOption[];
}

const ENTITIES: Record<string, string> = {
	amp: "&",
	lt: "<",
	gt: ">",
	quot: '"',
	apos: "'",
	nbsp: " ",
};

/** Text content of an HTML fragment: tags dropped, common entities decoded, whitespace folded. */
function htmlText(fragment: string): string {
	const decoded = fragment
		.replace(/<[^>]*>?/g, " ")
		.replace(/&(#x[0-9a-f]+|#\d+|[a-z]+);/gi, (entity, code: string) => {
			if (code[0] !== "#") return ENTITIES[code.toLowerCase()] ?? entity;
			const point =
				code[1] === "x" || code[1] === "X"
					? Number.parseInt(code.slice(2), 16)
					: Number(code.slice(1));
			return Number.isInteger(point) && point >= 0 && point <= 0x10ffff
				? String.fromCodePoint(point)
				: entity;
		});
	return normalizeWs(decoded);
}

/**
 * Coarse outline of a (possibly still streaming) form without a DOM: fieldset
 * legends, else the captions and labels before its first control, plus how
 * many distinct named controls it has. 没有 DOMParser 的宿主（及流式残缺的表单）用它兜底。
 */
function formOutline(html: string): { headings: string[]; fields: number } {
	const names = new Set<string>();
	for (const match of html.matchAll(/<(input|select|textarea)\b([^>]*)>?/gi)) {
		const attrs = match[2] ?? "";
		if (/\btype\s*=\s*["']?(hidden|submit|button|reset|image)\b/i.test(attrs))
			continue;
		const name = /\bname\s*=\s*(?:"([^"]*)"|'([^']*)'|([^\s>]+))/i.exec(attrs);
		const value = name?.[1] ?? name?.[2] ?? name?.[3];
		if (value) names.add(value);
	}
	const legends = [
		...html.matchAll(/<legend\b[^>]*>([\s\S]*?)(?:<\/legend>|$)/gi),
	]
		.map((match) => htmlText(match[1] ?? ""))
		.filter(Boolean);
	if (legends.length > 0) return { headings: legends, fields: names.size };
	const first =
		/<(?:p|label|h[1-6])\b[^>]*>([\s\S]*?)(?:<(?:input|select|textarea|\/p|\/label|\/h[1-6])\b|$)/i.exec(
			html,
		);
	const caption = first ? htmlText(first[1] ?? "") : "";
	return { headings: caption ? [caption] : [], fields: names.size };
}

/** Core 的旧版参数把选项说明接在标签后（`标签 — 说明`）：显示时拆开，回答文本仍按完整标签比对。 */
function splitHint(label: string): { main: string; hint: string | null } {
	const at = label.indexOf(" — ");
	if (at <= 0) return { main: label, hint: null };
	return { main: label.slice(0, at), hint: label.slice(at + 3) || null };
}

const SKIP_INPUT: Record<string, true> = {
	hidden: true,
	submit: true,
	button: true,
	reset: true,
	image: true,
};
/** 说明文字所在的块：同一块里的文字连成一段说明，换块就换一段。 */
const BLOCK_SELECTOR =
	"p, div, h1, h2, h3, h4, h5, h6, li, td, th, pre, blockquote, section, form, fieldset, label";

/** 控件所在 label 的文字（不含控件本身与嵌套选项）。 */
function labelText(el: Element): string {
	const label =
		el.closest("label") ??
		(el.id
			? el.ownerDocument.querySelector(`label[for="${CSS.escape(el.id)}"]`)
			: null);
	return label ? normalizeWs(label.textContent ?? "") : "";
}

/**
 * 用浏览器的 DOMParser 把表单拆成逐题结构（惰性文档，不执行脚本、不加载资源）。题干的取法同 Core 的
 * `fieldQuestions`：控件自己的 label，否则紧挨在前的说明文字，否则所在 fieldset 的 legend，否则唯一选项的标签，否则 name。
 * 宿主没有 DOMParser 时返回 null，由调用方退回提纲。
 */
function parseQuestions(html: string): AskQuestion[] | null {
	if (typeof DOMParser === "undefined" || typeof CSS === "undefined")
		return null;
	const doc = new DOMParser().parseFromString(html, "text/html");
	const byName = new Map<string, AskQuestion>();
	const order: AskQuestion[] = [];
	let caption = "";
	let captionBlock: Element | null = null;
	/** 题干之外的说明文字（表单引言、两题之间的说明），挂到其后的第一道题上显示。 */
	let lead: string[] = [];
	const walker = doc.createTreeWalker(doc.body, 0x1 | 0x4);
	for (let node = walker.nextNode(); node; node = walker.nextNode()) {
		if (node.nodeType === 3) {
			const parent = node.parentElement;
			if (!parent || parent.closest("option, legend, select, textarea"))
				continue;
			const label = parent.closest("label");
			if (label?.querySelector("input, select, textarea")) continue;
			const text = normalizeWs(node.textContent ?? "");
			if (!text) continue;
			const block = parent.closest(BLOCK_SELECTOR);
			if (caption && block === captionBlock) caption = `${caption} ${text}`;
			else {
				if (caption) lead.push(caption);
				caption = text;
				captionBlock = block;
			}
			continue;
		}
		const el = node as Element;
		const tag = el.tagName;
		if (tag !== "INPUT" && tag !== "SELECT" && tag !== "TEXTAREA") continue;
		const type = (el.getAttribute("type") ?? "text").toLowerCase();
		if (tag === "INPUT" && Object.hasOwn(SKIP_INPUT, type)) continue;
		const name = el.getAttribute("name");
		if (!name) continue;
		let q = byName.get(name);
		const choice = tag === "INPUT" && (type === "radio" || type === "checkbox");
		if (!q) {
			const own = choice ? "" : labelText(el);
			const fieldset = el.closest("fieldset");
			const legend = normalizeWs(
				fieldset?.querySelector("legend")?.textContent ?? "",
			);
			// 说明文字只认同一分节（fieldset）里的：表单开头的引言不是第一个 fieldset 里那道题的题干
			const sameSection =
				(captionBlock?.closest("fieldset") ?? null) === fieldset;
			const useCaption = !own && sameSection && caption !== "";
			if (caption && !useCaption) lead.push(caption);
			q = {
				name,
				question: own || (useCaption ? caption : "") || legend,
				lead: lead.length > 0 ? lead.join("\n") : null,
				kind:
					type === "checkbox" ||
					(tag === "SELECT" && el.hasAttribute("multiple"))
						? "multi"
						: choice || tag === "SELECT"
							? "single"
							: "text",
				options: [],
			};
			byName.set(name, q);
			order.push(q);
		}
		if (choice) {
			const label = labelText(el) || el.getAttribute("value") || "";
			if (label)
				q.options.push({
					label,
					...splitHint(label),
					preset: el.hasAttribute("checked"),
				});
		} else if (tag === "SELECT") {
			for (const opt of el.querySelectorAll("option")) {
				const label = normalizeWs(opt.textContent ?? "");
				if (label)
					q.options.push({
						label,
						...splitHint(label),
						preset: opt.hasAttribute("selected"),
					});
			}
		}
		caption = "";
		captionBlock = null;
		lead = [];
	}
	for (const q of order) {
		if (!q.question)
			q.question =
				q.options.length === 1 ? (q.options[0]?.main ?? q.name) : q.name;
	}
	return order;
}

function escapeHtml(text: string): string {
	return text
		.replace(/&/g, "&amp;")
		.replace(/</g, "&lt;")
		.replace(/>/g, "&gt;")
		.replace(/"/g, "&quot;")
		.replace(/'/g, "&#039;");
}

/**
 * 旧版 `questions` 参数（`[{id, question, options: [{label, description?}], multi?, recommended?}]`）
 * 重建成表单，同 Core `recoverAskHtml`：Core 19 起 ask 宽松校验，模型发旧参数照样会问。
 * 选项说明的拼法也与 Core 一致（回答文本按它比对），显示时由 `splitHint` 拆开。
 */
function legacyQuestionsHtml(questions: unknown): string | null {
	if (!Array.isArray(questions) || questions.length === 0) return null;
	const fieldsets: string[] = [];
	for (const [index, item] of questions.entries()) {
		if (!isRecord(item) || typeof item.question !== "string") return null;
		const name = escapeHtml(
			typeof item.id === "string" && item.id ? item.id : `q${index + 1}`,
		);
		const kind = item.multi === true ? "checkbox" : "radio";
		const options = Array.isArray(item.options) ? item.options : [];
		const inputs: string[] = [];
		for (const [optionIndex, option] of options.entries()) {
			const label = isRecord(option) ? option.label : option;
			if (typeof label !== "string") return null;
			const description =
				isRecord(option) && typeof option.description === "string"
					? ` — ${escapeHtml(option.description)}`
					: "";
			const checked = item.recommended === optionIndex ? " checked" : "";
			inputs.push(
				`<label><input type="${kind}" name="${name}" value="${escapeHtml(label)}"${checked}> ${escapeHtml(label)}${description}</label>`,
			);
		}
		const control =
			inputs.length > 0
				? inputs.join("")
				: `<textarea name="${name}"></textarea>`;
		fieldsets.push(
			`<fieldset><legend>${escapeHtml(item.question)}</legend>${control}</fieldset>`,
		);
	}
	return `<form>${fieldsets.join("")}</form>`;
}

/** Answers from `result.details`; null when the result carries none (error, cancel, older sessions). */
function answersOf(
	details: Record<string, unknown> | null,
): AskAnswer[] | null {
	if (!details || !Array.isArray(details.answers)) return null;
	const out: AskAnswer[] = [];
	for (const entry of details.answers) {
		if (!isRecord(entry)) continue;
		const name = str(entry.name);
		const question = str(entry.question);
		const answer = str(entry.answer);
		if (question !== null && answer !== null)
			out.push({ name: name ?? "", question, answer });
	}
	return out;
}

/** Valid selected-option notes from persisted details; ignore malformed entries. */
function notesOf(details: Record<string, unknown> | null): AskOptionNote[] {
	if (!details || !Array.isArray(details.notes)) return [];
	const out: AskOptionNote[] = [];
	for (const entry of details.notes) {
		if (!isRecord(entry)) continue;
		const name = str(entry.name);
		const question = str(entry.question);
		const option = str(entry.option);
		const text = str(entry.text);
		if (
			name !== null &&
			question !== null &&
			option !== null &&
			text !== null
		) {
			out.push({ name, question, option, text });
		}
	}
	return out;
}

/** 参数里的表单 HTML（新版 `html`，或旧版 `questions` 重建）；都没有为 null。 */
function formHtml(args: Record<string, unknown>): string | null {
	return str(args.html) ?? legacyQuestionsHtml(args.questions);
}

function useQuestions(html: string | null): AskQuestion[] | null {
	return useMemo(() => (html ? parseQuestions(html) : null), [html]);
}

/**
 * 回答文本里选中了哪些选项：Core 把多选的标签用 `, ` 连起来，「其他」写成 `Other: …`。
 * 按完整标签逐个摘出，剩下的部分是自由填写。
 */
function pickOptions(
	answer: string,
	options: readonly AskOption[],
): { picked: Set<string>; rest: string } {
	const picked = new Set<string>();
	let rest = `, ${answer}, `;
	// 长标签先比对：免得「保留」吃掉「保留，默认关闭」的前缀
	for (const opt of [...options].sort(
		(a, b) => b.label.length - a.label.length,
	)) {
		const needle = `, ${opt.label}, `;
		const at = rest.indexOf(needle);
		if (at < 0) continue;
		picked.add(opt.label);
		rest = `${rest.slice(0, at)}, ${rest.slice(at + needle.length)}`;
	}
	return { picked, rest: rest.replace(/^(?:,\s*)+|(?:,\s*)+$/g, "").trim() };
}

function Summary({ args, result }: ToolRenderProps): ReactNode {
	const { t } = useTranslation();
	const html = formHtml(args);
	const questions = useQuestions(html);
	const answers = answersOf(detailsRecord(result));
	const outline = html ? formOutline(html) : { headings: [], fields: 0 };
	const first =
		answers?.[0]?.question ?? questions?.[0]?.question ?? outline.headings[0];
	if (!first)
		return html === null && !result ? <InvalidArg what="html" /> : null;
	const count = answers?.length || questions?.length || outline.fields;
	return (
		<>
			<span>{truncate(normalizeWs(first), 70)}</span>
			{count > 1 && (
				<span className="tv-faint">
					{t("toolRender.ask.questions", { count })}
				</span>
			)}
		</>
	);
}

/** 右侧读数：等待作答（强调色、呼吸点）/ 已作答 n 题 / 超时 / 取消。 */
function Meta(props: ToolRenderProps): ReactNode {
	const { t } = useTranslation();
	const { result } = props;
	if (props.running === true && !result)
		return (
			<span className="tv-ask-wait">
				<span className="tv-ask-wait-dot" aria-hidden="true" />
				{t("toolRender.ask.waiting")}
			</span>
		);
	if (!result) return null;
	const details = detailsRecord(result);
	const answers = answersOf(details);
	if (result.isError || answers === null)
		return result.isError ? (
			<span className="tv-meta-err">{t("toolRender.ask.cancelled")}</span>
		) : null;
	if (details?.timedOut === true)
		return (
			<span className="tv-meta-warn">
				{answers.length === 0
					? t("toolRender.ask.timedOutEmpty")
					: t("toolRender.ask.timedOut")}
			</span>
		);
	const given = answers.filter((a) => a.answer !== "").length;
	return (
		<span className={given > 0 ? "tv-meta-ok" : undefined}>
			{t("toolRender.ask.answered", { given, total: answers.length })}
		</span>
	);
}

/** 选项前的小记号：单选圆、多选方，选中时填实。 */
function Mark({
	kind,
	on,
}: {
	kind: AskQuestion["kind"];
	on: boolean;
}): ReactNode {
	return (
		<span
			className={`tv-ask-mark tv-ask-mark--${kind === "multi" ? "box" : "dot"}${on ? " tv-ask-mark--on" : ""}`}
			aria-hidden="true"
		>
			{on && kind === "multi" && <Icon name="check" size={9} />}
		</span>
	);
}

/** 输入：问卷。逐题编号，题干 + 选项（推荐项标出）或一条空白作答线；解析不出结构时给提纲与 HTML 原文。 */
function Input({ args, result }: ToolRenderProps): ReactNode {
	const { t } = useTranslation();
	const html = formHtml(args);
	const questions = useQuestions(html);
	if (html === null) return result ? null : <InvalidArg what="html" />;
	if (!questions || questions.length === 0) {
		const { headings } = formOutline(html);
		return (
			<>
				{headings.length > 0 && (
					<div className="tv-prose">{headings.join("\n")}</div>
				)}
				{html.trim() !== "" && (
					<Output text={html} lang="xml" variant="code" maxLines={8} />
				)}
			</>
		);
	}
	return (
		<ol className="tv-ask-form">
			{questions.map((q, i) => (
				<li key={q.name} className="tv-ask-q">
					{q.lead && <div className="tv-ask-lead">{q.lead}</div>}
					<span className="tv-ask-num">{i + 1}</span>
					<div className="tv-ask-qbody">
						<div className="tv-ask-qtext">
							{q.question}
							{q.kind === "multi" && (
								<span className="tv-ask-kind">{t("toolRender.ask.multi")}</span>
							)}
						</div>
						{q.options.length > 0 ? (
							<ul className="tv-ask-opts">
								{q.options.map((opt, n) => (
									<li key={n} className="tv-ask-opt">
										<Mark kind={q.kind} on={false} />
										<span className="tv-ask-opt-text">
											{opt.main}
											{opt.hint && (
												<span className="tv-ask-hint">{opt.hint}</span>
											)}
										</span>
										{opt.preset && (
											<span className="tv-ask-rec">
												{t("toolRender.ask.recommended")}
											</span>
										)}
									</li>
								))}
							</ul>
						) : (
							<div className="tv-ask-blank" aria-hidden="true" />
						)}
					</div>
				</li>
			))}
		</ol>
	);
}

/** 输出段的等待状态：呼吸点 + 「等待你作答」+ 已等时长。 */
function Waiting({ startedAt }: { startedAt?: number }): ReactNode {
	const { t } = useTranslation();
	const now = useLiveNow(startedAt !== undefined);
	return (
		<div className="tv-ask-pending" role="status">
			<span className="tv-ask-wait-dot" aria-hidden="true" />
			<span>{t("toolRender.ask.waitingLong")}</span>
			{startedAt !== undefined && (
				<span className="tv-ask-pending-time">
					{formatElapsed(now - startedAt)}
				</span>
			)}
		</div>
	);
}

/**
 * 输出：回执。逐题给出选择（选中高亮、未选淡出、自由填写单列）与选项备注；等待中给等待状态；
 * 没有结构化回答时给结果文本（取消、出错、Core 19 的「超时时必填项仍空」）。
 */
function Result(props: ToolRenderProps): ReactNode {
	const { t } = useTranslation();
	const { args, result, startedAt } = props;
	const questions = useQuestions(formHtml(args));
	if (props.running === true && !result)
		return <Waiting startedAt={startedAt} />;
	const details = detailsRecord(result);
	const answers = answersOf(details);
	if (!answers || answers.length === 0) {
		if (!result) return null;
		if (result.isError) return <ErrorBlock text={resultTextOf(result)} />;
		return details?.timedOut === true ? (
			<Note tone="warn">{t("toolRender.ask.timedOutEmptyNote")}</Note>
		) : (
			<ResultText result={result} maxLines={10} />
		);
	}
	const notes = notesOf(details);
	const byName = new Map((questions ?? []).map((q) => [q.name, q]));
	return (
		<>
			{details?.timedOut === true && (
				<Note tone="warn">{t("toolRender.ask.timedOutNote")}</Note>
			)}
			<ol className="tv-ask-sheet">
				{answers.map((entry, i) => {
					const q = byName.get(entry.name);
					const kind = q?.kind ?? "text";
					const options = q?.options ?? [];
					const { picked, rest } =
						options.length > 0
							? pickOptions(entry.answer, options)
							: { picked: new Set<string>(), rest: entry.answer };
					const mine = notes.filter(
						(note) =>
							note.name === entry.name && note.question === entry.question,
					);
					return (
						<li key={i} className="tv-ask-q">
							<span className="tv-ask-num">{i + 1}</span>
							<div className="tv-ask-qbody">
								<div className="tv-ask-qtext tv-ask-qtext--sheet">
									{entry.question}
								</div>
								{options.length > 0 && (
									<ul className="tv-ask-opts">
										{options.map((opt, n) => {
											const on = picked.has(opt.label);
											return (
												<li
													key={n}
													className={`tv-ask-opt${on ? " tv-ask-opt--on" : " tv-ask-opt--off"}`}
												>
													<Mark kind={kind} on={on} />
													<span className="tv-ask-opt-text">
														{opt.main}
														{opt.hint && (
															<span className="tv-ask-hint">{opt.hint}</span>
														)}
													</span>
												</li>
											);
										})}
									</ul>
								)}
								{rest !== "" && <div className="tv-ask-free">{rest}</div>}
								{entry.answer === "" && (
									<div className="tv-ask-none">
										{t("toolRender.ask.noAnswer")}
									</div>
								)}
								{mine.map((note, n) => (
									<div key={n} className="tv-ask-note">
										<Icon name="penNib" size={11} />
										<span className="tv-ask-note-opt">{note.option}</span>
										<span>{note.text}</span>
									</div>
								))}
							</div>
						</li>
					);
				})}
			</ol>
			<ResultImages result={result} />
		</>
	);
}

export const askRenderer: ToolRenderer = { Summary, Meta, Input, Result };
