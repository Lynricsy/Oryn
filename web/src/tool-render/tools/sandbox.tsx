/**
 * Agent 沙箱工具（M30，orb 扩展注册的 8 个 `sandbox_*`）共用的卡片。
 * 摘要：exec 是命令首行（`detach` 时带标记），cp 是 `source → dest`，create 是 `后端 · 网络`，其余是沙箱 id 末 6 位
 * （network / jobs / extend 另带目标网络、动作、TTL）。读数：exec 是退出码与耗时（`detach` 时是作业 id），
 * cp 是文件数与字节数（有跳过时另计），list 是台数。输入段：exec 是命令行加选项标记，其余是参数树；输出段是结果文本。
 * 运行中的输出尾巴由 ToolView 的通用逻辑显示（不设 `livePartial`）。
 */
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { formatBytes, formatDuration } from "../../lib/format";
import { ArgsTree } from "../generic";
import {
	Badge,
	Badges,
	CommandLine,
	InvalidArg,
	MetaCount,
	ResultText,
} from "../parts";
import type { ToolRenderer, ToolRenderProps } from "../types";
import {
	detailsRecord,
	isRecord,
	num,
	shortenPath,
	str,
	truncate,
} from "../util";

export const SANDBOX_TOOLS = [
	"sandbox_create",
	"sandbox_exec",
	"sandbox_cp",
	"sandbox_network",
	"sandbox_jobs",
	"sandbox_extend",
	"sandbox_list",
	"sandbox_destroy",
] as const;

/** 沙箱 id（`sbx_<ULID>`）的末 6 位；悬停看全 id。 */
function SandboxRef({ id }: { id: unknown }): ReactNode {
	const s = str(id);
	if (!s) return null;
	return (
		<span className="tv-path" title={s}>
			{s.slice(-6)}
		</span>
	);
}

function useNetworkLabel(): (v: unknown) => string | null {
	const { t } = useTranslation();
	return (v) => {
		const s = str(v);
		if (s === "none" || s === "egress")
			return t(`toolRender.sandbox.network.${s}`);
		return s;
	};
}

/** 命令的第一行非空行。 */
function firstLine(command: string): string {
	return (
		command
			.split("\n")
			.find((l) => l.trim() !== "")
			?.trim() ?? ""
	);
}

function Summary({ name, args, result }: ToolRenderProps): ReactNode {
	const { t } = useTranslation();
	const network = useNetworkLabel();
	const a = args ?? {};
	const d = detailsRecord(result);
	switch (name) {
		case "sandbox_exec": {
			if (a.command === undefined) return <span>…</span>;
			const command = str(a.command);
			if (command === null) return <InvalidArg what="command" />;
			const text = truncate(firstLine(command) || "…", 80);
			return (
				<>
					<span className={result?.isError ? "tv-err-text" : undefined}>
						{text}
					</span>
					<Badges
						items={[a.detach === true && <Badge tone="accent">detach</Badge>]}
					/>
				</>
			);
		}
		case "sandbox_cp": {
			const source = str(a.source);
			const dest = str(a.dest);
			if (!source && !dest) return null;
			return (
				<>
					<span className="tv-path">{shortenPath(source ?? "…")}</span>
					<span className="text-faint"> → </span>
					<span className="tv-path">{shortenPath(dest ?? "…")}</span>
				</>
			);
		}
		case "sandbox_create": {
			const sb = isRecord(d?.sandbox) ? d.sandbox : null;
			const backend =
				str(sb?.backend) ?? str(a.backend) ?? t("toolRender.sandbox.auto");
			const net = network(sb?.network ?? a.network);
			return (
				<span>
					{backend}
					{net && ` · ${net}`}
				</span>
			);
		}
		case "sandbox_list":
			return null;
		default: {
			const ttl = num(a.ttlMinutes);
			return (
				<>
					<SandboxRef id={a.sandbox} />
					<Badges
						items={[
							name === "sandbox_network" && network(a.network),
							name === "sandbox_jobs" && str(a.action),
							name === "sandbox_jobs" && str(a.job),
							name === "sandbox_extend" &&
								ttl !== null &&
								t("toolRender.sandbox.ttl", { n: ttl }),
						]}
					/>
				</>
			);
		}
	}
}

function Meta({ name, result }: ToolRenderProps): ReactNode {
	const { t } = useTranslation();
	const d = detailsRecord(result);
	if (!d) return null;
	switch (name) {
		case "sandbox_exec": {
			const jobId = str(d.jobId);
			if (jobId)
				return <span>{t("toolRender.sandbox.job", { id: jobId })}</span>;
			const exit = num(d.exit);
			const durationMs = num(d.durationMs);
			return (
				<>
					{exit !== null && (
						<span className={exit === 0 ? "tv-meta-ok" : "tv-meta-err"}>
							{t("toolRender.meta.exit", { code: exit })}
						</span>
					)}
					{durationMs !== null && <span>{formatDuration(durationMs)}</span>}
				</>
			);
		}
		case "sandbox_cp": {
			const bytes = num(d.bytes);
			const skipped = num(d.skipped);
			return (
				<>
					<MetaCount unit="files" count={num(d.files)} />
					{bytes !== null && <span>{formatBytes(bytes)}</span>}
					{skipped !== null && skipped > 0 && (
						<span className="tv-meta-warn">
							{t("toolRender.sandbox.skipped", { count: skipped })}
						</span>
					)}
				</>
			);
		}
		case "sandbox_list":
			return Array.isArray(d.sandboxes) ? (
				<span>
					{t("toolRender.sandbox.sandboxes", { count: d.sandboxes.length })}
				</span>
			) : null;
		default:
			return null;
	}
}

function Input({ name, args }: ToolRenderProps): ReactNode {
	const a = args ?? {};
	if (name !== "sandbox_exec") return <ArgsTree args={a} />;
	const command = str(a.command);
	const cwd = str(a.cwd);
	const timeout = num(a.timeoutSec);
	const env = isRecord(a.env) ? Object.keys(a.env) : [];
	return (
		<>
			{command !== null && <CommandLine command={command} />}
			<Badges
				items={[
					str(a.sandbox) && <Badge>{str(a.sandbox)}</Badge>,
					cwd && <Badge>cwd={shortenPath(cwd)}</Badge>,
					timeout !== null && <Badge>timeout={timeout}s</Badge>,
					a.detach === true && <Badge tone="accent">detach</Badge>,
					env.length > 0 && <Badge>env={env.join(",")}</Badge>,
				]}
			/>
		</>
	);
}

function Result({ name, result }: ToolRenderProps): ReactNode {
	return (
		<ResultText
			result={result}
			maxLines={name === "sandbox_exec" ? 16 : 10}
			variant={name === "sandbox_exec" ? "code" : "plain"}
		/>
	);
}

export const sandboxRenderer: ToolRenderer = { Summary, Meta, Input, Result };
