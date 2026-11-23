/**
 * `eval` (aliases: js, python, notebook) — code cells executed in the
 * persistent kernel (py/js/rb/jl). Args arrive either as the modern single-cell
 * flat shape (`language`/`code`/`title`/`timeout`/`reset`), a legacy `cells`
 * array, or a legacy framed `input` string (`*** Cell`, `*** Begin LANG`,
 * `===== info =====`); all render as discrete highlighted cells in the input
 * section. Typed per-cell details (`details.cells`: status pending → running →
 * complete | error, output, views, durationMs, statusEvents) render notebook-style
 * in the result section, live while the call runs.
 */
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { formatDuration } from "../../lib/format";
import {
	Badge,
	type CellTone,
	CodeBlock,
	InvalidArg,
	MarkdownBlock,
	Note,
	Output,
	ProgressCells,
	ResultImages,
	ResultText,
} from "../parts";
import type { ToolRenderer, ToolRenderProps } from "../types";
import {
	argsDigest,
	detailsRecord,
	isRecord,
	normalizeWs,
	num,
	resultTextOf,
	shortenPath,
	str,
	truncate,
} from "../util";

interface EvalCell {
	lang: string;
	title: string;
	/** Per-cell timeout as written (`60s`, `90`), or null. */
	timeout: string | null;
	/** Reset the kernel before this cell. */
	reset: boolean;
	code: string;
}

const HLJS_LANG: Record<string, string> = {
	py: "python",
	js: "javascript",
	ts: "typescript",
	rb: "ruby",
	jl: "julia",
};

/** 语言徽标上的名字（专有名词，不翻译）。 */
const LANG_NAME: Record<string, string> = {
	py: "Python",
	js: "JavaScript",
	ts: "TypeScript",
	rb: "Ruby",
	jl: "Julia",
};

/** Map an eval language token to its canonical short id, or null when unknown. */
function evalLangAlias(token: string | undefined): string | null {
	const t = (token ?? "").toUpperCase();
	if (t === "PY" || t === "PYTHON" || t === "IPY" || t === "IPYTHON")
		return "py";
	if (t === "JS" || t === "JAVASCRIPT") return "js";
	if (t === "TS" || t === "TYPESCRIPT") return "ts";
	if (t === "RB" || t === "RUBY") return "rb";
	if (t === "JL" || t === "JULIA") return "jl";
	return null;
}

/** Tokenize a `*** Cell` header attribute list, preserving quoted segments. */
function tokenizeCellAttrs(input: string): string[] {
	const tokens: string[] = [];
	let i = 0;
	while (i < input.length) {
		while (i < input.length && /\s/.test(input[i] ?? "")) i++;
		if (i >= input.length) break;
		let tok = "";
		while (i < input.length && !/\s/.test(input[i] ?? "")) {
			const ch = input[i] ?? "";
			if (ch === '"' || ch === "'") {
				tok += ch;
				i++;
				while (i < input.length && (input[i] ?? "") !== ch) {
					tok += input[i] ?? "";
					i++;
				}
				if (i < input.length) {
					tok += input[i] ?? "";
					i++;
				}
			} else {
				tok += ch;
				i++;
			}
		}
		tokens.push(tok);
	}
	return tokens;
}

/** Drop trailing blank lines of a cell body. */
function trimTrailingBlank(lines: string[]): string {
	while (lines.length > 0 && (lines[lines.length - 1] ?? "").trim() === "")
		lines.pop();
	return lines.join("\n");
}

