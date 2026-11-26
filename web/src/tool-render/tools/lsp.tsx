/**
 * `lsp` — 语言服务器查询。按动作给出原生形态：诊断按文件分组、严重度图标着色；定义 / 引用 / 实现按文件
 * 列出位置并附上下文代码（位置所在行标为命中）；hover 按 Markdown 渲染；符号是缩进的大纲；代码操作是
 * 编号列表；重命名列出每个文件的改动数；status 列出各服务器状态。头部读数随动作：错误 / 警告数（无问题时
 * 绿色「无问题」）、位置数、符号数、操作数、改动文件数。结果形状见 oryn-core `tui/src/tools/lsp.ts`。
 */
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { FileTypeIcon } from "../../components/ui/FileTypeIcon";
import { Icon } from "../../components/ui/icons";
import {
	Badge,
	CodeLines,
	type CodeRow,
	DiagnosticList,
	EmptyNote,
	ErrorBlock,
	FileHead,
	FootNotes,
	InvalidArg,
	Kv,
	KvGrid,
	MarkdownBlock,
	Output,
	PathText,
	parseDiagnostics,
	SeverityCounts,
} from "../parts";
import type { ToolRenderer, ToolRenderProps, ToolResultLike } from "../types";
import {
	detailsRecord,
	languageFromPath,
	normalizeWs,
	num,
	resultTextOf,
	shortenPath,
	str,
	truncate,
} from "../util";

/** 结果是位置列表的动作。 */
const LOCATION_ACTIONS: Record<string, true> = {
	definition: true,
	references: true,
	type_definition: true,
	implementation: true,
};

function actionOf(
	args: Record<string, unknown>,
	result: ToolResultLike | undefined,
): string | null {
	const details = detailsRecord(result);
	return str(args.action) ?? (details ? str(details.action) : null);
}

// ---- 位置列表（definition / references / …） ----

/** 位置行 `  src/a.ts:12:5`；其后缩进更深的 `    11: text` 是上下文。 */
const LOC_RE = /^ {0,3}(\S.*?):(\d+):(\d+)$/;
const LOC_CTX_RE = /^\s{4,}(\d+): ?(.*)$/;

interface LocFile {
	path: string;
	rows: CodeRow[];
	/** 没附上下文的位置（`… N additional reference(s) shown without context` 之后）。 */
	bare: string[];
	count: number;
}

function parseLocations(text: string): { files: LocFile[]; notes: string[] } {
	const files: LocFile[] = [];
	const notes: string[] = [];
	let current: LocFile | null = null;
	let target = 0;
	for (const raw of text.split("\n")) {
		const line = raw.trimEnd();
		if (!line.trim() || /^Found \d+ /.test(line.trim())) continue;
		const loc = LOC_RE.exec(line);
		if (loc) {
			const path = loc[1] ?? "";
			const file: LocFile = files.find((f) => f.path === path) ?? {
				path,
				rows: [],
				bare: [],
				count: 0,
			};
			if (!files.includes(file)) files.push(file);
			file.count++;
			current = file;
			target = Number(loc[2]);
			file.bare.push(`${loc[2]}:${loc[3]}`);
			continue;
		}
		const ctx = LOC_CTX_RE.exec(line);
		if (ctx && current) {
			const no = Number(ctx[1]);
			// 有上下文的位置不再列成裸位置
			const last = current.bare[current.bare.length - 1];
			if (last?.startsWith(`${target}:`)) {
				current.bare.pop();
				if (current.rows.length > 0)
					current.rows.push({ kind: "gap", text: "" });
			}
			current.rows.push({
				kind: no === target ? "hit" : "ctx",
				no,
				text: ctx[2] ?? "",
			});
			continue;
		}
		notes.push(line.trim());
	}
	return { files, notes };
}

