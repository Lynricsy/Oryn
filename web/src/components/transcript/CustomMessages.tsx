/**
 * Core 注入转录的专属自定义消息渲染：
 * - `AdvisorCard`：顾问旁听建议（按最重严重度着色、带顾问头像、分级计数、多来源归属与超过 3 条折叠）；
 * - `LateDiagnosticsCard`：迟到的 LSP 诊断（按文件展示等宽诊断列表，超出 5 行折叠）；
 * - `SkillPromptCard`：用户触发的 `/skill:<name>`（直接突出用户指令，把冗长技能提示词折叠收起）；
 * - `IrcMessageCard`：agent 间 IRC 协作消息（`irc:incoming` / `irc:autoreply` / `irc:relay` / `irc:workpool`）；
 * - `TanDispatchRow`：`/tan` 后台分支派发面包屑。
 *
 * 所有组件只使用 `index.css` 语义令牌，不使用颜色字面量或入场动画，分享页（`readOnly`）不发设置请求。
 */
import type { TFunction } from "i18next";
import { type ReactNode, useContext, useState } from "react";
import { useTranslation } from "react-i18next";
import { useAgentFace } from "../../api/identity-queries";
import {
	type AdvisorRank,
	type ParsedAdvisorMessage,
	type ParsedIrcMessage,
	type ParsedLateDiagnostics,
	type ParsedSkillPrompt,
	type ParsedTanDispatch,
	parseAdvisorMessage,
	parseIrcCustomMessage,
	parseLateDiagnostics,
	parseSkillPrompt,
	parseTanDispatch,
} from "../../lib/custom-messages";
import { shareAgentFace } from "../../share/identity";
import type { JsonRecord } from "../../state/message-builder";
import { Markdown } from "../markdown/Markdown";
import { AvatarTile } from "../ui/AvatarTile";
import { CopyButton } from "../ui/CopyButton";
import { Badge } from "../ui/data";
import { FileTypeIcon } from "../ui/FileTypeIcon";
import { Icon, type IconName } from "../ui/icons";
import { TranscriptContext } from "./context";
import { Foldable } from "./Foldable";
import { escapeAngles, MD_SIZE } from "./NoticeLine";

/** 与 Core 一致：顾问卡片默认展示前 3 条，超出时可点击展开全部。 */
const COLLAPSED_ADVISOR_NOTES = 3;
/** 与 Core 一致：迟到诊断默认预览前 5 行。 */
const COLLAPSED_DIAGNOSTICS = 5;

// ─── AdvisorCard ──────────────────────────────────────────────────────────────

interface RankLook {
	icon: IconName;
	card: string;
	head: string;
	pill: string;
	chip: string;
	rail: string;
}

const ADVISOR_LOOK: Record<AdvisorRank, RankLook> = {
	blocker: {
		icon: "octagonAlert",
		card: "border-err/40",
		head: "border-err/20 bg-err/8",
		pill: "bg-err/12 text-err",
		chip: "bg-err/12 text-err",
		rail: "border-err/55",
	},
	concern: {
		icon: "alert",
		card: "border-warn/40",
		head: "border-warn/25 bg-warn/10",
		pill: "bg-warn/15 text-warn-ink",
		chip: "bg-warn/15 text-warn-ink",
		rail: "border-warn/65",
	},
	nit: {
		icon: "lightbulb",
		card: "border-border",
		head: "border-border bg-inset/50",
		pill: "bg-inset text-muted",
		chip: "bg-inset text-muted",
		rail: "border-border-strong",
	},
	note: {
		icon: "info",
		card: "border-border",
		head: "border-border bg-inset/50",
		pill: "bg-inset text-muted",
		chip: "bg-inset text-muted",
		rail: "border-border-strong",
	},
};

function advisorCopyText(parsed: ParsedAdvisorMessage, t: TFunction): string {
	return parsed.notes
		.map((n) => {
			const parts: string[] = [];
			parts.push(`[${t(`transcript.advisor.rank.${n.rank}`)}]`);
			if (n.source) parts.push(`${n.source}:`);
			return `${parts.join(" ")} ${n.note}`;
		})
		.join("\n\n");
}

/** 顾问头像（22px 圆形）：复用「子 agent → advisor」自定义头像，未设时回退默认字形；分享页不发请求。 */
function AdvisorAvatar(): ReactNode {
	const ctx = useContext(TranscriptContext);
	const share = ctx?.readOnly === true;
	const site = useAgentFace("advisor", !share);
	const { face, glyph } = share
		? shareAgentFace(ctx?.agentFaces, "advisor")
		: site;
	return (
		<AvatarTile
			of="agent"
			identity={face}
			size={22}
			tone="neutral"
			shape="circle"
			fallback={<span style={{ fontSize: 14 }}>{glyph}</span>}
		/>
	);
}