/** Canonical `*** Cell <attrs>` framing. */
function parseEvalCellsCell(text: string): EvalCell[] {
	const CELL = /^\*{2,}\s*Cell\b\s*(.*)$/i;
	const END = /^\*{2,}\s*End\b.*$/i;
	const ATTR = /^([a-zA-Z][\w-]*)(?::(?:"([^"]*)"|'([^']*)'|(.*)))?$/;
	const DUR = /^\d+(?:ms|s|m)?$/;
	const ID_KEYS = ["id", "title", "name", "cell", "file", "label"];
	const T_KEYS = ["t", "timeout", "duration", "time"];
	const RST_KEYS = ["rst", "reset"];
	const lines = text.split("\n");
	if (lines.length > 0 && lines[lines.length - 1] === "") lines.pop();
	const cells: EvalCell[] = [];
	let i = 0;
	while (i < lines.length && (lines[i] ?? "").trim() === "") i++;
	while (i < lines.length) {
		const m = CELL.exec(lines[i] ?? "");
		if (!m) {
			i++;
			continue;
		}
		const tokens = tokenizeCellAttrs(m[1] ?? "");
		let lang: string | null = null;
		let title = "";
		let timeout: string | null = null;
		let reset = false;
		const titleParts: string[] = [];
		for (const tok of tokens) {
			if (RST_KEYS.includes(tok.toLowerCase())) {
				reset = true;
				continue;
			}
			const am = ATTR.exec(tok);
			if (am && tok.includes(":")) {
				const key = (am[1] ?? "").toLowerCase();
				const value = am[2] ?? am[3] ?? am[4] ?? "";
				const lc = evalLangAlias(key);
				if (lc) {
					if (!lang) lang = lc;
					if (!title && value) title = value;
					continue;
				}
				if (ID_KEYS.includes(key)) {
					if (!title) title = value;
					continue;
				}
				if (T_KEYS.includes(key)) {
					timeout = value;
					continue;
				}
				if (RST_KEYS.includes(key)) reset = true;
				continue;
			}
			const lc = evalLangAlias(tok);
			if (lc && !lang) {
				lang = lc;
				continue;
			}
			if (DUR.test(tok)) {
				timeout = tok;
				continue;
			}
			titleParts.push(tok);
		}
		if (!title && titleParts.length > 0) title = titleParts.join(" ");
		i++;
		const codeLines: string[] = [];
		while (i < lines.length) {
			if (END.test(lines[i] ?? "")) {
				i++;
				break;
			}
			if (CELL.test(lines[i] ?? "")) break;
			codeLines.push(lines[i] ?? "");
			i++;
		}
		cells.push({
			lang: lang ?? "py",
			title,
			timeout,
			reset,
			code: trimTrailingBlank(codeLines),
		});
		while (i < lines.length && (lines[i] ?? "").trim() === "") i++;
	}
	return cells;
}

/** Older `*** Begin LANG` / `*** Title:` / `*** End` framing. */
function parseEvalCellsBegin(text: string): EvalCell[] {
	const BEGIN = /^\*{2,}\s*Begin\b\s*(\S+)?\s*$/i;
	const END = /^\*{2,}\s*End\b.*$/i;
	const TITLE = /^\*{2,}\s*Title\s*:\s*(.+?)\s*$/i;
	const TIMEOUT = /^\*{2,}\s*Timeout\s*:\s*(\S+)\s*$/i;
	const RESET = /^\*{2,}\s*Reset\s*$/i;
	const lines = text.split("\n");
	if (lines.length > 0 && lines[lines.length - 1] === "") lines.pop();
	const cells: EvalCell[] = [];
	let i = 0;
	while (i < lines.length && (lines[i] ?? "").trim() === "") i++;
	while (i < lines.length) {
		const beginMatch = BEGIN.exec(lines[i] ?? "");
		if (!beginMatch) {
			i++;
			continue;
		}
		const lang = evalLangAlias(beginMatch[1]) ?? "py";
		i++;
		let title = "";
		let timeout: string | null = null;
		let reset = false;
		while (i < lines.length) {
			const tm = TITLE.exec(lines[i] ?? "");
			if (tm) {
				if (!title) title = tm[1] ?? "";
				i++;
				continue;
			}
			const to = TIMEOUT.exec(lines[i] ?? "");
			if (to) {
				timeout = to[1] ?? null;
				i++;
				continue;
			}
			if (RESET.test(lines[i] ?? "")) {
				reset = true;
				i++;
				continue;
			}
			break;
		}
		const codeLines: string[] = [];
		while (i < lines.length) {
			if (END.test(lines[i] ?? "")) {
				i++;
				break;
			}
			if (BEGIN.test(lines[i] ?? "")) break;
			codeLines.push(lines[i] ?? "");
			i++;
		}
		cells.push({
			lang,
			title,
			timeout,
			reset,
			code: trimTrailingBlank(codeLines),
		});
		while (i < lines.length && (lines[i] ?? "").trim() === "") i++;
	}
	return cells;
}

