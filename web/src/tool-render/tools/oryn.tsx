/**
 * Oryn 宿主工具（M9，协调对话里的 `create_thread` / `send_message` / `archive_thread` / `list_threads` /
 * `read_thread` / `set_schedule` / `list_schedules` / `get_stats` / `list_projects`；M19 加上全局助手的
 * `update_thread` / `abort_thread` / `set_thread_model` / `fork_thread` / `wake_thread` / `delete_thread` / `list_runners`）。
 * 摘要行只用调用参数，读数是结果里的线程数；输入段是参数的 JSON 树，发出的消息与提示词单独按引文给出全文；
 * 输出段里 `details.threadIds` / `details.threads` / `details.thread` 指向的线程渲染成实时的 `ThreadCard`
 * （数据取线程索引），卡片只是补充：下面始终是原始结果（JSON 树，有卡片时深层先收起；`read_thread` 读到的消息
 * 在这里，M9 审查 W7）；出错时错误块。卡片超过 12 张时 `+N` 可展开其余。
 * `details` 残缺或类型不对时一律当作没有。
 * 与 collab-web 移植来的渲染器不同，这一组只在 Oryn 里出现，可以直接用应用自己的组件。
 */
import { type ReactNode, useState } from "react";
import { useTranslation } from "react-i18next";
import { ThreadCard } from "../../components/space/ThreadCard";
import { parseTitleCard } from "../../lib/title";
import { useIndex } from "../../state/stores";
import { ArgsTree, parseJsonText, SmartResult } from "../generic";
import { Badge, Badges, ErrorBlock, JsonTree } from "../parts";
import type { ToolRenderer, ToolRenderProps } from "../types";
import { detailsRecord, num, resultTextOf, str, truncate } from "../util";

/** 卡片先列这么多条（`list_threads` 可能有上百条），其余点 `+N` 展开。 */
const MAX_CARDS = 12;

/** 从 `details` 里收集线程 id：`threadIds[]`、`threads[].id`、`thread.id`，去重保序。 */
export function detailThreadIds(details: unknown): string[] {
	if (!details || typeof details !== "object") return [];
	const d = details as Record<string, unknown>;
	const out: string[] = [];
	const push = (v: unknown) => {
		if (typeof v === "string" && v !== "" && !out.includes(v)) out.push(v);
	};
	if (Array.isArray(d.threadIds)) for (const v of d.threadIds) push(v);
	if (Array.isArray(d.threads))
		for (const v of d.threads)
			if (v && typeof v === "object") push((v as Record<string, unknown>).id);
	if (d.thread && typeof d.thread === "object")
		push((d.thread as Record<string, unknown>).id);
	return out;
}

/** 线程 id 在摘要里显示成标题（索引里有时），否则原样。 */
function ThreadRef({ id }: { id: string }): ReactNode {
	const title = useIndex((s) => s.threads[id]?.title);
	const text = title ? parseTitleCard(title).text : "";
	return <span className="tv-path">{text || id}</span>;
}

function Text({
	value,
	max = 80,
}: {
	value: unknown;
	max?: number;
}): ReactNode {
	const s = str(value);
	return s ? <span>{truncate(s.replace(/\s+/g, " "), max)}</span> : null;
}

function Summary({ name, args }: ToolRenderProps): ReactNode {
	const { t } = useTranslation();
	const a = args ?? {};
	const threadId = str(a.threadId);
	const tags: string[] = [];
	switch (name) {
		case "create_thread":
			for (const k of ["project", "space", "runner", "orbType"] as const)
				if (str(a[k])) tags.push(str(a[k]) as string);
			return (
				<>
					<Text value={a.title ?? a.prompt} />
					<Badges items={tags} />
				</>
			);
		case "send_message":
			if (a.steer === true) tags.push("steer");
			return (
				<>
					{threadId ? (
						<ThreadRef id={threadId} />
					) : (
						str(a.space) && <span className="tv-path">{str(a.space)}</span>
					)}
					<span className="text-faint"> ← </span>
					<Text value={a.message} max={60} />
					<Badges items={tags} />
				</>
			);
		case "archive_thread":
			return (
				<>
					{threadId && <ThreadRef id={threadId} />}
					{a.outcome === "shipped" && (
						<Badges
							items={[
								<Badge key="shipped" tone="ok">
									{t("space.shipped")}
								</Badge>,
							]}
						/>
					)}
				</>
			);
		case "update_thread":
		case "abort_thread":
		case "wake_thread":
		case "delete_thread":
			for (const k of ["title", "space"] as const)
				if (str(a[k])) tags.push(str(a[k]) as string);
			if (a.pinned === true) tags.push("pinned");
			return (
				<>
					{threadId && <ThreadRef id={threadId} />}
					<Badges items={tags} />
				</>
			);
		case "set_thread_model":
		case "fork_thread":
			for (const k of ["model", "thinkingLevel", "env"] as const)
				if (str(a[k])) tags.push(str(a[k]) as string);
			return (
				<>
					{threadId && <ThreadRef id={threadId} />}
					<Badges items={tags} />
				</>
			);
		case "read_thread":
			return (
				<>
					{threadId && <ThreadRef id={threadId} />}
					{num(a.tail) !== null && <Badges items={[`tail ${num(a.tail)}`]} />}
				</>
			);
		case "set_schedule": {
			for (const k of ["action", "mode", "kind"] as const)
				if (str(a[k])) tags.push(str(a[k]) as string);
			const spec =
				str(a.cron) ??
				str(a.every) ??
				(num(a.at) !== null ? String(a.at) : null);
			return (
				<>
					{str(a.name) ? (
						<Text value={a.name} />
					) : threadId ? (
						<ThreadRef id={threadId} />
					) : null}
					{spec && <span className="tv-path"> {spec}</span>}
					<Badges items={tags} />
				</>
			);
		}
		case "list_threads":
		case "list_schedules":
			for (const k of ["scope", "state", "project", "query"] as const)
				if (str(a[k])) tags.push(str(a[k]) as string);
			if (a.archived === true) tags.push("archived");
			return <Badges items={tags} />;
		case "get_stats":
			if (str(a.groupBy)) tags.push(str(a.groupBy) as string);
			return <Badges items={tags} />;
		default:
			return null;
	}
}

