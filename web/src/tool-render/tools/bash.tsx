/** `bash` — shell command execution: prompt line (or omp's Pseudo view), arg badges, output tail. */
import { type ReactNode, useState } from "react";
import { useTranslation } from "react-i18next";
import { formatDuration } from "../../lib/format";
import { revealTerminal } from "../../state/terminals";
import {
	Badge,
	Badges,
	CommandLine,
	ExpandToggle,
	InvalidArg,
	ResultImages,
	ResultText,
} from "../parts";
import type { ToolRenderer, ToolRenderProps } from "../types";
import {
	detailsRecord,
	display,
	isRecord,
	normalizeWs,
	num,
	resultTextOf,
	shortenPath,
	str,
	truncate,
} from "../util";

/** Values safe to show unquoted in a `NAME=value` shell prefix. */
const SHELL_SAFE = /^[\w@%+=:,./-]+$/;

/** Footer appended by the bash tool when long output was spilled to an artifact. */
const ARTIFACT_NOTICE = /\[raw output: artifact:\/\/([\w-]+)\]/;

function envPrefix(env: Record<string, unknown>): string {
	const parts: string[] = [];
	for (const k in env) {
		const v = display(env[k]);
		parts.push(`${k}=${SHELL_SAFE.test(v) ? v : JSON.stringify(v)}`);
	}
	return parts.join(" ");
}

interface BashAsyncDetails {
	state: string;
	jobId: string | null;
}

function asyncDetailsOf(
	details: Record<string, unknown> | null,
): BashAsyncDetails | null {
	if (!details || !isRecord(details.async)) return null;
	const state = str(details.async.state);
	return state ? { state, jobId: str(details.async.jobId) } : null;
}

/** `pty:true` 的调用在 orb 的终端里运行，agent 等用户作答：运行中提示并可打开 Terminal 面板。 */
function PtyBadge({ running }: { running?: boolean }): ReactNode {
	const { t } = useTranslation();
	if (!running) return <Badge tone="accent">pty</Badge>;
	return (
		<button
			type="button"
			className="tv-badge tv-badge--warn tv-agent-link"
			onClick={() => revealTerminal()}
		>
			{t("toolRender.terminal.awaitingInput")} ·{" "}
			{t("toolRender.terminal.openTerminal")}
		</button>
	);
}

function Summary({ args, result }: ToolRenderProps): ReactNode {
	const command = args.command === undefined ? "…" : str(args.command);
	if (command === null) return <InvalidArg what="command" />;
	const text = truncate(normalizeWs(command) || "…", 80);
	return result?.isError ? (
		<span className="tv-err-text">{text}</span>
	) : (
		<span>{text}</span>
	);
}

// ---- Pseudo 视图 ----
// orb 的 omp 扩展（internal/orbdrt/assets/oryn-orb.ts）用 omp 原生 ShellVisualization 把执行轨迹渲染成
// Python 风格的行，写进 details.orynPseudo；这里只负责显示。运行中、旧会话、没有轨迹时只有原始命令。

interface PseudoSegment {
	text: string;
	style: string;
	state: string | null;
	failMark: boolean;
}

interface PseudoRow {
	depth: number;
	state: string | null;
	failMark: boolean;
	segments: PseudoSegment[];
	meta: string | null;
	annotations: { name: string; value: string; truncated: boolean }[];
	effects: string[];
}

interface Pseudo {
	rows: PseudoRow[];
	omittedRows: number;
	message: string | null;
}

/** 校验并归一 `details.orynPseudo`（线路上的普通 JSON，字段逐个收窄）；不可用时 null。 */
function pseudoOf(details: Record<string, unknown> | null): Pseudo | null {
	const raw = details?.orynPseudo;
	if (!isRecord(raw) || raw.v !== 1 || !Array.isArray(raw.rows)) return null;
	const rows: PseudoRow[] = [];
	for (const r of raw.rows) {
		if (!isRecord(r) || !Array.isArray(r.segments)) continue;
		rows.push({
			depth: Math.max(0, Math.min(num(r.depth) ?? 0, 12)),
			state: str(r.state),
			failMark: r.failMark === true,
			segments: r.segments.flatMap((s) =>
				isRecord(s) && typeof s.text === "string"
					? [
							{
								text: s.text,
								style: str(s.style) ?? "",
								state: str(s.state),
								failMark: s.failMark === true,
							},
						]
					: [],
			),
			meta: str(r.meta),
			annotations: Array.isArray(r.annotations)
				? r.annotations.flatMap((a) =>
						isRecord(a) &&
						typeof a.name === "string" &&
						typeof a.value === "string"
							? [
									{
										name: a.name,
										value: a.value,
										truncated: a.truncated === true,
									},
								]
							: [],
					)
				: [],
			effects: Array.isArray(r.effects)
				? r.effects.filter((e): e is string => typeof e === "string")
				: [],
		});
	}
	if (rows.length === 0) return null;
	return {
		rows,
		omittedRows: num(raw.omittedRows) ?? 0,
		message: str(raw.message),
	};
}

