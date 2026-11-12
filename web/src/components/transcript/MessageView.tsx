/**
 * 转录中的单条消息渲染：按 omp 的信息量全量展示（思考块、工具卡片、用量行、错误、bash 执行、自定义消息等）。
 * 所有字段都按「可能残缺」处理——条目与流式帧都是外部 JSON。
 */
import { memo, type ReactNode, useState } from "react";
import { useTranslation } from "react-i18next";
import { formatCost, formatDuration, formatTokens } from "../../lib/format";
import { imageSrc } from "../../lib/image-src";
import type { JsonRecord } from "../../state/message-builder";
import { ToolView } from "../../tool-render/ToolView";
import type { ToolResultLike } from "../../tool-render/types";
import { isRecord, resultTextOf } from "../../tool-render/util";
import { Markdown } from "../markdown/Markdown";
import { ImageViewer } from "../ui/ImageViewer";
import { Icon } from "../ui/icons";
import { Menu } from "../ui/primitives";
import { useTranscriptContext } from "./context";

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
		<div className="absolute top-1 right-1 opacity-0 transition-opacity group-hover/msg:opacity-100 focus-within:opacity-100">
			<Menu
				trigger={(p) => (
					<button
						type="button"
						className="rounded p-1 text-faint hover:bg-hover hover:text-fg"
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
	const text = textOfContent(message.content);
	const irc = isRecord(message.parentIrc) ? message.parentIrc : null;
	return (
		<div
			className={`group/msg relative rounded-lg border border-border bg-raised px-3.5 py-2.5 ${live ? "opacity-80" : ""}`}
		>
			<div className="mb-1 flex items-center gap-2 text-faint text-xs">
				<Icon name="user" size={12} />
				<span>
					{irc
						? t("transcript.ircFrom", { from: String(irc.from ?? "") })
						: t("transcript.you")}
				</span>
				{message.synthetic === true && (
					<span className="rounded bg-inset px-1">
						{t("transcript.synthetic")}
					</span>
				)}
				{(message.steering === true || message.liveSteered === true) && (
					<span className="rounded bg-inset px-1">{t("transcript.steer")}</span>
				)}
			</div>
			<div className="whitespace-pre-wrap break-words text-sm">
				{irc ? String(irc.body ?? text) : text}
			</div>
			<Images content={message.content} />
			<MessageMenu entryId={entryId} text={text} />
		</div>
	);
}

function ThinkingBlock({
	text,
	streaming,
}: {
	text: string;
	streaming: boolean;
}) {
	const { t } = useTranslation();
	const [open, setOpen] = useState(true);
	return (
		<div className="my-1.5 border-border border-l-2 pl-3">
			<button
				type="button"
				className="flex items-center gap-1 text-faint text-xs hover:text-muted"
				onClick={() => setOpen((v) => !v)}
				aria-expanded={open}
			>
				<Icon name={open ? "chevronDown" : "chevronRight"} size={12} />
				{streaming ? t("transcript.thinkingLive") : t("transcript.thinking")}
			</button>
			{open && (
				<div
					className={`mt-1 whitespace-pre-wrap text-muted text-xs leading-relaxed ${streaming ? "stream-caret" : ""}`}
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
		<div className="my-1.5">
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
		</div>
	);
}

function UsageLine({ message }: { message: JsonRecord }) {
	const { t } = useTranslation();
	const usage = isRecord(message.usage) ? message.usage : null;
	if (!usage) return null;
	const n = (v: unknown) => (typeof v === "number" ? v : 0);
	const cost = isRecord(usage.cost) ? n(usage.cost.total) : 0;
	const parts = [
		typeof message.model === "string" ? message.model : "",
		t("transcript.usageIn", { n: formatTokens(n(usage.input)) }),
		t("transcript.usageOut", { n: formatTokens(n(usage.output)) }),
		n(usage.cacheRead) > 0
			? t("transcript.usageCache", { n: formatTokens(n(usage.cacheRead)) })
			: "",
		cost > 0 ? formatCost(cost) : "",
		typeof message.duration === "number"
			? formatDuration(message.duration)
			: "",
	].filter(Boolean);
	return (
		<div className="mt-1.5 font-mono text-[11px] text-faint">
			{parts.join(" · ")}
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
							<div key={key} className="my-1 text-faint text-xs italic">
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
				<div
					className={`mt-1.5 rounded-md px-2 py-1 text-xs ${stop === "error" ? "bg-err/10 text-err" : "bg-inset text-muted"}`}
				>
					{stop === "aborted"
						? t("transcript.aborted")
						: t("transcript.error", { message: error ?? "" })}
				</div>
			)}
			{!live && <UsageLine message={message} />}
			{!live && <MessageMenu entryId={entryId} text={textOfContent(content)} />}
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
		<div className="rounded-lg border border-border bg-inset font-mono text-xs">
			<div className="flex items-center gap-2 border-border border-b px-3 py-1.5 text-muted">
				<Icon name="terminal" size={12} />
				<span className="min-w-0 flex-1 truncate">
					{isBash ? `$ ${code}` : code.split("\n")[0]}
				</span>
				{message.excludeFromContext === true && (
					<span className="text-faint">{t("transcript.excluded")}</span>
				)}
				{message.cancelled === true && (
					<span className="text-warn">{t("transcript.cancelled")}</span>
				)}
				{exit !== undefined && (
					<span
						className={exit === 0 ? "text-ok" : "text-err"}
					>{`exit ${exit}`}</span>
				)}
			</div>
			<pre className="max-h-72 overflow-auto whitespace-pre-wrap px-3 py-2 text-fg">
				{String(message.output ?? "")}
			</pre>
		</div>
	);
}

function CustomMessage({ message }: { message: JsonRecord }) {
	return (
		<div className="rounded-lg border border-border border-dashed px-3 py-2">
			<div className="mb-1 text-faint text-xs">
				{String(message.customType ?? message.role ?? "")}
			</div>
			<Markdown text={textOfContent(message.content)} />
			<Images content={message.content} />
		</div>
	);
}

function SummaryCard({
	label,
	summary,
	extra,
}: {
	label: string;
	summary: string;
	extra?: string;
}) {
	const [open, setOpen] = useState(false);
	return (
		<div className="my-1 rounded-lg border border-border bg-raised">
			<button
				type="button"
				className="flex w-full items-center gap-2 px-3 py-2 text-left text-muted text-xs"
				onClick={() => setOpen((v) => !v)}
				aria-expanded={open}
			>
				<Icon name={open ? "chevronDown" : "chevronRight"} size={12} />
				<span className="font-medium">{label}</span>
				{extra && <span className="text-faint">{extra}</span>}
			</button>
			{open && (
				<div className="border-border border-t px-3 py-2">
					<Markdown text={summary} />
				</div>
			)}
		</div>
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
				<div className="flex flex-wrap gap-1.5 text-xs">
					{files.map((f, i) => (
						<span
							// biome-ignore lint/suspicious/noArrayIndexKey: 文件列表无稳定 id
							key={i}
							className="inline-flex items-center gap-1 rounded border border-border px-1.5 py-0.5 text-muted"
						>
							<Icon name="file" size={11} />
							{isRecord(f) ? String(f.path ?? "") : ""}
						</span>
					))}
				</div>
			);
		}
		case "developer":
			return (
				<SummaryCard
					label={t("transcript.developer")}
					summary={textOfContent(message.content)}
				/>
			);
		default:
			return null;
	}
}

export const MemoMessageView = memo(MessageView);
