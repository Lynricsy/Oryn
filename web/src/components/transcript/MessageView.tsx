/**
 * 转录中的单条消息渲染：按 omp 的信息量全量展示（思考块、工具卡片、回合脚注、错误、bash 执行、自定义消息等）。
 * 所有字段都按「可能残缺」处理——条目与流式帧都是外部 JSON。
 */
import { memo, type ReactNode, useState } from "react";
import { useTranslation } from "react-i18next";
import {
	attachmentContentUrl,
	type MessageAttachment,
	parseAttachmentBlock,
} from "../../lib/attachments";
import { fileIcon } from "../../lib/file-icon";
import {
	formatBytes,
	formatCost,
	formatDuration,
	formatTokens,
} from "../../lib/format";
import { imageSrc } from "../../lib/image-src";
import type { JsonRecord } from "../../state/message-builder";
import { ToolView } from "../../tool-render/ToolView";
import type { ToolResultLike } from "../../tool-render/types";
import { isRecord, resultTextOf } from "../../tool-render/util";
import { Markdown } from "../markdown/Markdown";
import { Badge, Callout, Stat } from "../ui/data";
import { ImageViewer } from "../ui/ImageViewer";
import { Icon, type IconName } from "../ui/icons";
import { Menu } from "../ui/primitives";
import { useTranscriptContext } from "./context";

const CHIP =
	"inline-flex items-center gap-1.5 rounded-md border border-border bg-panel px-2 py-1 text-xs";

function textOfContent(content: unknown): string {
	if (typeof content === "string") return content;
	if (!Array.isArray(content)) return "";
	return content
		.map((b) =>
			isRecord(b) && b.type === "text" && typeof b.text === "string"
				? b.text
				: "",
		)
		.filter(Boolean)
		.join("\n\n");
}

function Images({ content }: { content: unknown }): ReactNode {
	const { t } = useTranslation();
	const [viewing, setViewing] = useState<string | null>(null);
	if (!Array.isArray(content)) return null;
	const imgs = content.filter(
		(b): b is { type: "image"; data: string; mimeType: string } =>
			isRecord(b) &&
			b.type === "image" &&
			typeof b.data === "string" &&
			typeof b.mimeType === "string",
	);
	if (imgs.length === 0) return null;
	return (
		<div className="mt-2 flex flex-wrap gap-2">
			{imgs.map((img, i) => {
				const src = imageSrc(img.data, img.mimeType);
				if (!src)
					return (
						<span
							// biome-ignore lint/suspicious/noArrayIndexKey: 图片块无稳定 id
							key={i}
							className="rounded-md border border-border px-2 py-1 text-faint text-xs"
						>
							{t("image.unsupported", { type: img.mimeType })}
						</span>
					);
				return (
					<button
						// biome-ignore lint/suspicious/noArrayIndexKey: 图片块无稳定 id
						key={i}
						type="button"
						className="rounded-md"
						onClick={() => setViewing(src)}
						aria-label={t("image.open", { n: i + 1 })}
					>
						<img
							src={src}
							alt=""
							className="max-h-64 max-w-full rounded-md border border-border"
							loading="lazy"
						/>
					</button>
				);
			})}
			{viewing && (
				<ImageViewer src={viewing} alt="" onClose={() => setViewing(null)} />
			)}
		</div>
	);
}

/** 用户消息附件块解析出的附件芯片：点击以附件形式下载（`attachments/{id}/content`）。 */
export function AttachmentChips({
	attachments,
}: {
	attachments: MessageAttachment[];
}): ReactNode {
	const { t } = useTranslation();
	if (attachments.length === 0) return null;
	return (
		<ul
			className="mt-2 flex flex-wrap gap-1.5"
			aria-label={t("attachments.inMessage")}
		>
			{attachments.map((a) => {
				const body = (
					<>
						<Icon
							name={fileIcon(a.name, a.mime)}
							size={13}
							className="shrink-0 text-faint"
						/>
						<span className="max-w-56 truncate">{a.name}</span>
						<span className="text-faint tabular-nums">
							{formatBytes(a.size)}
						</span>
					</>
				);
				const cls = CHIP;
				return (
					<li key={a.path}>
						{a.id ? (
							<a
								href={attachmentContentUrl(a.id)}
								download={a.name}
								className={`${cls} hover:bg-hover`}
								title={a.path}
							>
								{body}
							</a>
						) : (
							<span className={cls} title={a.path}>
								{body}
							</span>
						)}
					</li>
				);
			})}
		</ul>
	);
}