/** 右侧读数：结果里列出的线程数（列表类工具，或一次涉及多个线程时）。 */
function Meta({ name, result }: ToolRenderProps): ReactNode {
	const { t } = useTranslation();
	if (!result || result.isError) return null;
	const n = detailThreadIds(detailsRecord(result)).length;
	return n > 1 || (n > 0 && name.startsWith("list_")) ? (
		<span>{t("toolRender.oryn.threads", { count: n })}</span>
	) : null;
}

/** 输入段：发出的消息、新线程的提示词按引文排版（全文），其余参数是 JSON 树。 */
function Input({ name, args }: ToolRenderProps): ReactNode {
	const a = args ?? {};
	const quoteKey =
		name === "send_message"
			? "message"
			: name === "create_thread" || name === "set_schedule"
				? "prompt"
				: null;
	const quote = quoteKey ? str(a[quoteKey]) : null;
	if (!quoteKey || quote === null) return <ArgsTree args={a} />;
	const rest: Record<string, unknown> = {};
	for (const k in a) if (k !== quoteKey) rest[k] = a[k];
	return (
		<>
			<ArgsTree args={rest} />
			<blockquote className="tv-oryn-quote tv-prose">{quote}</blockquote>
		</>
	);
}

function Result(props: ToolRenderProps): ReactNode {
	const { t } = useTranslation();
	const [all, setAll] = useState(false);
	const { result } = props;
	if (!result) return null;
	if (result.isError)
		return (
			<ErrorBlock
				text={resultTextOf(result).trim() || t("toolRender.generic.failed")}
			/>
		);
	const ids = detailThreadIds(detailsRecord(result));
	const shown = all ? ids : ids.slice(0, MAX_CARDS);
	const json = parseJsonText(resultTextOf(result));
	return (
		<>
			{ids.length > 0 && (
				<ul data-testid="oryn-tool-threads" className="tv-oryn-threads">
					{shown.map((id) => (
						<li key={id}>
							<ThreadCard threadId={id} />
						</li>
					))}
					{ids.length > shown.length && (
						<li>
							<button
								type="button"
								className="tv-expand"
								aria-label={t("tool.oryn.showAll", { n: ids.length })}
								onClick={() => setAll(true)}
							>
								+{ids.length - shown.length}
							</button>
						</li>
					)}
				</ul>
			)}
			{/* 卡片只是补充：原始结果始终在（read_thread 读到的消息只在这里）；有卡片时 JSON 先收起深层 */}
			{ids.length > 0 && json !== undefined ? (
				<JsonTree value={json} openDepth={1} />
			) : (
				<SmartResult result={result} />
			)}
		</>
	);
}

export const orynRenderer: ToolRenderer = {
	Summary,
	Meta,
	Input,
	Result,
};

/** 宿主工具名（与 `internal/coord/profile.go` 的 `Tool*` 常量一致）。 */
export const ORYN_TOOLS = [
	"create_thread",
	"send_message",
	"archive_thread",
	"list_threads",
	"read_thread",
	"set_schedule",
	"list_schedules",
	"get_stats",
	"list_projects",
	"update_thread",
	"abort_thread",
	"set_thread_model",
	"fork_thread",
	"wake_thread",
	"delete_thread",
	"list_runners",
] as const;
