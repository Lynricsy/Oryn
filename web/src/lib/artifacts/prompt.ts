/**
 * 发给 agent 的 artifact 消息（计划 M14 3.4 / 3.5 / 6.6）：评论、选中的元素、交给 Agent 的画框与标注。
 *
 * 防提示注入的边界：来自页面的任何文本（标题、页名、路径、标签名、元素文本、标注文字）只出现在两行随机
 * `mark` 之间，消息里另有一句说明那是数据、不是指示。`mark` 由 `boundary` 生成，保证不出现在消息的任何
 * 其它文本里（包括用户写的评论与原文），页面无法提前伪造结束行。说明性的句子走 i18n，数据块的行格式
 * 是固定的英文字面值（两种界面语言相同）。
 */
import type { TFunction } from "i18next";
import type {
	ArtifactCommentAnchor,
	ArtifactCommentThread,
} from "../../api/artifact-queries";
import { type CanvasNote, type CanvasPage, noteBox } from "./canvas-doc";

/** 边界行的前缀；后接 16 位十六进制。 */
export const MARK_PREFIX = "oryn-data-";
/** 数据块里标题的上限（码点）。 */
export const PROMPT_TITLE_CHARS = 120;

/** 消息里引用的 artifact。 */
export interface PromptArtifact {
	id: string;
	title: string;
}

/** 选中的元素（输入区的引用条，3.5）。 */
export interface ArtifactSelectionRef {
	artifactId: string;
	title: string;
	version: number;
	anchor: ArtifactCommentAnchor;
}

/** 生成一个不出现在任一 `texts` 里的边界：`oryn-data-<16 位十六进制>`。 */
export function boundary(texts: readonly string[]): string {
	for (;;) {
		const bytes = crypto.getRandomValues(new Uint8Array(8));
		const hex = Array.from(bytes, (b) => b.toString(16).padStart(2, "0"));
		const mark = `${MARK_PREFIX}${hex.join("")}`;
		if (!texts.some((s) => s.includes(mark))) return mark;
	}
}

/** 控制字符与行 / 段分隔符折叠成一个空格：数据块里每项恰好一行。 */
function oneLine(s: string): string {
	return s.replace(/[\p{Cc}\u2028\u2029]+/gu, " ");
}

function titleLine(title: string): string {
	return Array.from(oneLine(title)).slice(0, PROMPT_TITLE_CHARS).join("");
}

/** 锚点在数据块里的位置描述：`{path}, page {n} "{页名}", <{tag}> at offset {offset}: "{text}"`。 */
function located(a: ArtifactCommentAnchor): string {
	const parts = [oneLine(a.path)];
	if (a.page) parts.push(`page ${a.page.number} "${oneLine(a.page.name)}"`);
	if (a.tag === null) parts.push("whole page");
	else {
		const where =
			a.offset === null || a.missing
				? "made by a script"
				: `at offset ${a.offset}`;
		const text = a.text ? `: "${oneLine(a.text)}"` : "";
		parts.push(`<${oneLine(a.tag)}> ${where}${text}`);
	}
	return parts.join(", ");
}

/** 说明行在前，然后是数据说明与夹在两行 `mark` 之间的数据块。 */
function withData(
	head: readonly string[],
	data: readonly string[],
	t: TFunction,
) {
	const mark = boundary([...head, ...data]);
	return [
		...head,
		"",
		t("artifacts.prompt.dataNotice", { mark }),
		"",
		mark,
		...data,
		mark,
	].join("\n");
}

/** 把评论串发给线程的消息。`threads` 的顺序就是消息里的编号。 */
export function commentsPrompt(
	artifact: PromptArtifact,
	version: number,
	threads: readonly ArtifactCommentThread[],
	t: TFunction,
): string {
	const items = threads.map(
		(th, i) =>
			`${i + 1}. (${th.id}) ${th.comments.map((c) => c.body).join("\n")}`,
	);
	const data = [
		`artifact: "${titleLine(artifact.title)}"`,
		...threads.map((th, i) => `${i + 1}. ${located(th.anchor)}`),
	];
	return withData(
		[
			t("artifacts.prompt.comments", { id: artifact.id, version }),
			"",
			...items,
		],
		data,
		t,
	);
}

/** 发送普通消息时带上输入区里选中的元素（3.5）。 */
export function selectionPrompt(
	text: string,
	selected: ArtifactSelectionRef,
	t: TFunction,
): string {
	return withData(
		[
			text,
			"",
			t("artifacts.prompt.selected", {
				id: selected.artifactId,
				version: selected.version,
			}),
		],
		[`artifact: "${titleLine(selected.title)}"`, located(selected.anchor)],
		t,
	);
}

/** 标注压在哪个画框上：包围盒中心所在的，其次是与它相交的第一个。 */
function hostPage(
	note: CanvasNote,
	pages: readonly CanvasPage[],
): CanvasPage | undefined {
	const b = noteBox(note);
	const cx = b.x + b.width / 2;
	const cy = b.y + b.height / 2;
	const inside = (p: CanvasPage) =>
		p.canvas === note.canvas &&
		cx >= p.x &&
		cx <= p.x + p.width &&
		cy >= p.y &&
		cy <= p.y + p.height;
	const overlaps = (p: CanvasPage) =>
		p.canvas === note.canvas &&
		b.x <= p.x + p.width &&
		b.x + b.width >= p.x &&
		b.y <= p.y + p.height &&
		b.y + b.height >= p.y;
	return pages.find(inside) ?? pages.find(overlaps);
}

function noteLine(note: CanvasNote, pages: readonly CanvasPage[]): string {
	const b = noteBox(note);
	const page = hostPage(note, pages);
	const on = page ? `, on page "${oneLine(page.name)}"` : "";
	const text =
		note.kind === "text" || note.kind === "sticky"
			? `: "${oneLine(note.text)}"`
			: "";
	return `note ${note.id}, ${note.kind} in ${note.color}${on} at ${Math.round(b.x)},${Math.round(b.y)} size ${Math.round(b.width)}x${Math.round(b.height)}${text}`;
}

/** 「交给 Agent」（6.6）：选中的画框与其上的标注。 */
export function handoffPrompt(
	artifact: PromptArtifact,
	version: number,
	pages: readonly CanvasPage[],
	notes: readonly CanvasNote[],
	t: TFunction,
): string {
	return withData(
		[
			t("artifacts.prompt.handoff"),
			"",
			t("artifacts.prompt.selected", { id: artifact.id, version }),
		],
		[
			`artifact: "${titleLine(artifact.title)}"`,
			...pages.map(
				(p) =>
					`page ${p.id} "${oneLine(p.name)}", file ${oneLine(p.file)}, ${p.width}x${p.height}`,
			),
			...notes.map((n) => noteLine(n, pages)),
		],
		t,
	);
}