/** 消息操作菜单：行内渲染，悬停 / 聚焦时显现；贴近输入坞，向上展开。 */
function MessageMenu({
	entryId,
	text,
}: {
	entryId?: string;
	text: string;
}): ReactNode {
	const { t } = useTranslation();
	const ctx = useTranscriptContext();
	return (
		<div className="opacity-0 transition-opacity duration-150 focus-within:opacity-100 group-hover/msg:opacity-100 pointer-coarse:opacity-100">
			<Menu
				side="top"
				trigger={(p) => (
					<button
						type="button"
						className="inline-grid h-6 w-6 place-items-center rounded-md text-ghost hover:bg-hover hover:text-fg"
						aria-label={t("transcript.messageActions")}
						{...p}
					>
						<Icon name="more" size={14} />
					</button>
				)}
				items={[
					{
						key: "copy",
						icon: "copy",
						label: t("common.copy"),
						onSelect: () => void navigator.clipboard?.writeText(text),
					},
					...(entryId
						? [
								{
									key: "fork",
									icon: "fork" as const,
									label: t("transcript.forkHere"),
									onSelect: () => ctx.forkFrom(entryId),
								},
								{
									key: "label",
									icon: "edit" as const,
									label: t("transcript.label"),
									onSelect: () => ctx.labelEntry(entryId),
								},
							]
						: []),
				]}
			/>
		</div>
	);
}

function UserMessage({
	message,
	entryId,
	live,
}: {
	message: JsonRecord;
	entryId?: string;
	live: boolean;
}) {
	const { t } = useTranslation();
	// 控制面追加的附件块不按原文显示，渲染成附件芯片
	const { text, attachments } = parseAttachmentBlock(
		textOfContent(message.content),
	);
	const irc = isRecord(message.parentIrc) ? message.parentIrc : null;
	const synthetic = message.synthetic === true;
	const steer = message.steering === true || message.liveSteered === true;
	const body = irc ? String(irc.body ?? text) : text;
	return (
		<div className="group/msg flex flex-col items-end gap-1">
			{(irc || synthetic || steer) && (
				<div className="flex flex-wrap justify-end gap-1">
					{irc && (
						<Badge icon="chats">
							{t("transcript.ircFrom", { from: String(irc.from ?? "") })}
						</Badge>
					)}
					{synthetic && <Badge icon="bolt">{t("transcript.synthetic")}</Badge>}
					{steer && <Badge icon="send">{t("transcript.steer")}</Badge>}
				</div>
			)}
			{!irc && <span className="sr-only">{t("transcript.you")}</span>}
			<div
				className={`w-fit min-w-0 max-w-[85%] rounded-2xl rounded-br-sm border border-border bg-raised px-3.5 py-2 ${live ? "opacity-80" : ""}`}
			>
				{body && (
					<div className="whitespace-pre-wrap break-words text-[0.875rem] leading-[1.375rem]">
						{body}
					</div>
				)}
				<Images content={message.content} />
				<AttachmentChips attachments={attachments} />
			</div>
			<MessageMenu entryId={entryId} text={text} />
		</div>
	);
}

/** 思考块文本压成一行预览：完成后取开头，流式中取末尾。 */
function thinkingPreview(text: string, streaming: boolean): string {
	const flat = text.replace(/\s+/g, " ").trim();
	if (flat.length <= 240) return flat;
	return streaming ? `…${flat.slice(-240)}` : flat.slice(0, 240);
}

