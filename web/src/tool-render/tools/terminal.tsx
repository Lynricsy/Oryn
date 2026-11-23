/**
 * `terminal` — omp 的终端工具（`terminal_configure{agentTools:true}` 时注册）：list / read / run / stop / open / exec / send / wait。
 * 结果形状见 oryn-core `modes/rpc/rpc-terminal-tool.ts`：details 为 `{ action, terminal?, terminals?, exitCode? }`，
 * 结果文本是给模型的说明句（`Started … in …`、`<id>  <kind>  <state>  …` 状态行）加一段空行后的屏幕内容。
 * 卡片把终端画成一扇小窗口：标题栏给终端名、类别、主机与状态灯，窗体是屏幕内容，说明句退为窗口下的小字。
 * 只读宿主（分享页、导出）没有终端面板，「在终端面板查看」退为纯文本。
 */
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { revealTerminal } from "../../state/terminals";
import {
	Badge,
	Badges,
	CommandLine,
	InvalidArg,
	Note,
	Output,
	PathText,
} from "../parts";
import type { ToolRenderer, ToolRenderHost, ToolRenderProps } from "../types";
import {
	detailsRecord,
	isRecord,
	normalizeWs,
	num,
	resultTextOf,
	str,
	truncate,
} from "../util";

const ACTIONS = [
	"list",
	"read",
	"run",
	"stop",
	"open",
	"exec",
	"send",
	"wait",
] as const;
type Action = (typeof ACTIONS)[number];

const KINDS = ["shell", "command", "background", "session"] as const;

interface TermInfo {
	terminalId: string;
	kind: string;
	title: string;
	cwd: string;
	running: boolean;
	exitCode: number | null;
	state: string | null;
	host: string | null;
	root: boolean;
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
		root: value.root === true,
	};
}

type Light = "run" | "ok" | "err" | "warn" | "idle";

/** 状态灯与状态文字：会话按 starting / idle / busy / input，进程按运行中 / 退出码 / 被杀。 */
function stateOf(info: TermInfo): {
	key: "starting" | "idle" | "input" | "running" | "killed" | "exited";
	light: Light;
	code?: number;
} {
	if (info.running) {
		switch (info.state) {
			case "starting":
				return { key: "starting", light: "idle" };
			case "idle":
				return { key: "idle", light: "ok" };
			case "input":
				return { key: "input", light: "warn" };
			default:
				return { key: "running", light: "run" };
		}
	}
	if (info.exitCode === null) return { key: "killed", light: "warn" };
	return {
		key: "exited",
		light: info.exitCode === 0 ? "ok" : "err",
		code: info.exitCode,
	};
}

/** 只读宿主不给 orb 相关的回调：没有终端面板可开。 */
function interactive(host: ToolRenderHost | undefined): boolean {
	return host?.openOrbFile !== undefined;
}

function StateText({ info }: { info: TermInfo }): ReactNode {
	const { t } = useTranslation();
	const s = stateOf(info);
	return <>{t(`toolRender.terminal.state.${s.key}`, { code: s.code })}</>;
}

function OpenInPanel({ id }: { id?: string }): ReactNode {
	const { t } = useTranslation();
	return (
		<button
			type="button"
			className="tv-term-open"
			onClick={() => revealTerminal(id)}
		>
			{t("toolRender.terminal.viewInPanel")}
			<span aria-hidden="true">↗</span>
		</button>
	);
}

/** `ctrl-c` → `Ctrl+C`，`enter` → `Enter`。 */
function keyLabel(key: string): string {
	return key
		.split("-")
		.map((p) =>
			p.length <= 1 ? p.toUpperCase() : p[0]?.toUpperCase() + p.slice(1),
		)
		.join("+");
}

function actionOf(args: Record<string, unknown>): Action | null {
	const action = str(args.action);
	return ACTIONS.find((a) => a === action) ?? null;
}

function Summary({ args, result }: ToolRenderProps): ReactNode {
	const { t } = useTranslation();
	const action = actionOf(args);
	if (!action)
		return args.action === undefined ? (
			<span>…</span>
		) : (
			<InvalidArg what="action" />
		);
	const info = termInfo(detailsRecord(result)?.terminal);
	const id = str(args.id) ?? info?.terminalId ?? null;
	const command = str(args.command);
	const host = str(args.host);
	let text: string | null = null;
	if (command) text = normalizeWs(command);
	else if (action === "send")
		text = [str(args.text), str(args.key) && keyLabel(str(args.key) ?? "")]
			.filter(Boolean)
			.join(" + ");
	else if (action === "open")
		text = host
			? `ssh ${host}`
			: (str(args.label) ?? t("toolRender.terminal.local"));
	else if (action !== "list") text = info?.title || null;
	return (
		<>
			<Badge>{t(`toolRender.terminal.action.${action}`)}</Badge>
			{/* 有命令时命令才是要点；终端 id 在展开体里 */}
			{id && !command && action !== "open" && (
				<span className="tv-path">{id}</span>
			)}
			{text && <span>{truncate(normalizeWs(text), 80)}</span>}
		</>
	);
}

