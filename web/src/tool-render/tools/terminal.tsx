/**
 * `terminal` — omp 的终端工具（`terminal_configure{agentTools:true}` 时注册）：list / read / run / stop / open / exec / send / wait。
 * 摘要按 `action` 给出命令或终端 id；正文列出 `details.terminal(s)` 与输出，并提供「在终端面板查看」。
 */
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { revealTerminal } from "../../state/terminals";
import type { Tone } from "../parts";
import { Badge, Badges, Kv, KvGrid, ResultText } from "../parts";
import type { ToolRenderer, ToolRenderProps } from "../types";
import {
	detailsRecord,
	isRecord,
	normalizeWs,
	num,
	str,
	truncate,
} from "../util";

interface TermInfo {
	terminalId: string;
	kind: string;
	title: string;
	cwd: string;
	running: boolean;
	exitCode: number | null;
	state: string | null;
	host: string | null;
}

function termInfo(value: unknown): TermInfo | null {
	if (!isRecord(value)) return null;
	const terminalId = str(value.terminalId);
	if (!terminalId) return null;
	return {
		terminalId,
		kind: str(value.kind) ?? "",
		title: str(value.title) ?? "",
		cwd: str(value.cwd) ?? "",
		running: value.running === true,
		exitCode: num(value.exitCode),
		state: str(value.state),
		host: str(value.host),
	};
}

function stateText(info: TermInfo): string {
	if (info.running) return info.state ?? "running";
	return info.exitCode === null ? "killed" : `exit ${info.exitCode}`;
}

/** 状态徽标色：运行中 accent，退出码 0 为 ok，其余（非零退出、被杀）为 err。 */
function stateTone(info: TermInfo): Tone {
	if (info.running) return "accent";
	return info.exitCode === 0 ? "ok" : "err";
}

function OpenInPanel({ id }: { id?: string }): ReactNode {
	const { t } = useTranslation();
	return (
		<button
			type="button"
			className="tv-badge tv-badge--accent tv-agent-link"
			onClick={() => revealTerminal(id)}
		>
			{t("toolRender.terminal.viewInPanel")}
			<span className="tv-agent-link-arrow" aria-hidden="true">
				{" ↗"}
			</span>
		</button>
	);
}

function Summary({ args, result }: ToolRenderProps): ReactNode {
	const action = str(args.action) ?? "…";
	const details = detailsRecord(result);
	const info = termInfo(details?.terminal);
	const id = str(args.id) ?? info?.terminalId ?? null;
	const command = str(args.command);
	const text =
		action === "list"
			? "list"
			: command
				? `${action} ${truncate(normalizeWs(command), 70)}`
				: action === "send"
					? `send ${id ?? ""} ${truncate(normalizeWs([str(args.text), str(args.key)].filter(Boolean).join(" + ")), 40)}`
					: `${action}${id ? ` ${id}` : args.host ? ` ssh ${String(args.host)}` : ""}`;
	return result?.isError ? (
		<span className="tv-err-text">{text}</span>
	) : (
		<span>{text}</span>
	);
}

function Body({ args, result }: ToolRenderProps): ReactNode {
	const details = detailsRecord(result);
	const info = termInfo(details?.terminal);
	const list = Array.isArray(details?.terminals)
		? details.terminals.map(termInfo).filter((x): x is TermInfo => x !== null)
		: [];
	const id = str(args.id) ?? info?.terminalId ?? undefined;
	const exitCode = num(details?.exitCode);
	const command = str(args.command);
	return (
		<>
			{command && (
				<div className="tv-cmd">
					<span className="tv-cmd-prompt">$</span>
					<span className="tv-cmd-text">{command}</span>
				</div>
			)}
			<Badges
				items={[
					<Badge key="action" tone="accent">
						{str(args.action) ?? "?"}
					</Badge>,
					id && <Badge>{id}</Badge>,
					info?.kind && <Badge>{info.kind}</Badge>,
					info && (
						<Badge
							tone={info.running ? "ok" : info.exitCode ? "err" : undefined}
						>
							{stateText(info)}
						</Badge>
					),
					info?.host && <Badge>ssh {info.host}</Badge>,
					exitCode !== null && (
						<Badge tone={exitCode === 0 ? "ok" : "err"}>exit {exitCode}</Badge>
					),
					(id || list.length > 0) && <OpenInPanel id={id} />,
				]}
			/>
			{list.length > 0 && (
				<KvGrid>
					{list.map((x) => (
						<Kv key={x.terminalId} k={x.terminalId}>
							<span className="tv-badges">
								<Badge>{x.kind}</Badge>
								<Badge tone={stateTone(x)}>{stateText(x)}</Badge>
							</span>{" "}
							{x.title || x.cwd}
						</Kv>
					))}
				</KvGrid>
			)}
			<ResultText result={result} maxLines={16} />
		</>
	);
}

export const terminalRenderer: ToolRenderer = { Summary, Body };