function ThinkingBlock({
	text,
	streaming,
}: {
	text: string;
	streaming: boolean;
}) {
	const { t } = useTranslation();
	const [open, setOpen] = useState(false);
	return (
		<div className="my-0.5">
			<button
				type="button"
				className="group/think flex h-7 w-full min-w-0 items-center gap-2 rounded-md text-left text-muted text-xs hover:text-fg"
				onClick={() => setOpen((v) => !v)}
				aria-expanded={open}
			>
				<Icon name="brain" size={14} className="shrink-0 text-faint" />
				<span className="shrink-0 whitespace-nowrap">
					{streaming ? t("transcript.thinkingLive") : t("transcript.thinking")}
				</span>
				<span className="min-w-0 flex-1 truncate text-faint italic">
					{open ? "" : thinkingPreview(text, streaming)}
				</span>
				<Icon
					name="chevronRight"
					size={12}
					className={`shrink-0 text-ghost transition-transform duration-150 group-hover/think:text-faint ${open ? "rotate-90" : ""}`}
				/>
			</button>
			{open && (
				<div
					className={`mt-0.5 mb-1.5 whitespace-pre-wrap break-words pl-[22px] text-muted text-xs leading-relaxed ${streaming ? "stream-caret" : ""}`}
				>
					{text}
				</div>
			)}
		</div>
	);
}

function toolResultLike(raw: unknown): ToolResultLike | undefined {
	if (!isRecord(raw)) return undefined;
	return {
		content: Array.isArray(raw.content)
			? (raw.content as ToolResultLike["content"])
			: [],
		details: raw.details,
		isError: raw.isError === true,
	};
}

/** 工具卡片：调用参数来自 toolCall 块（或实时帧），结果来自 toolResult 条目（或 tool_execution_end）。 */
export function ToolCard({
	toolCallId,
	name,
	args,
	intent,
	streamingArgs,
}: {
	toolCallId: string;
	name: string;
	args?: unknown;
	intent?: string;
	streamingArgs?: boolean;
}): ReactNode {
	const ctx = useTranscriptContext();
	const live = ctx.liveTools[toolCallId];
	const entryResult = ctx.toolResults.get(toolCallId);
	const result = entryResult
		? toolResultLike(entryResult)
		: live?.ended
			? toolResultLike(live.result)
			: undefined;
	const running =
		!result && (Boolean(live && !live.ended) || streamingArgs === true);
	const partial =
		live && !live.ended
			? resultTextOf(toolResultLike(live.partialResult))
			: undefined;
	return (
		<ToolView
			name={live?.toolName ?? name}
			args={args ?? live?.args}
			result={
				result
					? { ...result, isError: result.isError || live?.isError }
					: undefined
			}
			running={running}
			intent={intent ?? live?.intent}
			partial={partial}
			host={{
				hasAgent: () => true,
				openAgent: (id) => ctx.openSubagent(id),
			}}
		/>
	);
}

/** 回合脚注里的单个读数胶囊。 */
const META_CHIP =
	"inline-flex h-6 items-center gap-1 whitespace-nowrap rounded-md border border-border px-1.5 tabular-nums [&_svg]:text-faint";

/** 回合脚注：模型与用量读数（每项一个细描边胶囊）+ 消息操作。 */
function TurnMeta({
	message,
	entryId,
	text,
}: {
	message: JsonRecord;
	entryId?: string;
	text: string;
}) {
	const { t } = useTranslation();
	const usage = isRecord(message.usage) ? message.usage : null;
	const n = (v: unknown) => (typeof v === "number" ? v : 0);
	const cost = usage && isRecord(usage.cost) ? n(usage.cost.total) : 0;
	const model = typeof message.model === "string" ? message.model : "";
	return (
		<div className="mt-2 flex flex-wrap items-center gap-1.5 text-2xs text-muted">
			{model && (
				<span className={`${META_CHIP} min-w-0 max-w-full font-medium text-fg`}>
					<Icon name="sparkle" size={12} aria-hidden className="shrink-0" />
					<span className="min-w-0 truncate">{model}</span>
				</span>
			)}
			{usage && (
				<>
					<span className={META_CHIP}>
						<Stat icon="send" label={t("stat.input")}>
							{formatTokens(n(usage.input))}
						</Stat>
					</span>
					<span className={META_CHIP}>
						<Stat icon="arrowDown" label={t("stat.output")}>
							{formatTokens(n(usage.output))}
						</Stat>
					</span>
					{n(usage.cacheRead) > 0 && (
						<span className={META_CHIP}>
							<Stat icon="database" label={t("stat.cache")}>
								{formatTokens(n(usage.cacheRead))}
							</Stat>
						</span>
					)}
					{cost > 0 && (
						<span className={META_CHIP}>
							<Stat icon="coin" label={t("stat.cost")}>
								{formatCost(cost)}
							</Stat>
						</span>
					)}
				</>
			)}
			{typeof message.duration === "number" && (
				<span className={META_CHIP}>
					<Stat icon="timer" label={t("stat.duration")}>
						{formatDuration(message.duration)}
					</Stat>
				</span>
			)}
			<span className="flex-1" />
			<MessageMenu entryId={entryId} text={text} />
		</div>
	);
}