/** Oldest `===== info =====` bar framing; bare code becomes one python cell. */
function parseEvalCellsLegacy(input: string): EvalCell[] {
	const HEADER = /^={5,}\s*(.*?)\s*={5,}\s*$/;
	const lines = input.split("\n");
	const cells: EvalCell[] = [];
	let inheritedLang = "py";
	let current: EvalCell | null = null;
	for (const line of lines) {
		const m = HEADER.exec(line);
		if (m) {
			if (current) cells.push(current);
			const info = m[1] ?? "";
			let lang = inheritedLang;
			let title = "";
			const langMatch = info.match(/^(py|js|ts|rb|jl)(?::"([^"]*)")?/);
			if (langMatch) {
				lang = langMatch[1] ?? lang;
				if (langMatch[2]) title = langMatch[2];
			}
			if (!title) {
				const idMatch = info.match(/id:"([^"]*)"/);
				if (idMatch) title = idMatch[1] ?? title;
			}
			inheritedLang = lang;
			const tMatch = info.match(/(?:^|\s)t:(\S+)/);
			current = {
				lang,
				title,
				timeout: tMatch?.[1] ?? null,
				reset: /(?:^|\s)rst(?:\s|$)/.test(info),
				code: "",
			};
		} else {
			if (!current)
				current = {
					lang: inheritedLang,
					title: "",
					timeout: null,
					reset: false,
					code: "",
				};
			current.code += (current.code ? "\n" : "") + line;
		}
	}
	if (current) cells.push(current);
	return cells.map((c) => ({ ...c, code: c.code.replace(/\s+$/, "") }));
}

function parseEvalCells(input: string): EvalCell[] {
	if (/^\*{2,}\s*Cell\b/im.test(input)) return parseEvalCellsCell(input);
	if (/^\*{2,}\s*Begin\b/im.test(input)) return parseEvalCellsBegin(input);
	return parseEvalCellsLegacy(input);
}

/** Cells from either arg shape; `name` disambiguates legacy alias tools. */
function cellsFromArgs(
	args: Record<string, unknown>,
	name: string,
): EvalCell[] {
	const raw = args.cells;
	if (Array.isArray(raw)) {
		const out: EvalCell[] = [];
		for (const item of raw) {
			if (!isRecord(item)) continue;
			out.push({
				lang: evalLangAlias(str(item.language) ?? undefined) ?? "py",
				title: str(item.title) ?? "",
				timeout: num(item.timeout) !== null ? `${item.timeout}s` : null,
				reset: item.reset === true,
				code: str(item.code) ?? "",
			});
		}
		return out;
	}
	const input = str(args.input);
	if (input !== null)
		return parseEvalCells(input).filter((c) => c.code !== "" || c.title !== "");
	const code = str(args.code);
	if (code !== null) {
		const lang =
			evalLangAlias(str(args.language) ?? undefined) ??
			(name === "js" ? "js" : "py");
		return [
			{
				lang,
				title: str(args.title) ?? "",
				timeout: num(args.timeout) !== null ? `${args.timeout}s` : null,
				reset: args.reset === true,
				code,
			},
		];
	}
	return [];
}

/** Per-cell execution result from `result.details.cells` (typed by the tool). */
interface DetailCell {
	index: number;
	title: string;
	code: string;
	lang: string | null;
	output: string;
	status: string;
	durationMs: number | null;
	exitCode: number | null;
	views: EvalViewEntry[];
	events: StatusEvent[];
	/** Evictable events dropped from the front of `events`. */
	eventsElided: number;
}

/** One prelude status event (`phase`, `log`, `read`, `agent`…), already narrowed to display text. */
interface StatusEvent {
	op: string;
	text: string;
	error: boolean;
}

interface EvalTableColumn {
	head: string;
	numeric: boolean;
}

