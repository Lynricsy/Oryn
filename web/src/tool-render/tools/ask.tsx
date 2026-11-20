/** `ask` — an HTML form posed to the user mid-run, answered with its form data. */
import type { ReactNode } from "react";
import { Badge, InvalidArg, Note, Output, ResultText, Row } from "../parts";
import type { ToolRenderer, ToolRenderProps } from "../types";
import { detailsRecord, isRecord, normalizeWs, str, truncate } from "../util";

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
 * many distinct named controls it has.
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

function Summary({ args, result }: ToolRenderProps): ReactNode {
	const html = str(args.html) ?? legacyQuestionsHtml(args.questions);
	const answers = answersOf(detailsRecord(result));
	const outline = html ? formOutline(html) : { headings: [], fields: 0 };
	const first = answers?.[0]?.question ?? outline.headings[0];
	if (!first)
		return html === null && !result ? <InvalidArg what="html" /> : null;
	const count = answers ? answers.length : outline.fields;
	return (
		<>
			<span>{truncate(normalizeWs(first), 70)}</span>
			{count > 1 && <Badge>{count} fields</Badge>}
		</>
	);
}

/** 输入：表单的标题提纲 + 表单 HTML 原文（问题与选项都在里面）。 */
function Input({ args, result }: ToolRenderProps): ReactNode {
	const html = str(args.html) ?? legacyQuestionsHtml(args.questions);
	if (html === null) return result ? null : <InvalidArg what="html" />;
	const { headings } = formOutline(html);
	return (
		<>
			{headings.length > 0 && (
				<div className="tv-list">
					{headings.map((heading, i) => (
						<Row key={i}>{heading}</Row>
					))}
				</div>
			)}
			{html.trim() !== "" && (
				<Output text={html} lang="xml" variant="code" maxLines={8} />
			)}
		</>
	);
}

/**
 * 输出：用户逐题的回答与选项备注；没有结构化回答时给结果文本
 * （含 Core 19 的「超时时必填项仍空」：answers 为空，不是自动提交的回答）。
 */
function Result({ result }: ToolRenderProps): ReactNode {
	const details = detailsRecord(result);
	const answers = answersOf(details);
	if (!answers || answers.length === 0)
		return <ResultText result={result} maxLines={10} />;
	const notes = notesOf(details);
	return (
		<>
			<div className="tv-list">
				{answers.map((entry, i) => (
					<div key={i}>
						<Row>{entry.question}</Row>
						{entry.answer ? (
							<Row k={<span className="tv-ok-text">✓</span>}>
								<span>{entry.answer}</span>
							</Row>
						) : (
							<Row k="—">
								<span className="tv-faint">(no answer)</span>
							</Row>
						)}
						{notes
							.filter(
								(note) =>
									note.name === entry.name && note.question === entry.question,
							)
							.map((note, noteIndex) => (
								<Note key={`${i}-${noteIndex}`}>
									✎ {note.option}: {note.text}
								</Note>
							))}
					</div>
				))}
			</div>
			{details?.timedOut === true && (
				<Note tone="warn">auto-selected after timeout — not a user choice</Note>
			)}
		</>
	);
}

export const askRenderer: ToolRenderer = { Summary, Input, Result };