/**
 * 原生样式类（空格分隔，同 omp TUI 的 shell-viz-render）→ 卡片里的 hljs 令牌类，与代码块共用配色。
 * 优先级与 TUI 一致：comment > kw > var/secret > str/path > num > op > cmd/fn > chip/badge。
 */
function segmentClass(style: string): string {
	const parts = style.split(" ");
	if (parts.includes("comment")) return "hljs-comment";
	if (parts.includes("kw")) return "hljs-keyword";
	if (parts.includes("var") || parts.includes("secret")) return "hljs-variable";
	if (parts.includes("str") || parts.includes("path")) return "hljs-string";
	if (parts.includes("num")) return "hljs-number";
	if (parts.includes("op")) return "hljs-operator";
	if (parts.includes("cmd") || parts.includes("fn")) return "hljs-title";
	if (parts.includes("chip") || parts.includes("badge"))
		return "tv-pseudo-chip";
	return "";
}

/** 折叠时显示的 pseudo 行数。 */
const PSEUDO_MAX_ROWS = 40;

function PseudoView({ pseudo }: { pseudo: Pseudo }): ReactNode {
	const [expanded, setExpanded] = useState(false);
	const collapsible = pseudo.rows.length > PSEUDO_MAX_ROWS + 1;
	const rows =
		collapsible && !expanded
			? pseudo.rows.slice(0, PSEUDO_MAX_ROWS)
			: pseudo.rows;
	return (
		<div className="tv-out">
			{pseudo.message && <div className="tv-out-title">{pseudo.message}</div>}
			<div className="tv-pseudo" data-testid="bash-pseudo">
				{rows.map((row, i) => (
					<div
						key={i}
						className={`tv-pseudo-row${row.state === "skipped" ? " tv-pseudo-row--skipped" : ""}`}
						style={{ paddingInlineStart: `${row.depth * 2 + 2}ch` }}
					>
						<span className="tv-pseudo-lead" aria-hidden="true">
							{row.failMark ? "×" : ""}
						</span>
						{row.effects.length > 0 && (
							<span className="tv-pseudo-effects">{`${row.effects.join("")} `}</span>
						)}
						{row.segments.map((seg, j) => (
							<span
								key={j}
								className={[
									segmentClass(seg.style),
									seg.state === "skipped" ? "tv-pseudo-skipped" : "",
								]
									.filter(Boolean)
									.join(" ")}
							>
								{seg.failMark && <span className="tv-pseudo-fail">× </span>}
								{seg.text}
							</span>
						))}
						{row.meta && (
							<span
								className={`tv-pseudo-meta${row.state === "fail" ? " tv-pseudo-meta--fail" : ""}`}
							>
								{row.meta}
							</span>
						)}
						{row.annotations.length > 0 && (
							<span className="tv-pseudo-ann">
								{"‹ "}
								{row.annotations.map((a, k) => (
									<span key={k}>
										{k > 0 && " · "}
										<span className="hljs-variable">{a.name}</span>
										{" = "}
										<span className="hljs-string">
											{JSON.stringify(a.value)}
											{a.truncated && "…"}
										</span>
									</span>
								))}
								{" ›"}
							</span>
						)}
					</div>
				))}
				{!collapsible && pseudo.omittedRows > 0 && (
					<div className="tv-pseudo-row tv-pseudo-row--skipped">
						{`… +${pseudo.omittedRows}`}
					</div>
				)}
			</div>
			{collapsible && (
				<ExpandToggle
					expanded={expanded}
					hidden={pseudo.rows.length - PSEUDO_MAX_ROWS}
					onToggle={() => setExpanded((v) => !v)}
				/>
			)}
		</div>
	);
}