/** A pretty view of part of a cell's printed output (mirrors the tool's `EvalView`). */
type EvalView =
	| { kind: "table"; columns: EvalTableColumn[]; rows: string[][] }
	| { kind: "json"; value: unknown }
	| { kind: "markdown"; text: string };

interface EvalViewEntry {
	text: string;
	view: EvalView;
}

type EvalOutputPart =
	| { kind: "text"; text: string }
	| { kind: "view"; view: EvalView };

function viewOf(raw: unknown): EvalView | null {
	if (!isRecord(raw)) return null;
	switch (raw.kind) {
		case "table": {
			if (!Array.isArray(raw.columns) || !Array.isArray(raw.rows)) return null;
			const columns: EvalTableColumn[] = [];
			for (const col of raw.columns) {
				if (!isRecord(col) || typeof col.head !== "string") return null;
				columns.push({ head: col.head, numeric: col.numeric === true });
			}
			const rows: string[][] = [];
			for (const row of raw.rows) {
				if (!Array.isArray(row)) return null;
				rows.push(
					row.map((cell) => (typeof cell === "string" ? cell : String(cell))),
				);
			}
			return { kind: "table", columns, rows };
		}
		case "json":
			return "value" in raw ? { kind: "json", value: raw.value } : null;
		case "markdown":
			return typeof raw.text === "string"
				? { kind: "markdown", text: raw.text }
				: null;
		default:
			return null;
	}
}

function viewsOf(raw: unknown): EvalViewEntry[] {
	if (!Array.isArray(raw)) return [];
	const out: EvalViewEntry[] = [];
	for (const item of raw) {
		if (!isRecord(item) || typeof item.text !== "string") continue;
		const view = viewOf(item.view);
		if (view) out.push({ text: item.text, view });
	}
	return out;
}

/** Display text of one status event, after the TUI's per-op wording (sans icons). */
function eventOf(raw: unknown): StatusEvent | null {
	if (!isRecord(raw)) return null;
	const op = str(raw.op);
	if (!op) return null;
	const error = raw.error != null && raw.error !== false;
	if (error) return { op, text: normalizeWs(String(raw.error)), error };
	const path = str(raw.path);
	let parts: (string | null)[];
	switch (op) {
		case "phase":
			parts = [str(raw.title)];
			break;
		case "log":
			parts = [str(raw.message)];
			break;
		case "agent":
		case "judge_batch":
		case "workpool":
			parts = [
				str(raw.action),
				str(raw.id) ?? str(raw.pool) ?? str(raw.agent),
				num(raw.total) !== null
					? `${num(raw.done) ?? 0}/${num(raw.total)}`
					: null,
				str(raw.status),
			];
			break;
		case "completion":
			parts = [str(raw.model), str(raw.tier)];
			break;
		default:
			parts = [
				raw.detail != null ? String(raw.detail) : null,
				num(raw.count) !== null ? String(num(raw.count)) : null,
				path ? shortenPath(path) : null,
			];
	}
	const text = normalizeWs(parts.filter(Boolean).join(" · "));
	return { op, text, error: false };
}

/** Same split as pi-tui's `splitEvalOutput`: views in order via a moving `indexOf` cursor; unmatched skipped. */
function splitEvalOutput(
	output: string,
	views: readonly EvalViewEntry[],
): EvalOutputPart[] {
	const parts: EvalOutputPart[] = [];
	const pushText = (
		chunk: string,
		trimStart: boolean,
		trimEnd: boolean,
	): void => {
		let text = chunk;
		if (trimStart) text = text.replace(/^\n+/, "");
		if (trimEnd) text = text.replace(/\n+$/, "");
		if (text.trim().length > 0) parts.push({ kind: "text", text });
	};
	let cursor = 0;
	for (const entry of views) {
		if (!entry.text) continue;
		const at = output.indexOf(entry.text, cursor);
		if (at < 0) continue;
		pushText(output.slice(cursor, at), cursor > 0, true);
		parts.push({ kind: "view", view: entry.view });
		cursor = at + entry.text.length;
	}
	pushText(output.slice(cursor), cursor > 0, cursor > 0);
	return parts;
}