function AssistantMessage({
	message,
	entryId,
	live,
}: {
	message: JsonRecord;
	entryId?: string;
	live: boolean;
}) {
	const { t } = useTranslation();
	const content = Array.isArray(message.content) ? message.content : [];
	const lastIdx = content.length - 1;
	const stop =
		typeof message.stopReason === "string" ? message.stopReason : undefined;
	const error =
		typeof message.errorMessage === "string" ? message.errorMessage : undefined;
	return (
		<div className="group/msg relative py-1">
			{content.map((b, i) => {
				if (!isRecord(b)) return null;
				const key = `${i}-${String(b.type)}`;
				const streaming = live && i === lastIdx;
				switch (b.type) {
					case "text":
						return typeof b.text === "string" && b.text ? (
							<div key={key} className={streaming ? "stream-caret" : ""}>
								<Markdown text={b.text} streaming={streaming} />
							</div>
						) : null;
					case "thinking":
						return typeof b.thinking === "string" && b.thinking ? (
							<ThinkingBlock
								key={key}
								text={b.thinking}
								streaming={streaming}
							/>
						) : null;
					case "redactedThinking":
						return (
							<div
								key={key}
								className="my-0.5 flex h-7 items-center gap-2 text-faint text-xs italic"
							>
								<Icon name="brain" size={14} className="shrink-0" />
								{t("transcript.redactedThinking")}
							</div>
						);
					case "toolCall":
						return (
							<ToolCard
								key={typeof b.id === "string" && b.id ? b.id : key}
								toolCallId={typeof b.id === "string" ? b.id : ""}
								name={typeof b.name === "string" ? b.name : "tool"}
								args={b.arguments}
								intent={typeof b.intent === "string" ? b.intent : undefined}
								streamingArgs={streaming}
							/>
						);
					case "image":
						return <Images key={key} content={[b]} />;
					default:
						return null;
				}
			})}
			{live && content.length === 0 && (
				<div className="stream-caret text-faint text-sm" />
			)}
			{(stop === "error" || stop === "aborted") && (
				<div className="mt-1.5">
					<Callout
						tone={stop === "error" ? "err" : "neutral"}
						icon={stop === "error" ? "alert" : "stop"}
					>
						{stop === "aborted"
							? t("transcript.aborted")
							: t("transcript.error", { message: error ?? "" })}
					</Callout>
				</div>
			)}
			{!live && (
				<TurnMeta
					message={message}
					entryId={entryId}
					text={textOfContent(content)}
				/>
			)}
		</div>
	);
}

function ExecutionMessage({ message }: { message: JsonRecord }) {
	const { t } = useTranslation();
	const isBash = message.role === "bashExecution";
	const code = String((isBash ? message.command : message.code) ?? "");
	const exit =
		typeof message.exitCode === "number" ? message.exitCode : undefined;
	return (
		<div className="overflow-hidden rounded-lg border border-border bg-inset text-xs">
			<div className="flex min-w-0 items-center gap-2 border-border border-b px-3 py-1.5">
				<Icon
					name={isBash ? "terminal" : "code"}
					size={13}
					className="shrink-0 text-faint"
				/>
				<span className="min-w-0 flex-1 truncate font-mono text-fg">
					{isBash ? code : code.split("\n")[0]}
				</span>
				{message.excludeFromContext === true && (
					<Badge>{t("transcript.excluded")}</Badge>
				)}
				{message.cancelled === true && (
					<Badge tone="warn">{t("transcript.cancelled")}</Badge>
				)}
				{exit !== undefined && (
					<Badge tone={exit === 0 ? "ok" : "err"}>{`exit ${exit}`}</Badge>
				)}
			</div>
			<pre className="max-h-72 overflow-auto whitespace-pre-wrap px-3 py-2 font-mono text-fg">
				{String(message.output ?? "")}
			</pre>
		</div>
	);
}

