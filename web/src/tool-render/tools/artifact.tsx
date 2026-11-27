/**
 * Artifacts 工具（M14，「agent 工具」表里的全部工具；orb / Runner 的扩展工具与全局助手的宿主工具同名）。
 * 头部：读类工具写工具名 + 目标标题；结果读数（右侧）写这次调用把 artifact 变成了什么：
 * `create_artifact` →「{类型} · 已创建 · v{n}」，`update_artifact` →「已更新 · v{from}→v{to}」（只改名时「已改名」），
 * `restore_artifact_version` →「已恢复 · v{被恢复的版本}」，`delete_artifact` →「已删除」；列表类工具写项数。
 * 输入段按参数的原生形态：页面 HTML / 文件内容按语言高亮，`edits` 逐条写成删改对照，备注单列，其余参数是 JSON 树。
 * 输出段：前几种渲染一张 `ArtifactCard form="row"`，数据只取 `result.details`
 * （`{op, id, title, kind, version, fromVersion?}`），不再发请求；点击调 `host.openArtifact(id)`（只读的分享页不注入，
 * 卡片不可点，分享出去的转录只带标题、类型与版本号）。`read_artifact` 分开文件头与文件内容；删除写出清除期限；
 * 其余工具给结果原文（JSON 树 / 文本），出错时错误块（控制面给的 `error.message` 优先）。
 * `details` 残缺或类型不对时一律当作没有。
 */
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { ArtifactCard } from "../../components/artifacts/ArtifactCard";
import { isArtifactKind, kindLabelKey } from "../../lib/artifacts/kinds";
import { formatDateTime } from "../../lib/format";
import { ArgsTree, parseJsonText, SmartResult } from "../generic";
import {
	CodeBlock,
	CodeLines,
	DiffBlock,
	ErrorBlock,
	FileHead,
	JsonTree,
	Note,
} from "../parts";
import type { ToolRenderer, ToolRenderProps } from "../types";
import {
	detailsRecord,
	isRecord,
	languageFromPath,
	num,
	resultTextOf,
	str,
	truncate,
} from "../util";

/** artifact 工具名（与「agent 工具」表、`internal/artifacts/agent.go` 的操作名一致）。 */
export const ARTIFACT_TOOLS = [
	"create_artifact",
	"read_artifact",
	"update_artifact",
	"list_artifacts",
	"delete_artifact",
	"list_artifact_versions",
	"restore_artifact_version",
	"read_artifact_guide",
	"read_artifact_comments",
	"reply_artifact_comment",
	"resolve_artifact_comment",
	"screenshot_artifact",
	"read_artifact_data",
	"write_artifact_data",
	"export_artifact",
	"import_artifact",
	"list_deleted_artifacts",
	"restore_artifact",
	"pull_artifact",
] as const;

/** 会写入的工具（工具图标的类别色调用）。 */
export const ARTIFACT_WRITE_TOOLS: readonly string[] = [
	"create_artifact",
	"update_artifact",
	"restore_artifact_version",
	"delete_artifact",
	"reply_artifact_comment",
	"resolve_artifact_comment",
	"write_artifact_data",
	"import_artifact",
	"restore_artifact",
];

/** 正文里带卡片的工具。 */
const CARD_TOOLS: readonly string[] = [
	"create_artifact",
	"update_artifact",
	"restore_artifact_version",
	"import_artifact",
	"restore_artifact",
];

/** 转录卡片的数据：从 `details` 里取，字段残缺时为 null。 */
export interface ArtifactToolCard {
	id: string;
	title: string;
	kind: string;
	version: number;
	fromVersion: number | null;
}

export function artifactCardOf(
	props: ToolRenderProps,
): ArtifactToolCard | null {
	if (props.result?.isError) return null;
	const d = detailsRecord(props.result);
	const id = str(d?.id);
	const version = num(d?.version);
	if (!d || !id || version === null) return null;
	return {
		id,
		title: str(d.title) ?? id,
		kind: str(d.kind) ?? "web",
		version,
		fromVersion: num(d.fromVersion),
	};
}

/** 摘要里的目标：结果里的标题，否则调用参数里的标题 / 查询 / 主题 / id。 */
function target(props: ToolRenderProps): string | null {
	const d = detailsRecord(props.result);
	const a = props.args ?? {};
	return (
		str(d?.title) ??
		str(a.title) ??
		str(a.query) ??
		str(a.topic) ??
		str(a.collection) ??
		str(a.id)
	);
}