function AdvisorRankPill({
	rank,
	count,
}: {
	rank: AdvisorRank;
	/** 省略时只显示级别名（单条模式）；给定数字时显示「N 级别」（多条计数模式）。 */
	count?: number;
}): ReactNode {
	const { t } = useTranslation();
	const look = ADVISOR_LOOK[rank];
	const label =
		count === undefined
			? t(`transcript.advisor.rank.${rank}`)
			: t(`transcript.advisor.tally.${rank}`, { count });
	return (
		<span
			data-rank={rank}
			className={`inline-flex h-5 max-w-full shrink-0 items-center gap-1 whitespace-nowrap rounded-full px-2 font-medium text-2xs tabular-nums ${look.pill}`}
		>
			<Icon name={look.icon} size={11} aria-hidden className="shrink-0" />
			<span className="truncate">{label}</span>
		</span>
	);
}

export function AdvisorCard({
	parsed,
}: {
	parsed: ParsedAdvisorMessage;
}): ReactNode {
	const { t } = useTranslation();
	const [expanded, setExpanded] = useState(false);
	const look = ADVISOR_LOOK[parsed.worstRank];
	const soleNote = parsed.notes.length === 1 ? parsed.notes[0] : undefined;
	const collapsible = parsed.notes.length > COLLAPSED_ADVISOR_NOTES;
	const visibleNotes =
		collapsible && !expanded
			? parsed.notes.slice(0, COLLAPSED_ADVISOR_NOTES)
			: parsed.notes;
	const hiddenCount = parsed.notes.length - visibleNotes.length;
	const title = parsed.soleSource ?? t("transcript.advisor.title");
	const ariaLabel = parsed.soleSource
		? `${parsed.soleSource} · ${t("transcript.advisor.title")}`
		: t("transcript.advisor.title");

	return (
		<section
			data-testid="advisor-card"
			data-severity={parsed.worstRank}
			aria-label={ariaLabel}
			className={`@container w-full min-w-0 overflow-hidden rounded-lg border bg-panel/75 ${look.card}`}
		>
			<header
				className={`flex min-h-10 min-w-0 flex-wrap items-center gap-x-2 gap-y-1.5 border-b px-3 py-2 ${look.head}`}
			>
				<AdvisorAvatar />
				<div className="flex min-w-0 items-baseline gap-1.5">
					<span className="truncate font-semibold text-[13px] text-fg">
						{title}
					</span>
					{parsed.soleSource && (
						<span className="shrink-0 text-2xs text-faint">
							{t("transcript.advisor.title")}
						</span>
					)}
				</div>
				<div className="flex min-w-0 flex-wrap items-center gap-1.5">
					{soleNote ? (
						<AdvisorRankPill rank={soleNote.rank} />
					) : (
						parsed.counts.map((c) => (
							<AdvisorRankPill key={c.rank} rank={c.rank} count={c.count} />
						))
					)}
				</div>
				<div className="ml-auto flex shrink-0 items-center gap-1.5">
					{soleNote?.turnsAgo !== undefined && (
						<span className="text-2xs text-faint tabular-nums">
							{t("transcript.advisor.turnsAgo", {
								count: soleNote.turnsAgo,
							})}
						</span>
					)}
					<CopyButton
						text={advisorCopyText(parsed, t)}
						ariaLabel={t("transcript.advisor.copy")}
					/>
				</div>
			</header>

			{soleNote ? (
				<div className="px-3.5 py-2.5">
					<div className={`border-l-2 pl-3 ${look.rail}`}>
						<Foldable
							lines={soleNote.note.split("\n").length}
							testId="advisor-note-body"
							toggleClassName="-ml-1.5 pt-1"
						>
							<div className={`min-w-0 text-fg ${MD_SIZE}`}>
								<Markdown text={escapeAngles(soleNote.note)} />
							</div>
						</Foldable>
					</div>
				</div>
			) : (
				<>
					<ul className="divide-y divide-border/60">
						{visibleNotes.map((note, idx) => {
							const itemLook = ADVISOR_LOOK[note.rank];
							const showMeta =
								(parsed.mixedSources && note.source) ||
								note.turnsAgo !== undefined;
							return (
								<li
									// biome-ignore lint/suspicious/noArrayIndexKey: 顾问建议批次内无独立稳定 id
									key={idx}
									data-testid="advisor-note"
									data-rank={note.rank}
									className="flex min-w-0 items-start gap-2.5 px-3.5 py-2.5"
								>
									<span
										role="img"
										aria-label={t(`transcript.advisor.rank.${note.rank}`)}
										title={t(`transcript.advisor.rank.${note.rank}`)}
										className={`mt-0.5 inline-grid size-5 shrink-0 place-items-center rounded-xs ${itemLook.chip}`}
									>
										<Icon name={itemLook.icon} size={12} aria-hidden />
									</span>
									<div className="min-w-0 flex-1">
										{showMeta && (
											<div className="mb-1 flex flex-wrap items-center gap-1.5 text-2xs text-faint">
												{parsed.mixedSources && note.source && (
													<span className="rounded bg-inset px-1.5 py-0.5 font-mono text-muted">
														{note.source}
													</span>
												)}
												{note.turnsAgo !== undefined && (
													<span className="tabular-nums">
														{t("transcript.advisor.turnsAgo", {
															count: note.turnsAgo,
														})}
													</span>
												)}
											</div>
										)}
										<div className={`min-w-0 text-fg ${MD_SIZE}`}>
											<Markdown text={escapeAngles(note.note)} />
										</div>
									</div>
								</li>
							);
						})}
					</ul>
					{collapsible && (
						<button
							type="button"
							aria-expanded={expanded}
							onClick={() => setExpanded((v) => !v)}
							className="flex w-full items-center justify-center gap-1.5 border-border/70 border-t bg-inset/30 px-3 py-1.5 font-medium text-muted text-xs transition-colors duration-150 hover:bg-hover/60 hover:text-fg"
						>
							<Icon
								name="chevronRight"
								size={12}
								aria-hidden
								className={`text-faint transition-transform duration-150 ${expanded ? "rotate-90" : ""}`}
							/>
							{expanded
								? t("transcript.advisor.collapse")
								: t("transcript.advisor.showMore", { count: hiddenCount })}
						</button>
					)}
				</>
			)}
		</section>
	);
}