function Locations({ text }: { text: string }): ReactNode {
	const { t } = useTranslation();
	const { files, notes } = parseLocations(text);
	if (files.length === 0)
		return <EmptyNote>{text.trim() || t("toolRender.lsp.none")}</EmptyNote>;
	return (
		<>
			<div className="tv-fgroups">
				{files.map((f) => (
					<div key={f.path} className="tv-fgroup">
						<FileHead path={f.path}>
							<span>{t("toolRender.lsp.locations", { count: f.count })}</span>
						</FileHead>
						<CodeLines
							rows={f.rows}
							lang={languageFromPath(f.path)}
							maxLines={18}
						/>
						{f.bare.length > 0 && (
							<div className="tv-lsp-locs">
								{f.bare.map((b) => (
									<span key={b} className="tv-badge">
										{b}
									</span>
								))}
							</div>
						)}
					</div>
				))}
			</div>
			<FootNotes notes={notes.map((n) => n.replace(/^\.\.\. /, ""))} />
		</>
	);
}

// ---- 符号大纲 ----

/** `<icon> Foo detail @ line 3`、`<icon> Foo (Container) @ src/a.ts:3:7`；图标是主题字形，丢掉。 */
const SYMBOL_RE =
	/^(\s*)(?:\S{1,2} )?(.+?) @ (?:line (\d+)|(.+):(\d+):(\d+))(?: \(in (.+)\))?$/;

function Symbols({ text }: { text: string }): ReactNode {
	const { t } = useTranslation();
	const rows: {
		depth: number;
		name: string;
		where: string;
		container: string | null;
	}[] = [];
	const notes: string[] = [];
	let title: string | null = null;
	for (const raw of text.split("\n")) {
		const line = raw.trimEnd();
		if (!line.trim()) continue;
		const m = SYMBOL_RE.exec(line);
		if (m) {
			rows.push({
				depth: Math.floor((m[1]?.length ?? 0) / 2),
				name: m[2] ?? "",
				where: m[3] ? `L${m[3]}` : `${shortenPath(m[4] ?? "")}:${m[5]}`,
				container: m[7] ?? null,
			});
			continue;
		}
		if (title === null && rows.length === 0 && line.endsWith(":"))
			title = line.slice(0, -1);
		else notes.push(line.trim());
	}
	if (rows.length === 0)
		return <EmptyNote>{text.trim() || t("toolRender.lsp.none")}</EmptyNote>;
	return (
		<>
			{title && <div className="tv-out-title">{title}</div>}
			<div className="tv-lsp-syms">
				{rows.map((r, i) => (
					<div
						key={i}
						className="tv-lsp-sym"
						style={{ paddingLeft: `${r.depth * 16}px` }}
					>
						<span className="tv-lsp-sym-name">{r.name}</span>
						{r.container && <span className="tv-faint">{r.container}</span>}
						<span className="tv-lsp-sym-at">{r.where}</span>
					</div>
				))}
			</div>
			<FootNotes notes={notes} />
		</>
	);
}

// ---- 代码操作 ----

const ACTION_RE =
	/^\s*(\d+): \[([^\]]+)\] (.+?)(?: \((preferred|disabled: .+)\))?$/;

function CodeActions({ text }: { text: string }): ReactNode {
	const { t } = useTranslation();
	const items: {
		index: string;
		kind: string;
		title: string;
		flag: string | null;
	}[] = [];
	const notes: string[] = [];
	for (const raw of text.split("\n")) {
		if (!raw.trim() || /^\d+ code action\(s\):$/.test(raw.trim())) continue;
		const m = ACTION_RE.exec(raw);
		if (m)
			items.push({
				index: m[1] ?? "",
				kind: m[2] ?? "",
				title: m[3] ?? "",
				flag: m[4] ?? null,
			});
		else notes.push(raw.trim());
	}
	if (items.length === 0)
		return <Output text={text} maxLines={12} variant="plain" />;
	return (
		<>
			<ol className="tv-lsp-actions">
				{items.map((a) => (
					<li
						key={a.index}
						className={a.flag?.startsWith("disabled") ? "tv-faint" : undefined}
					>
						<span className="tv-lsp-action-idx">{a.index}</span>
						<span className="tv-lsp-action-title">{a.title}</span>
						<span className="tv-badge">{a.kind}</span>
						{a.flag === "preferred" && (
							<Badge tone="ok">{t("toolRender.lsp.preferred")}</Badge>
						)}
						{a.flag?.startsWith("disabled") && (
							<span className="tv-faint">{a.flag}</span>
						)}
					</li>
				))}
			</ol>
			<FootNotes notes={notes} />
		</>
	);
}

