/**
 * `debug` — DAP 调试会话：launch / attach、断点、单步与继续、求值、调用栈、变量、反汇编、内存……
 * 结果形状见 oryn-core `tools/debug.ts`：details 为 `{ action, success, snapshot?, … }`，按 action 另带
 * stackFrames / threads / scopes / variables / evaluation / breakpoints / functionBreakpoints /
 * instructionBreakpoints / dataBreakpoints / dataBreakpointInfo / disassembly / memoryAddress + memoryData /
 * bytesWritten / modules / sources / sessions / output / customBody，以及继续、单步的 `state` 与 `timedOut`。
 * 展开体画成调试器面板：顶部一条会话状态栏（状态灯、停止原因、帧名、源码位置），其下按动作给出结构化内容。
 */
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import {
	Badge,
	Badges,
	CodeBlock,
	CommandLine,
	InvalidArg,
	Kv,
	KvGrid,
	MetaCount,
	Note,
	Output,
	PathText,
	ResultText,
} from "../parts";
import type { ToolRenderer, ToolRenderProps, ToolResultLike } from "../types";
import {
	detailsRecord,
	display,
	isRecord,
	languageFromPath,
	normalizeWs,
	num,
	resultTextOf,
	shortenPath,
	str,
	truncate,
} from "../util";

/** 有本地化名字的动作；其余（新动作）按原名显示。 */
const ACTIONS = [
	"launch",
	"attach",
	"set_breakpoint",
	"remove_breakpoint",
	"set_instruction_breakpoint",
	"remove_instruction_breakpoint",
	"data_breakpoint_info",
	"set_data_breakpoint",
	"remove_data_breakpoint",
	"continue",
	"step_over",
	"step_in",
	"step_out",
	"pause",
	"evaluate",
	"stack_trace",
	"threads",
	"scopes",
	"variables",
	"disassemble",
	"read_memory",
	"write_memory",
	"modules",
	"loaded_sources",
	"custom_request",
	"output",
	"terminate",
	"sessions",
] as const;

/** 推进程序执行的动作：读数给出这一步之后程序停在哪里 / 是否结束。 */
const CONTROL: Record<string, true> = {
	launch: true,
	attach: true,
	continue: true,
	step_over: true,
	step_in: true,
	step_out: true,
	pause: true,
	terminate: true,
};

const STATUSES = [
	"launching",
	"configuring",
	"stopped",
	"running",
	"terminated",
] as const;

/** Session snapshot (details.snapshot / sessions[]). */
interface Snapshot {
	id: string | null;
	adapter: string | null;
	status: string | null;
	program: string | null;
	stopReason: string | null;
	stopDescription: string | null;
	frameName: string | null;
	sourcePath: string | null;
	line: number | null;
	column: number | null;
	exitCode: number | null;
	threadId: number | null;
	ip: string | null;
	needsConfigurationDone: boolean;
}

function snapOf(value: unknown): Snapshot | null {
	if (!isRecord(value)) return null;
	const source = isRecord(value.source) ? value.source : null;
	return {
		id: str(value.id) ?? (num(value.id) !== null ? String(value.id) : null),
		adapter: str(value.adapter),
		status: str(value.status),
		program: str(value.program),
		stopReason: str(value.stopReason),
		stopDescription: str(value.stopDescription),
		frameName: str(value.frameName),
		sourcePath: source ? (str(source.path) ?? str(source.name)) : null,
		line: num(value.line),
		column: num(value.column),
		exitCode: num(value.exitCode),
		threadId: num(value.threadId),
		ip: str(value.instructionPointerReference),
		needsConfigurationDone: value.needsConfigurationDone === true,
	};
}

function recs(value: unknown): Record<string, unknown>[] {
	return Array.isArray(value) ? value.filter(isRecord) : [];
}

function actionOf(props: ToolRenderProps): string | null {
	return str(props.args.action) ?? str(detailsRecord(props.result)?.action);
}

function ActionLabel({ action }: { action: string }): ReactNode {
	const { t } = useTranslation();
	const known = ACTIONS.find((a) => a === action);
	return known
		? t(`toolRender.debug.action.${known}`)
		: action.replace(/_/g, " ");
}