// ─── LateDiagnosticsCard ──────────────────────────────────────────────────────

export function LateDiagnosticsCard({
	parsed,
}: {
	parsed: ParsedLateDiagnostics;
}): ReactNode {
	const { t } = useTranslation();
	const [open, setOpen] = useState(false);
	const errored = parsed.errored;
	const chipCls = errored ? "bg-err/10 text-err" : "bg-warn/15 text-warn-ink";
	const borderCls = errored ? "border-err/35" : "border-warn/40";
	const allLines = parsed.files.flatMap((f) =>
		f.messages.map((msg) => ({ path: f.path, msg })),
	);
	const hasBody = allLines.length > 0 || Boolean(parsed.fallbackText);
	const detail =
		parsed.summary ||
		(parsed.files.length === 1
			? (parsed.files[0]?.path ?? "")
			: parsed.files.length > 1
				? t("transcript.lateDiagnostics.files", { count: parsed.files.length })
				: "");

	return (
		<section
			data-testid="late-diagnostics-card"
			data-errored={errored ? "true" : "false"}
			className={`overflow-hidden rounded-lg border bg-panel/60 ${borderCls}`}
		>
			{hasBody ? (
				<button
					type="button"
					aria-expanded={open}
					onClick={() => setOpen((v) => !v)}
					className="flex min-h-[38px] w-full min-w-0 items-center gap-2.5 pr-2.5 pl-[7px] text-left transition-colors duration-150 hover:bg-hover/60"
				>
					<span
						aria-hidden
						className={`grid h-[22px] w-[22px] shrink-0 place-items-center rounded-sm ${chipCls}`}
					>
						<Icon name={errored ? "xCircle" : "alert"} size={13} />
					</span>
					<span className="shrink-0 font-semibold text-[12.5px] text-fg">
						{t("transcript.lateDiagnostics.title")}
					</span>
					{detail && (
						<span className="min-w-0 flex-1 truncate font-mono text-faint text-xs">
							{detail}
						</span>
					)}
					{parsed.totalMessages > 0 && (
						<span className="shrink-0 text-2xs text-faint tabular-nums">
							{t("transcript.lateDiagnostics.items", {
								count: parsed.totalMessages,
							})}
						</span>
					)}
					<Icon
						name="chevronRight"
						size={12}
						aria-hidden
						className={`shrink-0 text-ghost transition-transform duration-150 ${open ? "rotate-90" : ""}`}
					/>
				</button>
			) : (
				<div className="flex min-h-[38px] w-full min-w-0 items-center gap-2.5 pr-2.5 pl-[7px]">
					<span
						aria-hidden
						className={`grid h-[22px] w-[22px] shrink-0 place-items-center rounded-sm ${chipCls}`}
					>
						<Icon name={errored ? "xCircle" : "alert"} size={13} />
					</span>
					<span className="font-semibold text-[12.5px] text-fg">
						{t("transcript.lateDiagnostics.title")}
					</span>
				</div>
			)}

			{!open && allLines.length > 0 && (
				<div className="border-border/70 border-t bg-inset/60 px-3 py-2 font-mono text-muted text-xs leading-relaxed">
					{allLines.slice(0, COLLAPSED_DIAGNOSTICS).map((item, i) => (
						// biome-ignore lint/suspicious/noArrayIndexKey: 诊断预览行无稳定 id
						<div key={i} className="truncate">
							{item.msg}
						</div>
					))}
					{allLines.length > COLLAPSED_DIAGNOSTICS && (
						<div className="mt-0.5 text-2xs text-faint">
							{t("transcript.lateDiagnostics.more", {
								count: allLines.length - COLLAPSED_DIAGNOSTICS,
							})}
						</div>
					)}
				</div>
			)}

			{open && (
				<div className="flex flex-col gap-2.5 border-border border-t bg-inset px-3 py-2.5">
					{parsed.files.length > 0 ? (
						parsed.files.map((file, i) => (
							// biome-ignore lint/suspicious/noArrayIndexKey: 同一文件可能多次上报
							<div key={`${file.path}-${i}`} className="min-w-0">
								{file.path && (
									<div className="mb-1 flex min-w-0 items-center gap-1.5 text-xs">
										<FileTypeIcon name={file.path} size={13} />
										<span className="min-w-0 truncate font-mono font-medium text-fg">
											{file.path}
										</span>
										{file.summary && (
											<span className="shrink-0 text-2xs text-faint">
												{file.summary}
											</span>
										)}
									</div>
								)}
								<pre className="m-0 max-h-72 overflow-auto whitespace-pre-wrap break-words font-mono text-fg text-xs leading-relaxed">
									{file.messages.join("\n")}
								</pre>
							</div>
						))
					) : (
						<pre className="m-0 max-h-72 overflow-auto whitespace-pre-wrap break-words font-mono text-fg text-xs leading-relaxed">
							{parsed.fallbackText}
						</pre>
					)}
				</div>
			)}
		</section>
	);
}