// ---- 工作区改动（rename、code action 的 apply） ----

const EDIT_LINE_RE = /^\s*(.+?): (\d+) edits?$/;
const FILE_OP_RE = /^\s*(CREATE|DELETE|RENAME): (.+)$/;

function WorkspaceEdits({ text }: { text: string }): ReactNode {
	const { t } = useTranslation();
	const rows: { path: string; aside: string }[] = [];
	const notes: string[] = [];
	for (const raw of text.split("\n")) {
		if (!raw.trim()) continue;
		const edit = EDIT_LINE_RE.exec(raw);
		if (edit) {
			rows.push({
				path: edit[1] ?? "",
				aside: t("toolRender.lsp.edits", { count: Number(edit[2]) }),
			});
			continue;
		}
		const op = FILE_OP_RE.exec(raw);
		if (op) {
			rows.push({ path: op[2] ?? "", aside: op[1]?.toLowerCase() ?? "" });
			continue;
		}
		notes.push(raw.trim());
	}
	if (rows.length === 0)
		return <Output text={text} maxLines={12} variant="plain" />;
	const [head, ...rest] = notes;
	return (
		<>
			{head && <div className="tv-out-title">{head.replace(/:$/, "")}</div>}
			<div className="tv-lsp-files">
				{rows.map((r, i) => (
					<div key={i} className="tv-lsp-file">
						<FileTypeIcon name={r.path} size={14} />
						<PathText path={r.path} />
						<span className="tv-lsp-file-aside">{r.aside}</span>
					</div>
				))}
			</div>
			<FootNotes notes={rest} />
		</>
	);
}

// ---- 服务器状态 ----

function ServerStatus({ text }: { text: string }): ReactNode {
	const lines = text.split("\n");
	const first = lines[0] ?? "";
	const m = /^Language servers: (.+)$/.exec(first.trim());
	if (!m) return <Output text={text} maxLines={12} variant="plain" />;
	const servers = (m[1] ?? "").split(/,\s+(?![^()]*\))/).map((s) => {
		const sm = /^(.+?) \((.+)\)$/.exec(s.trim());
		const state = sm?.[2] ?? "";
		return {
			name: sm?.[1] ?? s.trim(),
			state,
			tone: /^ready/.test(state)
				? "ok"
				: /unusable|failed|error/.test(state)
					? "err"
					: "idle",
		};
	});
	return (
		<>
			<div className="tv-lsp-servers">
				{servers.map((s) => (
					<div key={s.name} className="tv-lsp-server">
						<span
							className={`tv-lsp-dot tv-lsp-dot--${s.tone}`}
							aria-hidden="true"
						/>
						<span className="tv-lsp-server-name">{s.name}</span>
						<span className="tv-faint">{s.state}</span>
					</div>
				))}
			</div>
			<FootNotes
				notes={lines
					.slice(1)
					.map((l) => l.trim())
					.filter(Boolean)}
			/>
		</>
	);
}

// ---- 卡片 ----

/** 动作名转成可读的词（`type_definition` → `type definition`）。 */
function actionWord(action: string | null): string {
	return action ? action.replace(/_/g, " ") : "request";
}