/** `hub.go:118:2`：路径只留文件名（读数、状态栏里够认出来，完整路径在 title）。 */
function shortLoc(path: string, line: number | null, column?: number | null) {
	const base = path.split("/").pop() ?? path;
	return `${base}${line !== null ? `:${line}` : ""}${line !== null && column != null ? `:${column}` : ""}`;
}

type Light = "run" | "stop" | "ok" | "err" | "idle";

/** 状态灯：停住（需要你看）用强调色，运行中闪动，异常停止与非零退出为红。 */
function lightOf(snap: Snapshot): Light {
	switch (snap.status) {
		case "stopped":
			return snap.stopReason === "exception" ? "err" : "stop";
		case "terminated":
			return snap.exitCode !== null && snap.exitCode !== 0
				? "err"
				: snap.exitCode === 0
					? "ok"
					: "idle";
		case "running":
		case "launching":
		case "configuring":
			return "run";
		default:
			return "idle";
	}
}

function StatusLabel({ status }: { status: string | null }): ReactNode {
	const { t } = useTranslation();
	if (!status) return null;
	const known = STATUSES.find((s) => s === status);
	return known ? t(`toolRender.debug.status.${known}`) : status;
}

/** Mirrors the TUI's summarizeDebugCall target priority. */
function targetTextOf(args: Record<string, unknown>): string | null {
	return (
		str(args.function) ??
		str(args.expression) ??
		str(args.command) ??
		str(args.memory_reference) ??
		str(args.instruction_reference) ??
		str(args.data_id) ??
		str(args.name)
	);
}

function Summary(props: ToolRenderProps): ReactNode {
	const { args } = props;
	const action = actionOf(props);
	const program = str(args.program);
	const file = str(args.file);
	const line = num(args.line);
	const target = targetTextOf(args);
	return (
		<>
			{action ? (
				<Badge>
					<ActionLabel action={action} />
				</Badge>
			) : args.action !== undefined ? (
				<InvalidArg what="action" />
			) : null}
			{program !== null ? (
				<PathText path={program} />
			) : file !== null ? (
				<PathText path={file} from={line ?? undefined} />
			) : target !== null ? (
				<span>{truncate(normalizeWs(target), 80)}</span>
			) : null}
		</>
	);
}

/** 断点列表的总数与未验证数。 */
function breakpointCounts(
	details: Record<string, unknown>,
): { total: number; pending: number } | null {
	for (const key of [
		"breakpoints",
		"functionBreakpoints",
		"instructionBreakpoints",
		"dataBreakpoints",
	]) {
		if (!Array.isArray(details[key])) continue;
		const list = recs(details[key]);
		return {
			total: list.length,
			pending: list.filter((b) => b.verified !== true).length,
		};
	}
	return null;
}

/** 各结构化列表的计数读数：`[details 键, i18n 键]`。 */
const COUNTS = [
	["stackFrames", "frames"],
	["threads", "threads"],
	["scopes", "scopes"],
	["variables", "variables"],
	["disassembly", "instructions"],
	["modules", "modules"],
	["sources", "sources"],
	["sessions", "sessions"],
] as const;

/**
 * 头部读数：推进执行的动作给出这一步之后的落点（停在哪、已退出、仍在运行）；
 * 查询类动作给出条目数；求值给出类型；断点给出数量与未验证数。
 */
