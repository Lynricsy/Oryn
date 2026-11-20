/**
 * Artifacts 工具（M14，「agent 工具」表里的全部工具；orb / Runner 的扩展工具与全局助手的宿主工具同名）。
 * 摘要：`create_artifact` →「{类型} · 已创建 · v{n}」，`update_artifact` →「已更新 · v{from}→v{to}」（只改名时「已改名」），
 * `restore_artifact_version` →「已恢复 · v{被恢复的版本}」，`delete_artifact` →「已删除」；其余工具显示工具名与目标标题。
 * 输入段是通用渲染的完整参数。输出段：前几种渲染一张 `ArtifactCard form="row"`，数据只取 `result.details`
 * （`{op, id, title, kind, version, fromVersion?}`），不再发请求；点击调 `host.openArtifact(id)`（只读的分享页不注入，
 * 卡片不可点，分享出去的转录只带标题、类型与版本号）。其余工具与出错的调用给原始结果（JSON 时按 JSON 高亮）。
 * `details` 残缺或类型不对时一律当作没有。
 */
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { ArtifactCard } from "../../components/artifacts/ArtifactCard";
import { isArtifactKind, kindLabelKey } from "../../lib/artifacts/kinds";
import { genericRenderer } from "../generic";
import { CodeBlock, JsonResultText, Output, ResultImages } from "../parts";
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

function Summary(props: ToolRenderProps): ReactNode {
	const { t } = useTranslation();
	const card = artifactCardOf(props);
	const what = target(props);
	let state: string | null = null;
	if (card)
		switch (props.name) {
			case "create_artifact":
				state = t("artifacts.tool.created", {
					kind: t(kindLabelKey(card.kind)),
					version: card.version,
				});
				break;
			case "update_artifact":
				state =
					card.fromVersion === null
						? t("artifacts.tool.renamed")
						: card.fromVersion === card.version
							? t("artifacts.tool.updatedTo", { to: card.version })
							: t("artifacts.tool.updated", {
									from: card.fromVersion,
									to: card.version,
								});
				break;
			case "restore_artifact_version":
				state = t("artifacts.tool.restored", {
					version: card.fromVersion ?? num(props.args?.version) ?? card.version,
				});
				break;
			case "import_artifact":
				state = t("artifacts.tool.imported", { version: card.version });
				break;
			case "restore_artifact":
				state = t("artifacts.tool.undeleted");
				break;
		}
	if (
		props.name === "delete_artifact" &&
		!props.result?.isError &&
		props.result
	)
		state = t("artifacts.tool.deleted");
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
			{state && <span className="text-faint"> · {state}</span>}
		</>
	);
}

/**
 * `read_artifact` 的结果文本：一行 JSON 头（`{id, title, files, path, …}`，Go 紧凑编码不含换行）、空行、文件内容
 * （`internal/artifacts/agent.go` 的 agentRead）。头不是带 `path` 的 JSON 对象时返回 null。
 */
function readEnvelope(
	text: string,
): { head: string; path: string; content: string } | null {
	const sep = text.indexOf("\n\n");
	let parsed: unknown;
	try {
		parsed = JSON.parse(sep === -1 ? text : text.slice(0, sep));
	} catch {
		return null;
	}
	const path = isRecord(parsed) ? str(parsed.path) : null;
	if (path === null) return null;
	return {
		head: JSON.stringify(parsed, null, 2),
		path,
		content: sep === -1 ? "" : text.slice(sep + 2),
	};
}

/** 读到的文件：头按 JSON 高亮，内容按返回的文件路径高亮。 */
function ReadResult({ result }: Pick<ToolRenderProps, "result">): ReactNode {
	const envelope = result?.isError ? null : readEnvelope(resultTextOf(result));
	if (!envelope) return <JsonResultText result={result} />;
	return (
		<>
			<Output text={envelope.head} lang="json" variant="code" maxLines={10} />
			{envelope.content.trim() && (
				<CodeBlock
					code={envelope.content}
					lang={languageFromPath(envelope.path)}
					title={envelope.path}
				/>
			)}
		</>
	);
}

function Result(props: ToolRenderProps): ReactNode {
	const card = CARD_TOOLS.includes(props.name) ? artifactCardOf(props) : null;
	if (!card)
		return (
			<>
				<ResultImages result={props.result} />
				{props.name === "read_artifact" ? (
					<ReadResult result={props.result} />
				) : (
					<JsonResultText result={props.result} />
				)}
			</>
		);
	const open = props.host?.openArtifact;
	return (
		<div data-testid="artifact-tool-card" className="p-2">
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
	Input: genericRenderer.Input,
	Result,
};