/** 头部读数：命令的退出码优先；否则终端此刻的状态；list 给终端数。 */
function Meta({ args, result }: ToolRenderProps): ReactNode {
	const { t } = useTranslation();
	if (!result || result.isError) return null;
	const details = detailsRecord(result);
	if (actionOf(args) === "list") {
		const list = Array.isArray(details?.terminals) ? details.terminals : null;
		return list ? (
			<span>{t("toolRender.terminal.count", { count: list.length })}</span>
		) : null;
	}
	const exitCode = num(details?.exitCode);
	if (exitCode !== null)
		return (
			<span className={exitCode === 0 ? "tv-meta-ok" : "tv-meta-err"}>
				{t("toolRender.meta.exit", { code: exitCode })}
			</span>
		);
	const info = termInfo(details?.terminal);
	if (!info) return null;
	const light = stateOf(info).light;
	return (
		<span
			className={
				light === "err"
					? "tv-meta-err"
					: light === "warn"
						? "tv-meta-warn"
						: undefined
			}
		>
			<StateText info={info} />
		</span>
	);
}

function Input({ args, result, host }: ToolRenderProps): ReactNode {
	const id = str(args.id);
	const command = str(args.command);
	const text = str(args.text);
	const key = str(args.key);
	const sshHost = str(args.host);
	const label = str(args.label);
	const cwd = str(args.cwd);
	const lines = num(args.lines);
	const wait = num(args.wait);
	// 有结果时链接在输出段的窗口标题栏；运行中（长命令、等待提示符）先在这里给入口
	const link = id && !result && interactive(host);
	return (
		<>
			{/* 折叠行只是摘要（会被截断、压成一行），展开体始终给出完整命令与发送内容 */}
			{command && <CommandLine command={command} />}
			{text && <Output text={text} maxLines={8} />}
			{(id ||
				key ||
				sshHost ||
				label ||
				cwd ||
				lines !== null ||
				wait !== null) && (
				<div className="tv-term-args">
					<Badges
						items={[
							id && <Badge>{id}</Badge>,
							key && (
								<kbd className="tv-kbd" title={key}>
									{keyLabel(key)}
								</kbd>
							),
							sshHost && <Badge>ssh {sshHost}</Badge>,
							label && <Badge>{label}</Badge>,
							cwd && <Badge>cwd={cwd}</Badge>,
							lines !== null && <Badge>lines={lines}</Badge>,
							wait !== null && <Badge>wait={wait}s</Badge>,
						]}
					/>
					{link && <OpenInPanel id={id} />}
				</div>
			)}
			{args.action !== undefined && !actionOf(args) && (
				<InvalidArg what="action" />
			)}
		</>
	);
}

const OMITTED_RE =
	/^\[(?:(\d+) earlier lines omitted[^\]]*|earlier output omitted)\]$/;
const EMPTY_SCREEN: Record<string, true> = {
	"(no output yet)": true,
	"(nothing on screen)": true,
};

/**
 * 结果文本拆成「说明句」与「屏幕」：第一个空行之前是说明（去掉与标题栏重复的状态行），之后是屏幕内容。
 * 只有说明、没有屏幕（open 的一句话、stop 无输出）时 screen 为 null。
 */
function splitText(
	text: string,
	info: TermInfo | null,
): { notes: string[]; screen: string | null; omitted: number | true | null } {
	const at = text.indexOf("\n\n");
	const head = at < 0 ? text : text.slice(0, at);
	let screen = at < 0 ? null : text.slice(at + 2);
	const notes = head
		.split("\n")
		.filter((l) => l.trim() !== "")
		.filter((l) => !info || !l.startsWith(`${info.terminalId}  `));
	let omitted: number | true | null = null;
	if (screen !== null) {
		const nl = screen.indexOf("\n");
		const m = OMITTED_RE.exec(nl < 0 ? screen : screen.slice(0, nl));
		if (m) {
			omitted = m[1] ? Number(m[1]) : true;
			screen = nl < 0 ? "" : screen.slice(nl + 1);
		}
		if (EMPTY_SCREEN[screen.trim()]) screen = "";
	}
	return { notes, screen, omitted };
}

