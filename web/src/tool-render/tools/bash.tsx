/** `bash` — shell command execution: prompt line with env prefix, badges, output tail. */
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { formatDuration } from "../../lib/format";
import { revealTerminal } from "../../state/terminals";
import { Badge, Badges, InvalidArg, ResultImages, ResultText } from "../parts";
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

function Summary({ args, result, running }: ToolRenderProps): ReactNode {
	const { t } = useTranslation();
	const command = args.command === undefined ? "…" : str(args.command);
	if (command === null) return <InvalidArg what="command" />;
	const text = truncate(normalizeWs(command) || "…", 80);
	const waiting = running && args.pty === true && (
		<>
			{" "}
			<Badge tone="warn">{t("toolRender.terminal.awaitingInput")}</Badge>
		</>
	);
	return result?.isError ? (
		<span className="tv-err-text">{text}</span>
	) : (
		<span>
			{text}
			{waiting}
		</span>
	);
}

function Body({ args, result, running }: ToolRenderProps): ReactNode {
	const command = args.command === undefined ? "…" : str(args.command);
	const prefix = isRecord(args.env) ? envPrefix(args.env) : "";
	const cwd = str(args.cwd);
	const head = num(args.head);
	const tail = num(args.tail);

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
			{/* 折叠行只是摘要（会被截断、压成一行），展开体始终给出完整命令 */}
			<div className="tv-cmd">
				<span className="tv-cmd-prompt">$</span>
				<span className="tv-cmd-text">
					{prefix && <span className="tv-cmd-env">{`${prefix} `}</span>}
					{command ?? <InvalidArg what="command" />}
				</span>
			</div>
			<Badges
				items={[
					cwd && <Badge>cwd={shortenPath(cwd)}</Badge>,
					timeoutSeconds !== null && <Badge>timeout={timeoutSeconds}s</Badge>,
					args.pty === true && <PtyBadge running={running} />,
					!job && args.async === true && <Badge tone="accent">async</Badge>,
					head !== null && <Badge>head={head}</Badge>,
					tail !== null && <Badge>tail={tail}</Badge>,
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
				]}
			/>
			<ResultImages result={result} />
			<ResultText result={result} maxLines={12} />
			<Badges items={stats} />
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

/** 右侧读数：非零退出码；否则测试通过 / 失败数与耗时。 */
function Meta({ result }: ToolRenderProps): ReactNode {
	const { t } = useTranslation();
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

export const bashRenderer: ToolRenderer = { Summary, Meta, Body };