function jsonText(value: unknown): string {
	try {
		return JSON.stringify(value, null, 2) ?? String(value);
	} catch {
		return String(value);
	}
}

/** 表格视图最多先显示的行数；其余收在「展开」后面。 */
const TABLE_ROWS = 12;

function TableView({
	columns,
	rows,
}: {
	columns: EvalTableColumn[];
	rows: string[][];
}): ReactNode {
	const { t } = useTranslation();
	const align = (c: number) => (columns[c]?.numeric ? "tv-nb-num" : undefined);
	const shown = rows.slice(0, TABLE_ROWS);
	return (
		<div className="tv-nb-tablewrap">
			<table className="tv-nb-table">
				<thead>
					<tr>
						{columns.map((col, c) => (
							<th key={`h${c}`} className={align(c)}>
								{col.head}
							</th>
						))}
					</tr>
				</thead>
				<tbody>
					{shown.map((row, r) => (
						<tr key={`r${r}`}>
							{row.map((cell, c) => (
								<td key={`c${c}`} className={align(c)}>
									{cell}
								</td>
							))}
						</tr>
					))}
				</tbody>
			</table>
			{rows.length > shown.length && (
				<div className="tv-nb-more">
					{t("toolRender.eval.moreRows", { count: rows.length - shown.length })}
				</div>
			)}
		</div>
	);
}

function ViewBlock({ view }: { view: EvalView }): ReactNode {
	switch (view.kind) {
		case "table":
			return <TableView columns={view.columns} rows={view.rows} />;
		case "json":
			return (
				<Output
					text={jsonText(view.value)}
					lang="json"
					variant="code"
					maxLines={12}
				/>
			);
		case "markdown":
			return <MarkdownBlock text={view.text} maxHeight={240} />;
	}
}

const TRACEBACK_RE = /(?:^|\n)Traceback \(most recent call last\):/;

/** 出错单元格的输出：普通打印在前、traceback 单独成块（红）；认不出 traceback 时整段按错误显示。 */
function ErrorOutput({ text }: { text: string }): ReactNode {
	const tb = TRACEBACK_RE.exec(text);
	if (!tb) return <Output text={text} maxLines={14} error />;
	const at = tb.index + (tb[0].startsWith("\n") ? 1 : 0);
	const before = text.slice(0, at).replace(/\s+$/, "");
	return (
		<>
			{before && <Output text={before} maxLines={10} />}
			<Output text={text.slice(at)} maxLines={14} error />
		</>
	);
}

function CellOutput({ cell }: { cell: DetailCell }): ReactNode {
	if (cell.status === "error" && cell.views.length === 0)
		return <ErrorOutput text={cell.output} />;
	const parts = splitEvalOutput(cell.output, cell.views);
	return parts.map((part, i) =>
		part.kind === "text" ? (
			<Output
				key={`p${i}`}
				text={part.text}
				maxLines={12}
				error={cell.status === "error"}
			/>
		) : (
			<ViewBlock key={`p${i}`} view={part.view} />
		),
	);
}

/** 运行中的单元格只看最近几条事件，结束后多留几条。 */
const EVENTS_RUNNING = 3;
const EVENTS_DONE = 6;

function CellEvents({
	cell,
	running,
}: {
	cell: DetailCell;
	running: boolean;
}): ReactNode {
	const { t } = useTranslation();
	const visible = cell.events.filter((e) => e.text !== "" || e.op === "phase");
	if (visible.length === 0) return null;
	const keep = running ? EVENTS_RUNNING : EVENTS_DONE;
	const shown = visible.slice(-keep);
	const hidden = visible.length - shown.length + cell.eventsElided;
	return (
		<ul className="tv-nb-events">
			{hidden > 0 && (
				<li className="tv-nb-event tv-nb-event--more">
					{t("toolRender.eval.earlierEvents", { count: hidden })}
				</li>
			)}
			{shown.map((e, i) => (
				<li
					key={`e${i}`}
					className={`tv-nb-event${e.op === "phase" ? " tv-nb-event--phase" : ""}${e.error ? " tv-nb-event--err" : ""}${running && i === shown.length - 1 ? " tv-nb-event--live" : ""}`}
				>
					{e.op !== "phase" && e.op !== "log" && (
						<span className="tv-nb-event-op">{e.op}</span>
					)}
					<span className="tv-nb-event-text">{e.text}</span>
				</li>
			))}
		</ul>
	);
}