function CustomMessage({ message }: { message: JsonRecord }) {
	return (
		<div className="rounded-lg border border-border-strong border-dashed px-3 py-2">
			<div className="mb-1.5">
				<Badge icon="message">
					{String(message.customType ?? message.role ?? "")}
				</Badge>
			</div>
			<Markdown text={textOfContent(message.content)} />
			<Images content={message.content} />
		</div>
	);
}

function SummaryCard({
	icon,
	label,
	summary,
	extra,
}: {
	icon: IconName;
	label: string;
	summary: string;
	extra?: string;
}) {
	const [open, setOpen] = useState(false);
	return (
		<section className="my-1 overflow-hidden rounded-lg border border-border bg-panel">
			<button
				type="button"
				className="flex h-9 w-full min-w-0 items-center gap-2 px-3 text-left text-muted text-xs hover:bg-hover/60"
				onClick={() => setOpen((v) => !v)}
				aria-expanded={open}
			>
				<Icon name={icon} size={13} className="shrink-0 text-faint" />
				<span className="truncate font-medium">{label}</span>
				{extra && <Badge>{extra}</Badge>}
				<span className="flex-1" />
				<Icon
					name="chevronRight"
					size={12}
					className={`shrink-0 text-ghost transition-transform duration-150 ${open ? "rotate-90" : ""}`}
				/>
			</button>
			{open && (
				<div className="border-border border-t px-3 py-2">
					<Markdown text={summary} />
				</div>
			)}
		</section>
	);
}

export function CompactionCard({ entry }: { entry: JsonRecord }) {
	const { t } = useTranslation();
	const before =
		typeof entry.tokensBefore === "number"
			? formatTokens(entry.tokensBefore)
			: "?";
	const after =
		typeof entry.tokensAfter === "number"
			? formatTokens(entry.tokensAfter)
			: "?";
	return (
		<SummaryCard
			icon="minimize"
			label={t("transcript.compaction")}
			extra={`${before} → ${after}`}
			summary={String(entry.shortSummary ?? entry.summary ?? "")}
		/>
	);
}

export function BranchSummaryCard({ entry }: { entry: JsonRecord }) {
	const { t } = useTranslation();
	return (
		<SummaryCard
			icon="fork"
			label={t("transcript.branchSummary")}
			summary={String(entry.summary ?? "")}
		/>
	);
}

export function MessageView({
	message,
	entryId,
	live,
}: {
	message: JsonRecord;
	entryId?: string;
	live: boolean;
}): ReactNode {
	const { t } = useTranslation();
	switch (message.role) {
		case "user":
			return <UserMessage message={message} entryId={entryId} live={live} />;
		case "assistant":
			return (
				<AssistantMessage message={message} entryId={entryId} live={live} />
			);
		case "bashExecution":
		case "pythonExecution":
			return <ExecutionMessage message={message} />;
		case "custom":
		case "hookMessage":
			return <CustomMessage message={message} />;
		case "branchSummary":
			return <BranchSummaryCard entry={message} />;
		case "compactionSummary":
			return <CompactionCard entry={message} />;
		case "fileMention": {
			const files = Array.isArray(message.files) ? message.files : [];
			return (
				<div className="flex flex-wrap gap-1.5">
					{files.map((f, i) => {
						const path = isRecord(f) ? String(f.path ?? "") : "";
						return (
							<span
								// biome-ignore lint/suspicious/noArrayIndexKey: 文件列表无稳定 id
								key={i}
								className={`${CHIP} text-muted`}
								title={path}
							>
								<Icon
									name={fileIcon(path)}
									size={13}
									className="shrink-0 text-faint"
								/>
								<span className="max-w-72 truncate">{path}</span>
							</span>
						);
					})}
				</div>
			);
		}
		case "developer":
			return (
				<SummaryCard
					icon="info"
					label={t("transcript.developer")}
					summary={textOfContent(message.content)}
				/>
			);
		default:
			return null;
	}
}

export const MemoMessageView = memo(MessageView);