function Meta(props: ToolRenderProps): ReactNode {
	const { t } = useTranslation();
	const { result } = props;
	if (!result || result.isError) return null;
	const details = detailsRecord(result);
	if (!details) return null;
	const action = actionOf(props) ?? "";
	const snap = snapOf(details.snapshot);
	if (CONTROL[action] && snap) {
		if (details.timedOut === true)
			return (
				<span className="tv-meta-warn">
					{t("toolRender.debug.stillRunning")}
				</span>
			);
		const light = lightOf(snap);
		if (snap.status === "stopped") {
			const where = snap.sourcePath
				? shortLoc(snap.sourcePath, snap.line)
				: snap.frameName;
			return (
				<span className={light === "err" ? "tv-meta-err" : undefined}>
					{where
						? t("toolRender.debug.stoppedAt", { where })
						: t("toolRender.debug.status.stopped")}
				</span>
			);
		}
		if (snap.status === "terminated")
			return (
				<span
					className={
						light === "err"
							? "tv-meta-err"
							: light === "ok"
								? "tv-meta-ok"
								: undefined
					}
				>
					{snap.exitCode !== null
						? t("toolRender.debug.exited", { code: snap.exitCode })
						: t("toolRender.debug.status.terminated")}
				</span>
			);
		return (
			<span>
				<StatusLabel status={snap.status} />
			</span>
		);
	}
	const bps = breakpointCounts(details);
	if (bps)
		return (
			<>
				<span>
					{t("toolRender.debug.count.breakpoints", { count: bps.total })}
				</span>
				{bps.pending > 0 && (
					<span className="tv-meta-warn">
						{t("toolRender.debug.unverified", { count: bps.pending })}
					</span>
				)}
			</>
		);
	for (const [key, unit] of COUNTS) {
		if (Array.isArray(details[key]))
			return (
				<span>
					{t(`toolRender.debug.count.${unit}`, {
						count: (details[key] as unknown[]).length,
					})}
				</span>
			);
	}
	const evaluation = isRecord(details.evaluation) ? details.evaluation : null;
	if (evaluation) {
		const type = str(evaluation.type);
		return type ? <span className="tv-dbg-type">{type}</span> : null;
	}
	const written = num(details.bytesWritten);
	if (written !== null)
		return (
			<span>{t("toolRender.debug.count.written", { count: written })}</span>
		);
	const memory = str(details.memoryData);
	if (memory !== null)
		return (
			<span>
				{t("toolRender.debug.count.bytes", {
					count: Math.floor((memory.replace(/=+$/, "").length * 3) / 4),
				})}
			</span>
		);
	const output = str(details.output);
	if (output)
		return (
			<MetaCount unit="lines" count={output.trimEnd().split("\n").length} />
		);
	return null;
}

/** 输入段原生展示之外、以 `key=value` 小标列出的参数（按显示顺序）。 */
const CHIP_ARGS = [
	"adapter",
	"cwd",
	"pid",
	"host",
	"port",
	"context",
	"frame_id",
	"scope_id",
	"variable_ref",
	"levels",
	"memory_reference",
	"instruction_reference",
	"instruction_count",
	"instruction_offset",
	"offset",
	"count",
	"data",
	"data_id",
	"name",
	"access_type",
	"hit_condition",
	"resolve_symbols",
	"allow_partial",
	"start_module",
	"module_count",
	"timeout",
];

/** 表达式按被调试源码的语言高亮：依次试断点文件、停住位置的源文件、被调试程序，取第一个认得出的。 */
function exprLangOf(paths: ReadonlyArray<string | null | undefined>) {
	for (const path of paths) {
		const lang = path ? languageFromPath(path) : null;
		if (lang) return lang;
	}
	return null;
}