function detailCellsOf(details: Record<string, unknown> | null): DetailCell[] {
	const raw = details?.cells;
	if (!Array.isArray(raw)) return [];
	const out: DetailCell[] = [];
	for (let i = 0; i < raw.length; i++) {
		const item: unknown = raw[i];
		if (!isRecord(item)) continue;
		const language = str(item.language);
		out.push({
			index: num(item.index) ?? i,
			title: str(item.title) ?? "",
			code: str(item.code) ?? "",
			lang: language !== null ? (evalLangAlias(language) ?? "py") : null,
			output: str(item.output) ?? "",
			status: str(item.status) ?? "",
			durationMs: num(item.durationMs),
			exitCode: num(item.exitCode),
			views: viewsOf(item.views),
			events: Array.isArray(item.statusEvents)
				? item.statusEvents
						.map(eventOf)
						.filter((e): e is StatusEvent => e !== null)
				: [],
			eventsElided: num(item.statusEventsElided) ?? 0,
		});
	}
	return out;
}

/** 单元格的显示状态：调用已结束时仍是 pending / running 的格子没有执行（前面的格子出错了）。 */
type CellState = "pending" | "running" | "ok" | "error" | "skipped";

function cellState(cell: DetailCell, finished: boolean): CellState {
	switch (cell.status) {
		case "error":
			return "error";
		case "running":
			return finished ? "skipped" : "running";
		case "pending":
			return finished ? "skipped" : "pending";
		default:
			return "ok";
	}
}

const STATE_TONE: Record<CellState, CellTone> = {
	pending: "idle",
	running: "accent",
	ok: "ok",
	error: "err",
	skipped: "idle",
};

const ERROR_NAME_RE =
	/^(?:Uncaught )?([A-Za-z_$][\w.$]*(?:Error|Exception|Interrupt|Exit))\b(?::|$)/;

/** 出错单元格的读数：异常类型（Python traceback 末行、JS 的 `TypeError: …`），否则退出码。 */
function errorLabel(cell: DetailCell): string | null {
	const lines = cell.output.trimEnd().split("\n");
	for (let i = lines.length - 1; i >= 0; i--) {
		const m = ERROR_NAME_RE.exec((lines[i] ?? "").trim());
		if (m) return m[1] ?? null;
	}
	return cell.exitCode !== null && cell.exitCode !== 0
		? `exit ${cell.exitCode}`
		: null;
}

/** `details.meta.truncation`：输出在 Core 侧被截断（保留尾部）。 */
function truncationOf(
	details: Record<string, unknown> | null,
): { shown: number | null; total: number | null } | null {
	const meta = isRecord(details?.meta) ? details.meta : null;
	const trunc = meta && isRecord(meta.truncation) ? meta.truncation : null;
	if (!trunc) return null;
	return { shown: num(trunc.outputLines), total: num(trunc.totalLines) };
}

function renderCells(
	args: Record<string, unknown>,
	name: string,
	detailCells: DetailCell[],
): EvalCell[] {
	const cells = cellsFromArgs(args, name);
	if (cells.length > 0) return cells;
	return detailCells.map((c) => ({
		lang: c.lang ?? "py",
		title: c.title,
		timeout: null,
		reset: false,
		code: c.code,
	}));
}

function Summary({ name, args, result, partial }: ToolRenderProps): ReactNode {
	const cells = renderCells(
		args,
		name,
		detailCellsOf(detailsRecord(result ?? partial)),
	);
	const first = cells[0];
	if (!first) return <span className="tv-muted">{argsDigest(args)}</span>;
	const label =
		first.title ||
		normalizeWs(first.code.split("\n").find((l) => l.trim() !== "") ?? "");
	const langs = [...new Set(cells.map((c) => LANG_NAME[c.lang] ?? c.lang))];
	return (
		<>
			{label && <span>{truncate(label, 72)}</span>}
			{langs.map((l) => (
				<Badge key={l}>{l}</Badge>
			))}
		</>
	);
}