/** 这次调用的结果状态（已创建 · v1、已更新 · v1→v2……）；读类工具、运行中与出错时为 null。 */
function useStateText(props: ToolRenderProps): string | null {
	const { t } = useTranslation();
	const card = artifactCardOf(props);
	if (props.name === "delete_artifact" && props.result && !props.result.isError)
		return t("artifacts.tool.deleted");
	if (!card) return null;
	switch (props.name) {
		case "create_artifact":
			return t("artifacts.tool.created", {
				kind: t(kindLabelKey(card.kind)),
				version: card.version,
			});
		case "update_artifact":
			return card.fromVersion === null
				? t("artifacts.tool.renamed")
				: card.fromVersion === card.version
					? t("artifacts.tool.updatedTo", { to: card.version })
					: t("artifacts.tool.updated", {
							from: card.fromVersion,
							to: card.version,
						});
		case "restore_artifact_version":
			return t("artifacts.tool.restored", {
				version: card.fromVersion ?? num(props.args?.version) ?? card.version,
			});
		case "import_artifact":
			return t("artifacts.tool.imported", { version: card.version });
		case "restore_artifact":
			return t("artifacts.tool.undeleted");
		default:
			return null;
	}
}

function Summary(props: ToolRenderProps): ReactNode {
	const { t } = useTranslation();
	const what = target(props);
	const state = useStateText(props);
	// 读类工具（以及还在运行、出错的调用）：工具名在前
	const label =
		state === null && (ARTIFACT_TOOLS as readonly string[]).includes(props.name)
			? t(
					`artifacts.tool.names.${props.name as (typeof ARTIFACT_TOOLS)[number]}`,
				)
			: null;
	return (
		<>
			{label && <span>{label}</span>}
			{what && (
				<span className="tv-path">
					{label ? " " : ""}
					{truncate(what.replace(/\s+/g, " "), 80)}
				</span>
			)}
		</>
	);
}

/** 右侧读数：写类工具的结果状态；列表类工具的项数。 */
function Meta(props: ToolRenderProps): ReactNode {
	const { t } = useTranslation();
	const state = useStateText(props);
	if (state)
		return (
			<span
				className={
					props.name === "delete_artifact" ? "tv-meta-warn" : "tv-meta-ok"
				}
			>
				{state}
			</span>
		);
	if (!props.result || props.result.isError) return null;
	const json = parseJsonText(resultTextOf(props.result));
	const list = Array.isArray(json)
		? json
		: isRecord(json)
			? Object.values(json).find(Array.isArray)
			: undefined;
	return Array.isArray(list) && props.name.startsWith("list_") ? (
		<span>{t("toolRender.json.items", { count: list.length })}</span>
	) : null;
}

// ---- 输入段 ----

/** `edits[]` 的一条：`{path?, oldText, newText}` 写成删改对照。 */
function EditDiff({ edit }: { edit: unknown }): ReactNode {
	if (!isRecord(edit)) return <JsonTree value={edit} />;
	const oldText = str(edit.oldText) ?? "";
	const newText = str(edit.newText) ?? "";
	const path = str(edit.path);
	const diff = [
		...(oldText ? oldText.split("\n").map((l) => `-${l}`) : []),
		...(newText ? newText.split("\n").map((l) => `+${l}`) : []),
	].join("\n");
	return (
		<div className="tv-out">
			{path && <div className="tv-out-title">{path}</div>}
			<DiffBlock
				diff={diff}
				maxLines={16}
				lang={path ? languageFromPath(path) : "html"}
			/>
		</div>
	);
}

/** 输入段里专门排版过的参数；其余按 JSON 树列出。 */
const SHAPED_KEYS = ["html", "files", "edits", "note"];

function Input({ args }: ToolRenderProps): ReactNode {
	const { t } = useTranslation();
	const html = str(args.html);
	const files = Array.isArray(args.files) ? args.files : [];
	const edits = Array.isArray(args.edits) ? args.edits : [];
	const note = str(args.note);
	const rest: Record<string, unknown> = {};
	for (const key in args) if (!SHAPED_KEYS.includes(key)) rest[key] = args[key];
	// 形状不对的 files / edits 原样留在 JSON 树里
	if (args.files !== undefined && !Array.isArray(args.files))
		rest.files = args.files;
	if (args.edits !== undefined && !Array.isArray(args.edits))
		rest.edits = args.edits;
	return (
		<>
			<ArgsTree args={rest} />
			{note && (
				<div className="tv-art-note">
					<span className="tv-faint">{t("toolRender.artifact.note")}</span>
					<span>{note}</span>
				</div>
			)}
			{html !== null && (
				<CodeBlock code={html} lang="html" title="index.html" maxLines={12} />
			)}
			{files.map((f, i) => {
				const path = isRecord(f) ? str(f.path) : null;
				const content = isRecord(f) ? str(f.content) : null;
				if (!path || content === null) return <JsonTree key={i} value={f} />;
				return (
					<CodeBlock
						key={path}
						code={content}
						lang={languageFromPath(path)}
						title={path}
						maxLines={12}
					/>
				);
			})}
			{edits.map((e, i) => (
				<EditDiff key={i} edit={e} />
			))}
		</>
	);
}