function Input(props: ToolRenderProps): ReactNode {
	const { args, result } = props;
	const program = str(args.program);
	const file = str(args.file);
	const line = num(args.line);
	const fn = str(args.function);
	const condition = str(args.condition);
	const expression = str(args.expression);
	const command = str(args.command);
	const programArgs = Array.isArray(args.args)
		? args.args.filter((a): a is string => typeof a === "string")
		: [];
	const snap = snapOf(detailsRecord(result)?.snapshot);

	let customArgsJson = "";
	if (isRecord(args.arguments)) {
		try {
			customArgsJson = JSON.stringify(args.arguments, null, 2) ?? "";
		} catch {
			customArgsJson = "";
		}
	}
	const chips = CHIP_ARGS.flatMap((key) => {
		const value = args[key];
		if (
			typeof value !== "string" &&
			typeof value !== "number" &&
			typeof value !== "boolean"
		)
			return [];
		return [`${key}=${truncate(display(value), 80)}`];
	});

	return (
		<>
			{/* 折叠行里的 program / file:line 只是摘要（会被截断），这里始终给出全文 */}
			{program !== null && (
				<CommandLine
					command={[shortenPath(program), ...programArgs].join(" ")}
				/>
			)}
			{(file !== null || fn !== null || (line !== null && file === null)) && (
				<div className="tv-dbg-where">
					{file !== null ? (
						<PathText path={file} from={line ?? undefined} />
					) : line !== null ? (
						<span className="tv-lines">:{line}</span>
					) : null}
					{fn !== null && <span className="tv-dbg-fn">{fn}()</span>}
					{condition !== null && (
						<span className="tv-dbg-cond">
							<span className="tv-dbg-kw">if</span> {condition}
						</span>
					)}
				</div>
			)}
			{expression !== null && (
				<CodeBlock
					code={expression}
					lang={exprLangOf([file, snap?.sourcePath, program, snap?.program])}
					maxLines={8}
				/>
			)}
			{command !== null && <Badge>{command}</Badge>}
			{customArgsJson && (
				<CodeBlock code={customArgsJson} lang="json" maxLines={10} />
			)}
			<Badges items={chips} />
		</>
	);
}

// ---- 输出段：会话状态栏 + 按动作的结构化面板 ----

/** 会话状态栏；`program` 为参数里没给出的被调试程序（参数给了时输入段已写全，这里不重复）。 */
function SessionBar({
	snap,
	program,
}: {
	snap: Snapshot;
	program: string | null;
}): ReactNode {
	const light = lightOf(snap);
	return (
		<div className={`tv-dbg-bar tv-dbg-bar--${light}`}>
			<span className={`tv-dbg-dot tv-dbg-dot--${light}`} aria-hidden="true" />
			<span className="tv-dbg-status">
				<StatusLabel status={snap.status} />
			</span>
			{snap.stopReason && <Badge>{snap.stopReason}</Badge>}
			{snap.frameName && <span className="tv-dbg-frame">{snap.frameName}</span>}
			{snap.sourcePath && (
				<span className="tv-dbg-loc" title={snap.sourcePath}>
					{shortLoc(snap.sourcePath, snap.line, snap.column)}
				</span>
			)}
			<span className="tv-dbg-session" title={program ?? undefined}>
				{[
					program && shortenPath(program),
					snap.adapter,
					snap.id && `#${snap.id}`,
				]
					.filter(Boolean)
					.join(" · ")}
			</span>
		</div>
	);
}

/** 列表最多先显示的行数。 */
const MAX_ROWS = 40;

function More({ total }: { total: number }): ReactNode {
	const { t } = useTranslation();
	if (total <= MAX_ROWS) return null;
	return (
		<div className="tv-dbg-more">
			{t("toolRender.debug.more", { count: total - MAX_ROWS })}
		</div>
	);
}

function Frames({ frames }: { frames: Record<string, unknown>[] }): ReactNode {
	return (
		<>
			<ol className="tv-dbg-list">
				{frames.slice(0, MAX_ROWS).map((f, i) => {
					const source = isRecord(f.source) ? f.source : null;
					const path = source ? (str(source.path) ?? str(source.name)) : null;
					const subtle = str(f.presentationHint) === "subtle";
					return (
						<li
							key={`${num(f.id) ?? i}`}
							className={`tv-dbg-row${i === 0 ? " tv-dbg-row--current" : ""}${subtle ? " tv-dbg-row--subtle" : ""}`}
						>
							<span className="tv-dbg-idx">#{i}</span>
							<span className="tv-dbg-frame">{str(f.name) ?? "?"}</span>
							{path && (
								<span className="tv-dbg-loc" title={path}>
									{shortLoc(path, num(f.line), num(f.column))}
								</span>
							)}
						</li>
					);
				})}
			</ol>
			<More total={frames.length} />
		</>
	);
}