/**
 * 头部读数：多格时一格一点的进度（运行中另给 `2/3`）；出错格的异常类型或退出码；
 * 截断提示；结束后的总耗时。
 */
function Meta({
	name,
	args,
	result,
	partial,
	running,
}: ToolRenderProps): ReactNode {
	const { t } = useTranslation();
	const details = detailsRecord(result ?? partial);
	const cells = detailCellsOf(details);
	const finished = result !== undefined && !running;
	const total = Math.max(cells.length, cellsFromArgs(args, name).length);
	const states = cells.map((c) => cellState(c, finished));
	const done = states.filter((s) => s === "ok" || s === "error").length;
	const failed = cells.find((c) => c.status === "error");
	const failedLabel = failed ? errorLabel(failed) : null;
	const trunc = truncationOf(details);
	const duration = cells.reduce((sum, c) => sum + (c.durationMs ?? 0), 0);
	return (
		<>
			{total > 1 && states.length > 0 && (
				<ProgressCells
					tones={states.map((s) => STATE_TONE[s])}
					label={t("toolRender.eval.progress", { done, total })}
				/>
			)}
			{total > 1 && !finished && states.length > 0 && (
				<span>
					{done}/{total}
				</span>
			)}
			{failed && (
				<span className="tv-meta-err">
					{total > 1 && `#${failed.index + 1} `}
					{failedLabel ?? t("toolRender.eval.failed")}
				</span>
			)}
			{trunc && (
				<span className="tv-meta-warn">{t("toolRender.eval.truncated")}</span>
			)}
			{finished && duration > 0 && <span>{formatDuration(duration)}</span>}
		</>
	);
}

/** 单元格标识：多格时 notebook 式的 `In [1]` / `Out [1]`，输入段与输出段据此对照。 */
function Prompt({ kind, index }: { kind: "In" | "Out"; index: number }) {
	return (
		<span className="tv-nb-prompt">
			{kind} [{index + 1}]
		</span>
	);
}

function Input({ name, args, result, running }: ToolRenderProps): ReactNode {
	const { t } = useTranslation();
	// 只列模型给出的单元格；参数里没有、从结果 details 恢复的代码放在输出段
	const cells = cellsFromArgs(args, name);

	if (cells.length === 0) {
		// 参数还在流式生成（运行中、尚无结果）时只是还没到，不算参数错误
		const badArgs =
			!running &&
			result !== undefined &&
			!Array.isArray(args.cells) &&
			str(args.input) === null &&
			str(args.code) === null;
		return badArgs ? <InvalidArg what="cells" /> : null;
	}

	const multi = cells.length > 1;
	return (
		<div className="tv-nb">
			{cells.map((cell, i) => (
				<div className="tv-nb-cell" key={`c${i}`}>
					<div className="tv-nb-head">
						{multi && <Prompt kind="In" index={i} />}
						<Badge>{LANG_NAME[cell.lang] ?? cell.lang}</Badge>
						{cell.title && <span className="tv-nb-title">{cell.title}</span>}
						{(cell.timeout || cell.reset) && (
							<span className="tv-nb-end">
								{cell.reset && <span>{t("toolRender.eval.reset")}</span>}
								{cell.timeout && (
									<span>
										{t("toolRender.eval.timeout", { value: cell.timeout })}
									</span>
								)}
							</span>
						)}
					</div>
					{cell.code ? (
						<CodeBlock
							code={cell.code}
							lang={HLJS_LANG[cell.lang] ?? null}
							maxLines={16}
						/>
					) : (
						<span className="tv-nb-empty">…</span>
					)}
				</div>
			))}
		</div>
	);
}