// ---- 输出段 ----

/**
 * `read_artifact` 的结果文本：一行 JSON 头（`{id, title, files, path, …}`，Go 紧凑编码不含换行）、空行、文件内容
 * （`internal/artifacts/agent.go` 的 agentRead）。头不是带 `path` 的 JSON 对象时返回 null。
 */
function readEnvelope(
	text: string,
): { head: Record<string, unknown>; path: string; content: string } | null {
	const sep = text.indexOf("\n\n");
	let parsed: unknown;
	try {
		parsed = JSON.parse(sep === -1 ? text : text.slice(0, sep));
	} catch {
		return null;
	}
	if (!isRecord(parsed)) return null;
	const path = str(parsed.path);
	if (path === null) return null;
	return {
		head: parsed,
		path,
		content: sep === -1 ? "" : text.slice(sep + 2),
	};
}

/** 读到的文件：文件头条（右侧版本号）+ 带行号、按路径高亮的内容；文件头 JSON 收成一行，点开再看。 */
function ReadResult({ result }: Pick<ToolRenderProps, "result">): ReactNode {
	const envelope = result?.isError ? null : readEnvelope(resultTextOf(result));
	if (!envelope) return <SmartResult result={result} />;
	const version = num(envelope.head.version);
	// 从头读起时行号就是文件行号；按偏移读的片段不标行号
	const fromStart = (num(envelope.head.offset) ?? 0) === 0;
	return (
		<>
			<JsonTree value={envelope.head} openDepth={0} />
			<FileHead path={envelope.path}>
				{version !== null && <span className="tv-faint">v{version}</span>}
			</FileHead>
			{envelope.content.trim() && (
				<CodeLines
					rows={envelope.content
						.replace(/\n$/, "")
						.split("\n")
						.map((text, i) => ({
							kind: "ctx" as const,
							no: fromStart ? i + 1 : null,
							text,
						}))}
					lang={languageFromPath(envelope.path)}
					maxLines={20}
				/>
			)}
		</>
	);
}

/** 出错：控制面给的 `{error: {code, message}}` 优先，否则结果文本。 */
function ArtifactError({
	result,
}: {
	result: NonNullable<ToolRenderProps["result"]>;
}): ReactNode {
	const { t } = useTranslation();
	const d = detailsRecord(result);
	const err = d && isRecord(d.error) ? d.error : null;
	const message = err ? str(err.message) : null;
	const code = err ? str(err.code) : null;
	const text =
		message !== null
			? `${code ? `${code}: ` : ""}${message}`
			: resultTextOf(result).trim();
	return <ErrorBlock text={text || t("toolRender.generic.failed")} />;
}

function Result(props: ToolRenderProps): ReactNode {
	const { t } = useTranslation();
	const { result } = props;
	if (!result) return null;
	if (result.isError) return <ArtifactError result={result} />;
	const card = CARD_TOOLS.includes(props.name) ? artifactCardOf(props) : null;
	if (!card) {
		if (props.name === "read_artifact") return <ReadResult result={result} />;
		const deleted =
			props.name === "delete_artifact"
				? parseJsonText(resultTextOf(result))
				: undefined;
		const purgeAt = isRecord(deleted) ? num(deleted.purgeAt) : null;
		if (purgeAt !== null)
			return (
				<Note>
					{t("toolRender.artifact.purgeAt", { when: formatDateTime(purgeAt) })}
				</Note>
			);
		return <SmartResult result={result} />;
	}
	const open = props.host?.openArtifact;
	return (
		<div data-testid="artifact-tool-card" className="tv-art-card">
			<ArtifactCard
				form="row"
				artifact={{
					id: card.id,
					title: card.title,
					kind: isArtifactKind(card.kind) ? card.kind : "web",
					version: card.version,
				}}
				onOpen={open ? (id) => open(id) : undefined}
			/>
		</div>
	);
}

export const artifactRenderer: ToolRenderer = {
	Summary,
	Meta,
	Input,
	Result,
};