function Variables({ vars }: { vars: Record<string, unknown>[] }): ReactNode {
	const { t } = useTranslation();
	return (
		<>
			<table className="tv-dbg-vars">
				<tbody>
					{vars.slice(0, MAX_ROWS).map((v, i) => {
						const ref = num(v.variablesReference) ?? 0;
						const value = str(v.value) ?? display(v.value);
						return (
							<tr key={`${str(v.name) ?? ""}${i}`}>
								<td className="tv-dbg-var-name">
									<span
										className="tv-dbg-expand"
										aria-hidden="true"
										data-on={ref > 0 ? "" : undefined}
									>
										▸
									</span>
									{str(v.name) ?? "?"}
								</td>
								<td
									className="tv-dbg-var-value"
									title={ref > 0 ? t("toolRender.debug.ref", { ref }) : value}
								>
									{value}
								</td>
								<td className="tv-dbg-type">{str(v.type)}</td>
							</tr>
						);
					})}
				</tbody>
			</table>
			<More total={vars.length} />
		</>
	);
}

function Scopes({ scopes }: { scopes: Record<string, unknown>[] }): ReactNode {
	const { t } = useTranslation();
	return (
		<ul className="tv-dbg-list">
			{scopes.map((s, i) => (
				<li key={`${str(s.name) ?? ""}${i}`} className="tv-dbg-row">
					<span className="tv-dbg-frame">{str(s.name) ?? "?"}</span>
					<Badges
						items={[
							str(s.presentationHint),
							s.expensive === true && t("toolRender.debug.expensive"),
						]}
					/>
					<span className="tv-dbg-loc">
						{t("toolRender.debug.ref", { ref: num(s.variablesReference) ?? 0 })}
					</span>
				</li>
			))}
		</ul>
	);
}

function Threads({
	threads,
	current,
}: {
	threads: Record<string, unknown>[];
	current: number | null;
}): ReactNode {
	return (
		<>
			<ul className="tv-dbg-list">
				{threads.slice(0, MAX_ROWS).map((th, i) => {
					const id = num(th.id);
					return (
						<li
							key={`${id ?? i}`}
							className={`tv-dbg-row${id !== null && id === current ? " tv-dbg-row--current" : ""}`}
						>
							<span className="tv-dbg-idx">{id ?? "?"}</span>
							<span className="tv-dbg-frame">{str(th.name)}</span>
						</li>
					);
				})}
			</ul>
			<More total={threads.length} />
		</>
	);
}

/** 一个断点的位置文字：行号 / 函数名 / 指令地址 + 偏移 / 数据 id。 */
function breakpointWhere(b: Record<string, unknown>): string {
	const line = num(b.line);
	if (line !== null) return `L${line}`;
	const name = str(b.name);
	if (name) return `${name}()`;
	const ref = str(b.instructionReference);
	if (ref) {
		const offset = num(b.offset);
		return offset ? `${ref}+${offset}` : ref;
	}
	return str(b.dataId) ?? "?";
}

function Breakpoints({
	list,
	file,
}: {
	list: Record<string, unknown>[];
	file: string | null;
}): ReactNode {
	const { t } = useTranslation();
	return (
		<>
			{file && (
				<div className="tv-dbg-where">
					<PathText path={file} />
				</div>
			)}
			{list.length === 0 ? (
				<span className="tv-dbg-empty">
					{t("toolRender.debug.noBreakpoints")}
				</span>
			) : (
				<ul className="tv-dbg-list">
					{list.map((b, i) => {
						const verified = b.verified === true;
						const condition = str(b.condition);
						const hit = str(b.hitCondition);
						const access = str(b.accessType);
						return (
							<li key={`${num(b.id) ?? i}`} className="tv-dbg-row">
								<span
									className={`tv-dbg-bp${verified ? " tv-dbg-bp--on" : ""}`}
									title={t(
										verified
											? "toolRender.debug.verified"
											: "toolRender.debug.pending",
									)}
									aria-hidden="true"
								/>
								<span className="tv-dbg-frame">{breakpointWhere(b)}</span>
								{access && <Badge>{access}</Badge>}
								{condition && (
									<span className="tv-dbg-cond">
										<span className="tv-dbg-kw">if</span> {condition}
									</span>
								)}
								{hit && (
									<span className="tv-dbg-cond">
										<span className="tv-dbg-kw">hit</span> {hit}
									</span>
								)}
								{!verified && (
									<span className="tv-dbg-loc">
										{str(b.message) ?? t("toolRender.debug.pending")}
									</span>
								)}
							</li>
						);
					})}
				</ul>
			)}
		</>
	);
}

