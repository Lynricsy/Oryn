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
import { Callout, StatusDot, type Tone } from "../ui/data";
import { Icon } from "../ui/icons";
import { OrbBead } from "../ui/OrbBead";
import {
	Button,
	EmptyState,
	ErrorText,
	Spinner,
	tabClass,
} from "../ui/primitives";
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

/** 状态珠自带动画（旋转弧 / 霜脉冲）的状态：不再叠 Spinner。 */
const BEAD_ANIMATED: Record<string, true> = {
	requested: true,
	spawning: true,
	connecting: true,
	setting_up: true,
	resuming: true,
	waking: true,
	freezing: true,
};

function titleOf(info: RpcTerminalInfo): string {
	return info.title || info.kind;
}

/** 标签上的状态点：等待输入→琥珀脉冲，运行→绿，失联→红，其余→中性。 */
function tabTone(entry: TerminalEntry): { tone: Tone; pulse?: boolean } {
	if (entry.attention) return { tone: "warn", pulse: true };
	if (entry.status === "live") return { tone: "ok" };
	if (entry.status === "lost") return { tone: "err" };
	return { tone: "neutral" };
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
	const transient = TRANSIENT_ORB_STATES[state] === true;
	const beadAnimated = BEAD_ANIMATED[state] === true;
	if (!orb || state === "deleted") {
		body = <p className="text-muted text-sm">{t("terminal.orbGone")}</p>;
	} else if (busy || transient) {
		// 状态珠自带动画的过渡态不再叠 Spinner
		body = (
			<p className="flex items-center gap-2 text-muted text-sm">
				{!beadAnimated && <Spinner size={12} />}
				{t("terminal.orbStarting", { state: t(`orbState.${state}`) })}
			</p>
		);
	} else if (state === "ready") {
		body = (
			<p className="flex items-center gap-2 text-muted text-sm">
				<Spinner size={12} />
				{t("terminal.agentStarting")}
			</p>
		);
	} else {
		body = (
			<>
				<div className="space-y-1">
					<p className="font-medium text-fg text-sm">{t("terminal.offline")}</p>
					<p className="max-w-xs text-muted text-xs">
						{state === "failed" && orb.failureReason
							? t("terminal.orbFailed", { reason: orb.failureReason })
							: t("terminal.offlineHint", { state: t(`orbState.${state}`) })}
					</p>
				</div>
				<Button
					variant="accent"
					icon="play"
					className="mt-1"
					onClick={() => void wake()}
				>
					{t("terminal.wake")}
				</Button>
			</>
		);
	}
	return (
		<div
			data-testid="terminal-offline"
			className="absolute inset-2 z-10 flex flex-col items-center justify-center gap-3 rounded-lg bg-panel/85 p-6 text-center backdrop-blur-sm"
		>
			{orb && state !== "deleted" ? (
				<OrbBead state={state} size={40} />
			) : (
				<span className="grid h-11 w-11 place-items-center rounded-full bg-inset">
					<Icon name="terminal" size={20} className="text-faint" />
				</span>
			)}
			{body}
			<ErrorText error={error} />
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
	const lost = entry.status === "lost";
	const text = lost
		? t("terminal.lost")
		: entry.info.exitCode === undefined
			? t("terminal.killed")
			: t("terminal.exited", { code: entry.info.exitCode });
	return (
		<div data-testid="terminal-notice" className="shrink-0 px-2 pt-2">
			<Callout
				tone={lost ? "err" : "neutral"}
				icon={lost ? "alert" : "info"}
				actions={
					<Button
						size="sm"
						variant="ghost"
						className="-my-1"
						onClick={() => void terminalHub.close(threadId, terminalId)}
					>
						{t("terminal.dismiss")}
					</Button>
				}
			>
				{text}
			</Callout>
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
				className="focus-inset flex shrink-0 items-center gap-0.5 overflow-x-auto border-border border-b px-1.5 py-1.5"
			>
				{order.map((id) => {
					const e = terminals[id];
					if (!e) return null;
					const selected = id === activeId;
					const dot = tabTone(e);
					return (
						<div
							key={id}
							className={`group flex shrink-0 items-center rounded-md transition-colors duration-150 ${selected ? "bg-hover" : "hover:bg-hover/60"}`}
						>
							<button
								type="button"
								role="tab"
								aria-selected={selected}
								data-terminal-tab={id}
								title={`${titleOf(e.info)} · ${t(`terminal.kind.${e.info.kind}`)}${e.attention ? ` · ${t("terminal.needsInput")}` : ""}`}
								className={tabClass(selected)}
								onClick={() => terminalHub.activate(threadId, id)}
							>
								<StatusDot tone={dot.tone} pulse={dot.pulse} />
								<span className="max-w-36 truncate">{titleOf(e.info)}</span>
							</button>
							<button
								type="button"
								aria-label={t("terminal.close")}
								title={t("terminal.close")}
								className={`-ml-1.5 mr-0.5 inline-flex h-5 w-5 items-center justify-center rounded text-faint transition-opacity hover:bg-active hover:text-fg ${selected ? "" : "opacity-60 group-hover:opacity-100"}`}
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
					className="inline-flex h-7 w-7 shrink-0 items-center justify-center rounded-md text-muted transition-[background-color,color,scale] duration-150 ease-out hover:bg-hover hover:text-fg active:scale-95 disabled:opacity-40"
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
				<div className="shrink-0 px-2 pt-2">
					<Callout tone="err">
						{t("terminal.syncFailed", { message: error })}
					</Callout>
				</div>
			)}
			{active && activeId && (
				<Notice threadId={threadId} terminalId={activeId} entry={active} />
			)}
			<div className="relative min-h-0 flex-1 p-2">
				<div className="h-full overflow-hidden rounded-lg border border-border bg-inset p-1.5">
					{active && activeId ? (
						<TerminalView
							key={activeId}
							threadId={threadId}
							terminalId={activeId}
							autoFocus={!coarse}
						/>
					) : (
						!offline &&
						(syncing ? (
							<div className="flex h-full items-center justify-center gap-2 text-faint text-sm">
								<Spinner />
								{t("terminal.opening")}
							</div>
						) : (
							<div className="flex h-full items-center justify-center">
								<EmptyState icon="terminal" title={t("terminal.empty")} />
							</div>
						))
					)}
				</div>
				{offline && <Offline threadId={threadId} />}
			</div>
			{coarse && active?.status === "live" && activeId && (
				<TerminalKeyBar threadId={threadId} terminalId={activeId} />
			)}
		</div>
	);
}