// ─── SkillPromptCard ──────────────────────────────────────────────────────────

export function SkillPromptCard({
	parsed,
	images,
}: {
	parsed: ParsedSkillPrompt;
	images?: ReactNode;
}): ReactNode {
	const { t } = useTranslation();
	const [open, setOpen] = useState(false);

	return (
		<section
			data-testid="skill-prompt-card"
			aria-label={`/skill:${parsed.name}`}
			className="overflow-hidden rounded-lg border border-border bg-panel/70"
		>
			<header className="flex min-h-9 min-w-0 flex-wrap items-center gap-x-2 gap-y-1 border-border border-b bg-inset/40 px-3 py-1.5">
				<Badge icon="puzzle">{`/skill:${parsed.name}`}</Badge>
				{parsed.lineCount !== undefined && (
					<span className="text-2xs text-faint tabular-nums">
						{t("transcript.skillPrompt.lines", { count: parsed.lineCount })}
					</span>
				)}
				{parsed.promptText && (
					<button
						type="button"
						aria-expanded={open}
						onClick={() => setOpen((v) => !v)}
						className="ml-auto inline-flex h-6 shrink-0 items-center gap-1 rounded-sm px-1.5 text-muted text-xs transition-colors duration-150 hover:bg-hover hover:text-fg"
					>
						<span>
							{open
								? t("transcript.skillPrompt.hidePrompt")
								: t("transcript.skillPrompt.showPrompt")}
						</span>
						<Icon
							name="chevronRight"
							size={11}
							aria-hidden
							className={`text-faint transition-transform duration-150 ${open ? "rotate-90" : ""}`}
						/>
					</button>
				)}
			</header>
			{(parsed.draft || images) && (
				<div className="px-3.5 py-2.5">
					{parsed.draft && (
						<div className={`text-fg ${MD_SIZE}`}>
							<Markdown text={escapeAngles(parsed.draft)} />
						</div>
					)}
					{images}
				</div>
			)}
			{open && parsed.promptText && (
				<div className="border-border border-t bg-inset/60 px-3.5 py-2.5">
					<div className={`max-h-96 overflow-auto text-muted ${MD_SIZE}`}>
						<Markdown text={escapeAngles(parsed.promptText)} />
					</div>
				</div>
			)}
		</section>
	);
}