function Disassembly({
	rows,
	ip,
}: {
	rows: Record<string, unknown>[];
	ip: string | null;
}): ReactNode {
	return (
		<>
			<div className="tv-dbg-scroll">
				<table className="tv-dbg-asm">
					<tbody>
						{rows.slice(0, MAX_ROWS).map((r, i) => {
							const address = str(r.address) ?? "";
							return (
								<tr
									key={`${address}${i}`}
									className={
										address && address === ip ? "tv-dbg-asm--ip" : undefined
									}
								>
									<td className="tv-dbg-addr">{address}</td>
									<td className="tv-dbg-bytes">{str(r.instructionBytes)}</td>
									<td className="tv-dbg-ins">{str(r.instruction)}</td>
									<td className="tv-dbg-sym">
										{str(r.symbol) && `<${str(r.symbol)}>`}
									</td>
								</tr>
							);
						})}
					</tbody>
				</table>
			</div>
			<More total={rows.length} />
		</>
	);
}

/** base64 → 字节；不是合法 base64 时为 null。 */
function decodeBase64(data: string): Uint8Array | null {
	try {
		const bin = atob(data);
		const out = new Uint8Array(bin.length);
		for (let i = 0; i < bin.length; i++) out[i] = bin.charCodeAt(i);
		return out;
	} catch {
		return null;
	}
}

/** 十六进制转储：每行 16 字节，`地址  hex…  ascii`。 */
function hexDump(bytes: Uint8Array, address: string | null): string {
	let base: bigint | null = null;
	try {
		base = address ? BigInt(address) : null;
	} catch {
		base = null;
	}
	const lines: string[] = [];
	for (let at = 0; at < bytes.length; at += 16) {
		const chunk = bytes.subarray(at, at + 16);
		const hex = [...chunk]
			.map((b) => b.toString(16).padStart(2, "0"))
			.join(" ")
			.padEnd(47, " ");
		const ascii = [...chunk]
			.map((b) => (b >= 0x20 && b < 0x7f ? String.fromCharCode(b) : "."))
			.join("");
		const addr =
			base !== null
				? `0x${(base + BigInt(at)).toString(16).padStart(8, "0")}`
				: `+${at.toString(16).padStart(4, "0")}`;
		lines.push(`${addr}  ${hex}  ${ascii}`);
	}
	return lines.join("\n");
}

function Memory({ details }: { details: Record<string, unknown> }): ReactNode {
	const { t } = useTranslation();
	const data = str(details.memoryData) ?? "";
	const bytes = decodeBase64(data);
	const unreadable = num(details.unreadableBytes);
	return (
		<>
			{bytes && bytes.length > 0 ? (
				<Output
					text={hexDump(bytes, str(details.memoryAddress))}
					variant="code"
					maxLines={16}
				/>
			) : (
				<span className="tv-dbg-empty">{t("toolRender.debug.noData")}</span>
			)}
			{unreadable !== null && unreadable > 0 && (
				<Note tone="warn">
					{t("toolRender.debug.unreadable", { count: unreadable })}
				</Note>
			)}
		</>
	);
}

function Modules({ mods }: { mods: Record<string, unknown>[] }): ReactNode {
	const { t } = useTranslation();
	return (
		<>
			<ul className="tv-dbg-list">
				{mods.slice(0, MAX_ROWS).map((m, i) => {
					const path = str(m.path);
					return (
						<li key={`${display(m.id)}${i}`} className="tv-dbg-row">
							<span className="tv-dbg-frame">{str(m.name) ?? "?"}</span>
							<Badges
								items={[
									str(m.version),
									m.isUserCode === true && t("toolRender.debug.userCode"),
									m.isOptimized === true && t("toolRender.debug.optimized"),
									str(m.symbolStatus),
								]}
							/>
							{path && (
								<span className="tv-dbg-loc" title={path}>
									{shortenPath(path)}
								</span>
							)}
						</li>
					);
				})}
			</ul>
			<More total={mods.length} />
		</>
	);
}