function TermWindow({
	info,
	text,
	host,
}: {
	info: TermInfo;
	text: string;
	host: ToolRenderHost | undefined;
}): ReactNode {
	const { t } = useTranslation();
	const { notes, screen, omitted } = splitText(text, info);
	const light = stateOf(info).light;
	const kind = KINDS.find((k) => k === info.kind);
	return (
		<>
			<div className="tv-term">
				<div className="tv-term-bar">
					<span
						className={`tv-term-dot tv-term-dot--${light}`}
						aria-hidden="true"
					/>
					<span className="tv-term-title">{info.title || info.terminalId}</span>
					{info.title && <span className="tv-term-id">{info.terminalId}</span>}
					<Badges
						items={[
							kind && t(`toolRender.terminal.kind.${kind}`),
							info.host && `ssh ${info.host}`,
							info.root && "root",
						]}
					/>
					<span className={`tv-term-state tv-term-state--${light}`}>
						<StateText info={info} />
					</span>
					{interactive(host) && <OpenInPanel id={info.terminalId} />}
				</div>
				{info.cwd && (
					<div className="tv-term-cwd">
						<PathText path={info.cwd} />
					</div>
				)}
				{screen !== null && (
					<div className="tv-term-screen">
						{omitted !== null && (
							<div className="tv-term-omitted">
								{omitted === true
									? t("toolRender.terminal.omittedSome")
									: t("toolRender.terminal.omitted", { count: omitted })}
							</div>
						)}
						{screen.trim() !== "" ? (
							<Output text={screen} maxLines={16} variant="code" bare />
						) : (
							<span className="tv-term-empty">
								{t("toolRender.terminal.noOutput")}
							</span>
						)}
					</div>
				)}
			</div>
			{notes.length > 0 && (
				<div className="tv-term-note">{notes.join(" ")}</div>
			)}
		</>
	);
}

function TermList({
	list,
	host,
}: {
	list: TermInfo[];
	host: ToolRenderHost | undefined;
}): ReactNode {
	const { t } = useTranslation();
	if (list.length === 0)
		return (
			<span className="tv-term-empty">{t("toolRender.terminal.empty")}</span>
		);
	const open = interactive(host);
	return (
		<ul className="tv-term-list">
			{list.map((x) => {
				const kind = KINDS.find((k) => k === x.kind);
				const body = (
					<>
						<span
							className={`tv-term-dot tv-term-dot--${stateOf(x).light}`}
							aria-hidden="true"
						/>
						<span className="tv-term-id">{x.terminalId}</span>
						<span className="tv-term-title">{x.title || x.cwd}</span>
						<Badges
							items={[
								kind && t(`toolRender.terminal.kind.${kind}`),
								x.host && `ssh ${x.host}`,
								x.root && "root",
							]}
						/>
						<span className="tv-term-state">
							<StateText info={x} />
						</span>
					</>
				);
				return (
					<li key={x.terminalId}>
						{open ? (
							<button
								type="button"
								className="tv-term-row"
								title={t("toolRender.terminal.viewInPanel")}
								onClick={() => revealTerminal(x.terminalId)}
							>
								{body}
							</button>
						) : (
							<div className="tv-term-row">{body}</div>
						)}
					</li>
				);
			})}
		</ul>
	);
}

function Result({ args, result, host }: ToolRenderProps): ReactNode {
	if (!result) return null;
	const text = resultTextOf(result).replace(/\s+$/, "");
	if (result.isError) {
		const nl = text.indexOf("\n");
		const first = nl < 0 ? text : text.slice(0, nl);
		const rest = nl < 0 ? "" : text.slice(nl + 1).replace(/^\n+/, "");
		return (
			<>
				<Note tone="err">{first}</Note>
				{rest && <Output text={rest} maxLines={12} variant="code" />}
			</>
		);
	}
	const details = detailsRecord(result);
	if (Array.isArray(details?.terminals) || actionOf(args) === "list") {
		const list = Array.isArray(details?.terminals)
			? details.terminals.map(termInfo).filter((x): x is TermInfo => x !== null)
			: [];
		return <TermList list={list} host={host} />;
	}
	const info = termInfo(details?.terminal);
	if (info) return <TermWindow info={info} text={text} host={host} />;
	return text ? <Output text={text} maxLines={16} /> : null;
}

export const terminalRenderer: ToolRenderer = { Summary, Meta, Input, Result };