// ─── IrcMessageCard ───────────────────────────────────────────────────────────

export function IrcMessageCard({
	parsed,
}: {
	parsed: ParsedIrcMessage;
}): ReactNode {
	const { t } = useTranslation();
	const route =
		parsed.from && parsed.to
			? `${parsed.from} → ${parsed.to}`
			: (parsed.from ?? parsed.to ?? t("transcript.irc.title"));

	return (
		<section
			data-testid="irc-message-card"
			data-irc-kind={parsed.kind}
			className="overflow-hidden rounded-lg border border-border bg-panel/60"
		>
			<header className="flex min-h-9 min-w-0 flex-wrap items-center gap-2 border-border border-b bg-inset/40 pr-3 pl-2.5 py-1.5">
				<span
					aria-hidden
					className="inline-grid h-[22px] w-[22px] shrink-0 place-items-center rounded-sm bg-inset text-muted"
				>
					<Icon name="chats" size={13} />
				</span>
				<span className="min-w-0 truncate font-medium text-[12.5px] text-fg">
					{route}
				</span>
				<Badge>{t(`transcript.irc.kind.${parsed.kind}`)}</Badge>
				{parsed.progress && <Badge>{t("transcript.irc.progress")}</Badge>}
				{parsed.broadcast && <Badge>{t("transcript.irc.broadcast")}</Badge>}
				{parsed.pool && <Badge>{parsed.pool}</Badge>}
				{parsed.replyTo && (
					<span className="truncate font-mono text-2xs text-faint">
						{`#${parsed.replyTo}`}
					</span>
				)}
			</header>
			{parsed.body && (
				<div className="px-3.5 py-2.5">
					<Foldable
						lines={parsed.body.split("\n").length}
						testId="irc-body"
						toggleClassName="-ml-1.5 pt-1"
					>
						<div className={`text-fg ${MD_SIZE}`}>
							<Markdown text={escapeAngles(parsed.body)} />
						</div>
					</Foldable>
				</div>
			)}
		</section>
	);
}

// ─── TanDispatchRow ───────────────────────────────────────────────────────────

export function TanDispatchRow({
	parsed,
}: {
	parsed: ParsedTanDispatch;
}): ReactNode {
	const { t } = useTranslation();
	return (
		<section
			data-testid="tan-dispatch-card"
			className="flex min-h-[38px] w-full min-w-0 items-center gap-2.5 overflow-hidden rounded-lg border border-border bg-panel/60 pr-3 pl-[7px]"
		>
			<span
				aria-hidden
				className="grid h-[22px] w-[22px] shrink-0 place-items-center rounded-sm bg-inset text-muted"
			>
				<Icon name="fork" size={13} />
			</span>
			<span className="shrink-0 font-semibold text-[12.5px] text-fg">
				{t("transcript.tanDispatch.title")}
			</span>
			{parsed.work && (
				<span className="min-w-0 flex-1 truncate text-muted text-xs">
					{parsed.work}
				</span>
			)}
			{parsed.jobId && <Badge>{parsed.jobId}</Badge>}
		</section>
	);
}

// ─── Unified Dispatcher ───────────────────────────────────────────────────────

/**
 * 识别并渲染 Core 已知的专属 `custom` / `hookMessage`；不匹配时返回 null，
 * 由调用方继续走后台任务事件行或通用 `CustomMessage`。
 */
export function renderKnownCustomMessage(
	message: JsonRecord,
	images?: ReactNode,
): ReactNode | null {
	const advisor = parseAdvisorMessage(message);
	if (advisor) return <AdvisorCard parsed={advisor} />;

	const diag = parseLateDiagnostics(message);
	if (diag) return <LateDiagnosticsCard parsed={diag} />;

	const skill = parseSkillPrompt(message);
	if (skill) return <SkillPromptCard parsed={skill} images={images} />;

	const irc = parseIrcCustomMessage(message);
	if (irc) return <IrcMessageCard parsed={irc} />;

	const tan = parseTanDispatch(message);
	if (tan) return <TanDispatchRow parsed={tan} />;

	return null;
}