function Sources({
	sources,
}: {
	sources: Record<string, unknown>[];
}): ReactNode {
	return (
		<>
			<ul className="tv-dbg-list">
				{sources.slice(0, MAX_ROWS).map((s, i) => {
					const path = str(s.path) ?? str(s.name) ?? "?";
					return (
						<li key={`${path}${i}`} className="tv-dbg-row">
							<PathText path={path} />
						</li>
					);
				})}
			</ul>
			<More total={sources.length} />
		</>
	);
}

function Sessions({ list }: { list: Record<string, unknown>[] }): ReactNode {
	const { t } = useTranslation();
	if (list.length === 0)
		return (
			<span className="tv-dbg-empty">{t("toolRender.debug.noSessions")}</span>
		);
	return (
		<ul className="tv-dbg-list">
			{list.map((raw, i) => {
				const s = snapOf(raw);
				if (!s) return null;
				const light = lightOf(s);
				return (
					<li key={`${s.id ?? i}`} className="tv-dbg-row">
						<span
							className={`tv-dbg-dot tv-dbg-dot--${light}`}
							aria-hidden="true"
						/>
						<span className="tv-dbg-idx">#{s.id}</span>
						<span className="tv-dbg-status">
							<StatusLabel status={s.status} />
						</span>
						{s.program && <PathText path={s.program} />}
						<span className="tv-dbg-loc">
							{[s.adapter, s.sourcePath && shortLoc(s.sourcePath, s.line)]
								.filter(Boolean)
								.join(" · ")}
						</span>
					</li>
				);
			})}
		</ul>
	);
}

/** `custom_request` 的结果正文：`<command> response:` 一行加 JSON 序列化的响应体；解析不了时为 null。 */
function customResponseOf(
	result: ToolResultLike | undefined,
): { heading: string; json: string } | null {
	if (!result || result.isError) return null;
	const text = resultTextOf(result).trim();
	const nl = text.indexOf("\n");
	if (nl < 0) return null;
	const json = text.slice(nl + 1);
	try {
		JSON.parse(json);
	} catch {
		return null;
	}
	return { heading: text.slice(0, nl), json };
}