function OutCell({
	cell,
	detail,
	index,
	state,
	multi,
	showCode,
}: {
	cell: EvalCell;
	detail: DetailCell;
	index: number;
	state: CellState;
	multi: boolean;
	showCode: boolean;
}): ReactNode {
	const { t } = useTranslation();
	const live = state === "running";
	const hasOutput = detail.output.trim() !== "";
	// 单格时头部读数已给出耗时与错误，这里只在需要说明状态（运行中 / 未运行）或带代码时写头
	const showHead = multi || showCode || state === "skipped";
	const empty =
		!hasOutput &&
		detail.events.length === 0 &&
		(state === "ok" || state === "error");
	return (
		<div className={`tv-cell tv-nb-out tv-nb-out--${state}`}>
			{showHead && (
				<div className="tv-nb-head">
					{multi && <Prompt kind="Out" index={index} />}
					{showCode && <Badge>{LANG_NAME[cell.lang] ?? cell.lang}</Badge>}
					{multi && cell.title && (
						<span className="tv-nb-title">{cell.title}</span>
					)}
					<span className="tv-nb-end">
						{state === "running" && (
							<span className="tv-nb-state">
								<i className="tv-nb-pulse" aria-hidden="true" />
								{t("toolRender.eval.state.running")}
							</span>
						)}
						{(state === "pending" || state === "skipped") && (
							<span className="tv-nb-state">
								{t(`toolRender.eval.state.${state}`)}
							</span>
						)}
						{state === "error" && multi && (
							<span className="tv-nb-state">
								{errorLabel(detail) ?? t("toolRender.eval.failed")}
							</span>
						)}
						{multi && detail.durationMs !== null && (
							<span>{formatDuration(detail.durationMs)}</span>
						)}
					</span>
				</div>
			)}
			{showCode && cell.code !== "" && (
				<CodeBlock code={cell.code} lang={HLJS_LANG[cell.lang] ?? null} />
			)}
			<CellEvents cell={detail} running={live} />
			{hasOutput && <CellOutput cell={detail} />}
			{empty && (
				<span className="tv-nb-empty">{t("toolRender.eval.noOutput")}</span>
			)}
		</div>
	);
}

function Result({
	name,
	args,
	result,
	partial,
	running,
}: ToolRenderProps): ReactNode {
	const { t } = useTranslation();
	// 运行中没有最终结果时，用结构化部分结果（details.cells）逐格展示输出
	const current = result ?? partial;
	const details = detailsRecord(current);
	const detailCells = detailCellsOf(details);
	const fromArgs = cellsFromArgs(args, name).length > 0;
	const cells = renderCells(args, name, detailCells);
	const finished = result !== undefined && !running;

	if (detailCells.length === 0) {
		// 旧结果（没有 details.cells）：结果原文；结束且确实为空时给明确的空状态
		const text = resultTextOf(current).trim();
		const empty = finished && (text === "" || text === "(no output)");
		return (
			<>
				<ResultImages result={current} />
				{empty ? (
					<span className="tv-nb-empty">{t("toolRender.eval.noOutput")}</span>
				) : (
					<ResultText result={current} maxLines={12} />
				)}
			</>
		);
	}

	const notice = str(details?.notice);
	const trunc = truncationOf(details);
	const multi = Math.max(cells.length, detailCells.length) > 1;
	const outputs = detailCells.map((dc, i) => {
		const index = dc.index;
		const cell = cells[index] ??
			cells[i] ?? {
				lang: dc.lang ?? "py",
				title: dc.title,
				timeout: null,
				reset: false,
				code: dc.code,
			};
		return (
			<OutCell
				key={`c${index}`}
				cell={cell}
				detail={dc}
				index={index}
				state={cellState(dc, finished)}
				multi={multi}
				// 参数里没有单元格时（代码只在 details 里），代码随输出一起放在这里
				showCode={!fromArgs}
			/>
		);
	});

	return (
		<>
			{notice && <Note tone="warn">{notice}</Note>}
			<div className="tv-nb">{outputs}</div>
			<ResultImages result={current} />
			{trunc && (
				<span className="tv-nb-empty">
					{trunc.shown !== null && trunc.total !== null
						? t("toolRender.eval.truncatedLines", {
								shown: trunc.shown,
								total: trunc.total,
							})
						: t("toolRender.eval.truncated")}
				</span>
			)}
		</>
	);
}

export const evalRenderer: ToolRenderer = {
	Summary,
	Meta,
	Input,
	Result,
	livePartial: true,
};