function Summary({ args }: ToolRenderProps): ReactNode {
	const { t } = useTranslation();
	const file = str(args.file);
	const line = num(args.line);
	const symbol = str(args.symbol);
	const query = str(args.query);
	const newName = str(args.new_name);
	return (
		<>
			<span className="tv-badge">{actionWord(str(args.action))}</span>
			{file === "*" && (
				<span className="tv-muted">{t("toolRender.lsp.workspace")}</span>
			)}
			{file && file !== "*" && <PathText path={file} from={line} />}
			{symbol && (
				<span className="tv-pattern">{truncate(normalizeWs(symbol), 48)}</span>
			)}
			{query && (
				<span className="tv-muted">{truncate(normalizeWs(query), 48)}</span>
			)}
			{newName && (
				<span className="tv-muted">→ {truncate(normalizeWs(newName), 48)}</span>
			)}
		</>
	);
}

/** 头部读数：随动作给出最重要的一个数。 */
function Meta({ args, result }: ToolRenderProps): ReactNode {
	const { t } = useTranslation();
	if (!result || result.isError) return null;
	const action = actionOf(args, result);
	const text = resultTextOf(result);
	if (action === "diagnostics") {
		const { diags, status } = parseDiagnostics(text);
		if (diags.length > 0) return <SeverityCounts diags={diags} />;
		if (status.some((s) => s.tone !== "ok")) return null;
		return <span className="tv-meta-ok">{t("toolRender.lsp.clean")}</span>;
	}
	if (action && LOCATION_ACTIONS[action]) {
		const found = /^Found (\d+) /m.exec(text);
		const n = found ? Number(found[1]) : 0;
		return (
			<span className={n === 0 ? "tv-meta-warn" : undefined}>
				{t("toolRender.lsp.locations", { count: n })}
			</span>
		);
	}
	if (action === "symbols") {
		const n = text.split("\n").filter((l) => SYMBOL_RE.test(l)).length;
		return <span>{t("toolRender.lsp.symbols", { count: n })}</span>;
	}
	if (action === "code_actions") {
		const m = /^(\d+) code action/m.exec(text);
		return m ? (
			<span>{t("toolRender.lsp.actions", { count: Number(m[1]) })}</span>
		) : null;
	}
	if (action === "rename") {
		const n = text.split("\n").filter((l) => EDIT_LINE_RE.test(l)).length;
		return n > 0 ? (
			<span>{t("toolRender.meta.files", { count: n })}</span>
		) : null;
	}
	return null;
}

/** Kv row for an optional arg: hidden when absent, InvalidArg when mistyped. */
function ArgKv({
	k,
	raw,
	val,
}: {
	k: string;
	raw: unknown;
	val: ReactNode;
}): ReactNode {
	if (raw === undefined) return null;
	return (
		<Kv k={k}>
			{val == null || val === false ? <InvalidArg what={k} /> : val}
		</Kv>
	);
}

function Input({ args }: ToolRenderProps): ReactNode {
	const { t } = useTranslation();
	const file = str(args.file);
	const line = num(args.line);
	const apply = typeof args.apply === "boolean" ? args.apply : null;
	const timeout = num(args.timeout);
	const payload = str(args.payload);
	const rows = [
		args.action !== undefined && str(args.action) === null && (
			<ArgKv
				key="action"
				k={t("toolRender.lsp.k.action")}
				raw={args.action}
				val={null}
			/>
		),
		<ArgKv
			key="file"
			k={t("toolRender.lsp.k.file")}
			raw={args.file}
			val={
				file === "*"
					? t("toolRender.lsp.workspace")
					: file && <PathText path={file} from={line} />
			}
		/>,
		!file && (
			<ArgKv
				key="line"
				k={t("toolRender.lsp.k.line")}
				raw={args.line}
				val={line}
			/>
		),
		<ArgKv
			key="symbol"
			k={t("toolRender.lsp.k.symbol")}
			raw={args.symbol}
			val={str(args.symbol)}
		/>,
		<ArgKv
			key="query"
			k={t("toolRender.lsp.k.query")}
			raw={args.query}
			val={str(args.query)}
		/>,
		<ArgKv
			key="new"
			k={t("toolRender.lsp.k.newName")}
			raw={args.new_name}
			val={str(args.new_name)}
		/>,
		<ArgKv
			key="apply"
			k={t("toolRender.lsp.k.apply")}
			raw={args.apply}
			val={
				apply === null
					? null
					: apply
						? t("toolRender.lsp.yes")
						: t("toolRender.lsp.no")
			}
		/>,
		<ArgKv
			key="timeout"
			k={t("toolRender.lsp.k.timeout")}
			raw={args.timeout}
			val={timeout !== null && `${timeout}s`}
		/>,
		args.payload !== undefined && payload === null && (
			<Kv key="payload" k={t("toolRender.lsp.k.payload")}>
				<InvalidArg what="payload" />
			</Kv>
		),
	];
	const hasRows =
		[
			"file",
			"line",
			"symbol",
			"query",
			"new_name",
			"apply",
			"timeout",
			"payload",
		].some((k) => args[k] !== undefined) ||
		(args.action !== undefined && str(args.action) === null);
	return (
		<>
			{hasRows && <KvGrid>{rows}</KvGrid>}
			{payload && (
				<Output
					text={payload}
					lang="json"
					variant="code"
					maxLines={8}
					title={t("toolRender.lsp.k.payload")}
				/>
			)}
		</>
	);
}