function Input({ args, result, running }: ToolRenderProps): ReactNode {
	const { t } = useTranslation();
	const [view, setView] = useState<"pseudo" | "raw">("pseudo");
	const command = args.command === undefined ? "…" : str(args.command);
	const prefix = isRecord(args.env) ? envPrefix(args.env) : "";
	const cwd = str(args.cwd);
	const head = num(args.head);
	const tail = num(args.tail);
	const details = detailsRecord(result);
	const timeoutSeconds = num(args.timeout) ?? num(details?.timeoutSeconds);
	const pseudo = pseudoOf(details);
	const showPseudo = pseudo !== null && view === "pseudo";
	return (
		<>
			{pseudo && (
				<fieldset
					className="tv-viewswitch"
					aria-label={t("toolRender.shellView.label")}
				>
					{(["pseudo", "raw"] as const).map((v) => (
						<button
							key={v}
							type="button"
							aria-pressed={view === v}
							onClick={() => setView(v)}
						>
							{t(`toolRender.shellView.${v}`)}
						</button>
					))}
				</fieldset>
			)}
			{/* 折叠行只是摘要（会被截断、压成一行），输入段始终给出完整命令 */}
			{showPseudo ? (
				<PseudoView pseudo={pseudo} />
			) : command === null ? (
				<InvalidArg what="command" />
			) : (
				<CommandLine command={command} prefix={prefix} />
			)}
			<Badges
				items={[
					cwd && <Badge>cwd={shortenPath(cwd)}</Badge>,
					timeoutSeconds !== null && <Badge>timeout={timeoutSeconds}s</Badge>,
					args.pty === true && <PtyBadge running={running} />,
					!asyncDetailsOf(details) && args.async === true && (
						<Badge tone="accent">async</Badge>
					),
					head !== null && <Badge>head={head}</Badge>,
					tail !== null && <Badge>tail={tail}</Badge>,
				]}
			/>
		</>
	);
}

function Result({ args, result }: ToolRenderProps): ReactNode {
	const details = detailsRecord(result);
	const timeoutSeconds = num(args.timeout) ?? num(details?.timeoutSeconds);
	const requestedTimeoutSeconds = num(details?.requestedTimeoutSeconds);
	const job = asyncDetailsOf(details);
	const artifactId = ARTIFACT_NOTICE.exec(resultTextOf(result))?.[1] ?? null;

	const stats: string[] = [];
	if (
		requestedTimeoutSeconds !== null &&
		requestedTimeoutSeconds !== timeoutSeconds
	) {
		stats.push(`requested timeout ${requestedTimeoutSeconds}s clamped`);
	}
	if (job?.jobId) stats.push(`job ${job.jobId}`);
	if (artifactId) stats.push(`artifact ${artifactId}`);

	return (
		<>
			<ResultImages result={result} />
			<ResultText result={result} maxLines={12} />
			<Badges
				items={[
					job && (
						<Badge
							tone={
								job.state === "failed"
									? "err"
									: job.state === "running"
										? "accent"
										: "ok"
							}
						>
							async {job.state}
						</Badge>
					),
					...stats,
				]}
			/>
		</>
	);
}

/** vitest（`Tests  1 failed | 5 passed`）或 bun test（`5 pass` / `1 fail`）的通过 / 失败数。 */
function testCounts(text: string): { passed: number; failed: number } | null {
	const vitest = /^\s*Tests\s+(.*)$/m.exec(text)?.[1];
	if (vitest) {
		const passed = /(\d+) passed/.exec(vitest);
		const failed = /(\d+) failed/.exec(vitest);
		if (passed || failed)
			return {
				passed: Number(passed?.[1] ?? 0),
				failed: Number(failed?.[1] ?? 0),
			};
	}
	const pass = /^\s*(\d+) pass$/m.exec(text);
	const fail = /^\s*(\d+) fail$/m.exec(text);
	if (pass || fail)
		return { passed: Number(pass?.[1] ?? 0), failed: Number(fail?.[1] ?? 0) };
	return null;
}

/** 右侧读数：运行中等待终端输入；非零退出码；否则测试通过 / 失败数与耗时。 */
function Meta({ args, result, running }: ToolRenderProps): ReactNode {
	const { t } = useTranslation();
	// 收起的卡片头部显示意图而不是命令，「等待输入」必须放在读数里才一直看得见
	if (running && args.pty === true)
		return (
			<span className="tv-meta-warn">
				{t("toolRender.terminal.awaitingInput")}
			</span>
		);
	const details = detailsRecord(result);
	const exitCode = num(details?.exitCode);
	const wallTimeMs = num(details?.wallTimeMs);
	if (exitCode !== null && exitCode !== 0)
		return (
			<span className="tv-meta-err">
				{t("toolRender.meta.exit", { code: exitCode })}
			</span>
		);
	const tests = result ? testCounts(resultTextOf(result)) : null;
	return (
		<>
			{tests && tests.failed > 0 && (
				<span className="tv-meta-err">
					{t("toolRender.meta.failed", { count: tests.failed })}
				</span>
			)}
			{tests && tests.passed > 0 && (
				<span className="tv-meta-ok">
					{t("toolRender.meta.passed", { count: tests.passed })}
				</span>
			)}
			{wallTimeMs !== null && <span>{formatDuration(wallTimeMs)}</span>}
		</>
	);
}

export const bashRenderer: ToolRenderer = { Summary, Meta, Input, Result };
