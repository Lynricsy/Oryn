/**
 * Terminal 标签（计划 M3 2.5）：子标签（标题、状态点、关闭、`+` 新建）+ 当前终端的 xterm 视图。
 * 挂载即「可见」：开启 `term.watch` 并经 list + snapshot 同步；卸载即关闭。
 * orb 未运行时显示遮罩 +「唤醒」；进程退出 / agent 重启导致会话终止时保留画面并提示。
 */
import { type ReactNode, useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import type { RpcTerminalInfo } from "../../omp/types";
import {
	type TerminalEntry,
	terminalHub,
	useTerminals,
} from "../../state/terminals";
import { useOrbOf } from "../thread/ThreadHeader";
import { Icon } from "../ui/icons";
import { Button, describeError, Spinner } from "../ui/primitives";
import { TerminalKeyBar, useCoarsePointer } from "./TerminalKeyBar";
import { TerminalView } from "./TerminalView";
import { createXterm } from "./xterm";

terminalHub.setTermFactory(createXterm);

const TRANSIENT_ORB_STATES: Record<string, true> = {
	requested: true,
	spawning: true,
	connecting: true,
	setting_up: true,
	resuming: true,
	waking: true,
	freezing: true,
	stopping: true,
	deleting: true,
};

function titleOf(info: RpcTerminalInfo): string {
	return info.title || info.kind;
}

function StatusDot({ entry }: { entry: TerminalEntry }) {
	const cls = entry.attention
		? "bg-warn animate-pulse"
		: entry.status === "live"
			? "bg-ok"
			: entry.status === "lost"
				? "bg-err"
				: "bg-faint";
	return <span className={`h-1.5 w-1.5 shrink-0 rounded-full ${cls}`} />;
}

function Offline({ threadId }: { threadId: string }) {
	const { t } = useTranslation();
	const orb = useOrbOf(threadId);
	const [busy, setBusy] = useState(false);
	const [error, setError] = useState<unknown>(null);
	const wake = async () => {
		setBusy(true);
		setError(null);
		try {
			await terminalHub.wake(threadId);
		} catch (err) {
			setError(err);
			setBusy(false);
		}
	};
	let body: ReactNode;
	const state = orb?.state ?? "deleted";
	if (!orb || state === "deleted") {
		body = <p className="text-muted text-sm">{t("terminal.orbGone")}</p>;
	} else if (busy || TRANSIENT_ORB_STATES[state]) {
		body = (
			<p className="flex items-center gap-2 text-muted text-sm">
				<Spinner />
				{t("terminal.orbStarting", { state: t(`orbState.${state}`) })}
			</p>
		);
	} else if (state === "ready") {
		body = (
			<p className="flex items-center gap-2 text-muted text-sm">
				<Spinner />
				{t("terminal.agentStarting")}
			</p>
		);
	} else {
		body = (
			<>
				<p className="font-medium text-sm">{t("terminal.offline")}</p>
				<p className="text-muted text-xs">
					{state === "failed" && orb.failureReason
						? t("terminal.orbFailed", { reason: orb.failureReason })
						: t("terminal.offlineHint", { state: t(`orbState.${state}`) })}
				</p>
				<Button variant="primary" icon="play" onClick={() => void wake()}>
					{t("terminal.wake")}
				</Button>
			</>
		);
	}
	return (
		<div
			data-testid="terminal-offline"
			className="absolute inset-0 z-10 flex flex-col items-center justify-center gap-2 bg-panel/85 p-6 text-center backdrop-blur-sm"
		>
			<Icon name="terminal" size={22} className="text-faint" />
			{body}
			{error != null && (
				<p className="text-err text-xs">{describeError(error, t)}</p>
			)}
		</div>
	);
}

function Notice({
	threadId,
	terminalId,
	entry,
}: {
	threadId: string;
	terminalId: string;
	entry: TerminalEntry;
}) {
	const { t } = useTranslation();
	if (entry.status === "live") return null;
	const text =
		entry.status === "lost"
			? t("terminal.lost")
			: entry.info.exitCode === undefined
				? t("terminal.killed")
				: t("terminal.exited", { code: entry.info.exitCode });
	return (
		<div
			data-testid="terminal-notice"
			className={`flex shrink-0 items-center gap-2 border-border border-b px-3 py-1.5 text-xs ${entry.status === "lost" ? "bg-err/10 text-err" : "bg-raised text-muted"}`}
		>
			<Icon name={entry.status === "lost" ? "alert" : "info"} size={13} />
			<span className="min-w-0 flex-1">{text}</span>
			<Button
				size="sm"
				variant="ghost"
				onClick={() => void terminalHub.close(threadId, terminalId)}
			>
				{t("terminal.dismiss")}
			</Button>
		</div>
	);
}

export default function TerminalPanel({
	threadId,
}: {
	threadId: string;
}): ReactNode {
	const { t } = useTranslation();
	const order = useTerminals(threadId, (s) => s.order);
	const terminals = useTerminals(threadId, (s) => s.terminals);
	const activeId = useTerminals(threadId, (s) => s.activeId);
	const offline = useTerminals(threadId, (s) => s.offline);
	const syncing = useTerminals(threadId, (s) => s.syncing);
	const error = useTerminals(threadId, (s) => s.error);
	const coarse = useCoarsePointer();

	useEffect(() => {
		terminalHub.setVisible(threadId, true);
		return () => terminalHub.setVisible(threadId, false);
	}, [threadId]);

	const active = activeId ? terminals[activeId] : undefined;

	return (
		<div
			data-testid="terminal-panel"
			className="flex h-full min-h-0 flex-col bg-panel"
			style={{
				paddingBottom: "max(env(safe-area-inset-bottom), var(--kb-inset))",
			}}
		>
			<div
				role="tablist"
				aria-label={t("terminal.title")}
				className="flex shrink-0 items-center gap-0.5 overflow-x-auto border-border border-b px-1.5 py-1"
			>
				{order.map((id) => {
					const e = terminals[id];
					if (!e) return null;
					const selected = id === activeId;
					return (
						<div
							key={id}
							className={`group flex h-7 max-w-48 shrink-0 items-center gap-1.5 rounded-md pr-0.5 pl-2 text-xs ${selected ? "bg-hover text-fg" : "text-muted hover:bg-hover/60"}`}
						>
							<button
								type="button"
								role="tab"
								aria-selected={selected}
								data-terminal-tab={id}
								title={`${titleOf(e.info)} · ${t(`terminal.kind.${e.info.kind}`)}${e.attention ? ` · ${t("terminal.needsInput")}` : ""}`}
								className="flex min-w-0 items-center gap-1.5"
								onClick={() => terminalHub.activate(threadId, id)}
							>
								<StatusDot entry={e} />
								<span className="truncate">{titleOf(e.info)}</span>
							</button>
							<button
								type="button"
								aria-label={t("terminal.close")}
								title={t("terminal.close")}
								className="inline-flex h-5 w-5 items-center justify-center rounded text-faint hover:bg-panel hover:text-fg"
								onClick={() => void terminalHub.close(threadId, id)}
							>
								<Icon name="close" size={11} />
							</button>
						</div>
					);
				})}
				<button
					type="button"
					aria-label={t("terminal.newShell")}
					title={t("terminal.newShell")}
					disabled={offline}
					className="inline-flex h-7 w-7 shrink-0 items-center justify-center rounded-md text-muted hover:bg-hover hover:text-fg disabled:opacity-40"
					onClick={() => void terminalHub.newShell(threadId)}
				>
					<Icon name="plus" size={14} />
				</button>
				{syncing && (
					<span className="ml-auto pr-1 text-faint">
						<Spinner size={12} />
					</span>
				)}
			</div>
			{error && !offline && (
				<div className="shrink-0 border-border border-b bg-err/10 px-3 py-1 text-err text-xs">
					{t("terminal.syncFailed", { message: error })}
				</div>
			)}
			{active && activeId && (
				<Notice threadId={threadId} terminalId={activeId} entry={active} />
			)}
			<div className="relative min-h-0 flex-1 bg-inset p-1.5">
				{active && activeId ? (
					<TerminalView
						key={activeId}
						threadId={threadId}
						terminalId={activeId}
						autoFocus={!coarse}
					/>
				) : (
					!offline && (
						<div className="flex h-full items-center justify-center gap-2 text-faint text-sm">
							{syncing ? <Spinner /> : null}
							{syncing ? t("terminal.opening") : t("terminal.empty")}
						</div>
					)
				)}
				{offline && <Offline threadId={threadId} />}
			</div>
			{coarse && active?.status === "live" && activeId && (
				<TerminalKeyBar threadId={threadId} terminalId={activeId} />
			)}
		</div>
	);
}