/** `request` 的结果首行是 `<server> ← <method>:`，其后是 JSON 序列化的响应。 */
const REQUEST_HEAD_RE = /^(.+ ← .+):\n([\s\S]*)$/;

function Body({
	action,
	text,
}: {
	action: string | null;
	text: string;
}): ReactNode {
	const { t } = useTranslation();
	if (action === "diagnostics") {
		const { diags, status, notes } = parseDiagnostics(text);
		if (
			diags.length === 0 &&
			status.every((s) => s.tone === "ok") &&
			notes.length <= 1
		)
			return (
				<div className="tv-lsp-clean">
					<Icon name="checkCircle" size={14} />
					<span>{notes[0] ?? t("toolRender.lsp.cleanLong")}</span>
				</div>
			);
		return (
			<>
				<DiagnosticList diags={diags} status={status} />
				<FootNotes
					notes={notes.filter((n) => !/^Workspace diagnostics/.test(n))}
				/>
			</>
		);
	}
	if (action && LOCATION_ACTIONS[action]) return <Locations text={text} />;
	if (action === "hover")
		return /^No hover information/.test(text.trim()) ? (
			<EmptyNote>{text.trim()}</EmptyNote>
		) : (
			<MarkdownBlock text={text} />
		);
	if (action === "symbols") return <Symbols text={text} />;
	if (action === "code_actions") return <CodeActions text={text} />;
	if (action === "rename" || action === "rename_file")
		return <WorkspaceEdits text={text} />;
	if (action === "status") return <ServerStatus text={text} />;
	const request =
		action === "request" ? REQUEST_HEAD_RE.exec(text.trim()) : null;
	if (request) {
		const body = request[2] ?? "";
		return (
			<>
				<div className="tv-out-title">{request[1]}</div>
				<Output
					text={body}
					lang={/^[[{]|^(?:null|true|false|-?\d)/.test(body) ? "json" : null}
					variant="code"
					maxLines={14}
				/>
			</>
		);
	}
	return <Output text={text} maxLines={14} variant="plain" />;
}

function Result({ args, result }: ToolRenderProps): ReactNode {
	if (!result) return null;
	const text = resultTextOf(result);
	if (result.isError) return <ErrorBlock text={text} />;
	const details = detailsRecord(result);
	const server = details ? str(details.serverName) : null;
	const failed = details?.success === false;
	return (
		<>
			{failed ? (
				<ErrorBlock text={text} />
			) : (
				text.trim() && <Body action={actionOf(args, result)} text={text} />
			)}
			{server && (
				<div className="tv-lsp-by">
					<Icon name="cpu" size={12} />
					<span>{server}</span>
				</div>
			)}
		</>
	);
}

export const lspRenderer: ToolRenderer = { Summary, Meta, Input, Result };