/** 按动作给出的结构化内容；details 里没有可展示的结构时返回 null（交给结果原文）。 */
function Payload({
	details,
	args,
	snap,
	result,
}: {
	details: Record<string, unknown>;
	args: Record<string, unknown>;
	snap: Snapshot | null;
	result: ToolResultLike | undefined;
}): ReactNode {
	const { t } = useTranslation();
	if (Array.isArray(details.stackFrames))
		return <Frames frames={recs(details.stackFrames)} />;
	if (Array.isArray(details.variables))
		return recs(details.variables).length > 0 ? (
			<Variables vars={recs(details.variables)} />
		) : (
			<span className="tv-dbg-empty">{t("toolRender.debug.noVariables")}</span>
		);
	if (Array.isArray(details.scopes))
		return <Scopes scopes={recs(details.scopes)} />;
	if (Array.isArray(details.threads))
		return (
			<Threads
				threads={recs(details.threads)}
				current={snap?.threadId ?? null}
			/>
		);
	for (const key of [
		"breakpoints",
		"functionBreakpoints",
		"instructionBreakpoints",
		"dataBreakpoints",
	])
		if (Array.isArray(details[key]))
			return (
				<Breakpoints
					list={recs(details[key])}
					file={key === "breakpoints" ? str(args.file) : null}
				/>
			);
	if (Array.isArray(details.disassembly))
		return (
			<Disassembly rows={recs(details.disassembly)} ip={snap?.ip ?? null} />
		);
	if (str(details.memoryData) !== null) return <Memory details={details} />;
	if (Array.isArray(details.modules))
		return <Modules mods={recs(details.modules)} />;
	if (Array.isArray(details.sources))
		return <Sources sources={recs(details.sources)} />;
	if (Array.isArray(details.sessions))
		return <Sessions list={recs(details.sessions)} />;
	if (isRecord(details.evaluation)) {
		const ev = details.evaluation;
		const ref = num(ev.variablesReference) ?? 0;
		return (
			<>
				<CodeBlock
					code={str(ev.result) ?? display(ev.result)}
					lang={exprLangOf([str(args.file), snap?.sourcePath, snap?.program])}
					maxLines={12}
				/>
				<Badges
					items={[
						str(ev.type) && `type ${str(ev.type)}`,
						ref > 0 && t("toolRender.debug.ref", { ref }),
					]}
				/>
			</>
		);
	}
	if (isRecord(details.dataBreakpointInfo)) {
		const info = details.dataBreakpointInfo;
		const access = Array.isArray(info.accessTypes)
			? info.accessTypes.filter((a) => typeof a === "string").join(", ")
			: null;
		return (
			<KvGrid>
				<Kv k={t("toolRender.debug.description")}>{str(info.description)}</Kv>
				<Kv k="data id">
					{str(info.dataId) ?? t("toolRender.debug.unavailable")}
				</Kv>
				<Kv k={t("toolRender.debug.access")}>{access}</Kv>
			</KvGrid>
		);
	}
	if (typeof details.output === "string")
		return details.output ? (
			<Output text={details.output} variant="code" maxLines={16} />
		) : (
			<span className="tv-dbg-empty">{t("toolRender.debug.noOutput")}</span>
		);
	if ("customBody" in details) {
		const custom = customResponseOf(result);
		return custom ? (
			<Output
				text={custom.json}
				lang="json"
				variant="code"
				title={custom.heading}
				maxLines={12}
			/>
		) : null;
	}
	return null;
}

function Result(props: ToolRenderProps): ReactNode {
	const { t } = useTranslation();
	const { args, result } = props;
	if (!result) return null;
	if (result.isError) {
		const text = resultTextOf(result).trim();
		const nl = text.indexOf("\n");
		return (
			<>
				<Note tone="err">{nl < 0 ? text : text.slice(0, nl)}</Note>
				{nl >= 0 && (
					<Output text={text.slice(nl + 1)} variant="code" maxLines={10} />
				)}
			</>
		);
	}
	const details = detailsRecord(result);
	const snap = snapOf(details?.snapshot);
	const action = actionOf(props) ?? "";
	const payload = details ? (
		<Payload details={details} args={args} snap={snap} result={result} />
	) : null;
	// 结构化内容能完整代替结果原文时不再堆原文；推进执行的动作原文只是快照的复述
	const structured =
		details !== null &&
		(CONTROL[action] === true ||
			[
				"stackFrames",
				"variables",
				"scopes",
				"threads",
				"breakpoints",
				"functionBreakpoints",
				"instructionBreakpoints",
				"dataBreakpoints",
				"disassembly",
				"memoryData",
				"modules",
				"sources",
				"sessions",
				"evaluation",
				"dataBreakpointInfo",
				"output",
				"bytesWritten",
			].some((k) => k in details) ||
			("customBody" in details && customResponseOf(result) !== null));
	return (
		<>
			{snap && (
				<SessionBar
					snap={snap}
					program={
						snap.program && snap.program !== str(args.program)
							? snap.program
							: null
					}
				/>
			)}
			{snap?.stopDescription && snap.stopDescription !== snap.stopReason && (
				<span className="tv-dbg-desc">{snap.stopDescription}</span>
			)}
			{details?.timedOut === true && (
				<Note tone="warn">{t("toolRender.debug.timedOut")}</Note>
			)}
			{snap?.needsConfigurationDone && (
				<Note tone="warn">{t("toolRender.debug.configPending")}</Note>
			)}
			{payload}
			{!structured && <ResultText result={result} maxLines={12} />}
		</>
	);
}

export const debugRenderer: ToolRenderer = { Summary, Meta, Input, Result };
