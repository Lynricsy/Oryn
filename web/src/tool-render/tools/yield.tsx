/**
 * `yield` — structured data handed back by a subagent at the end of its run.
 * 输入段把提交的数据按「原生形态」展开：对象逐个字段（文字按 Markdown、字符串数组按列表、数字与布尔等宽、
 * 更深的结构按 JSON），失败提交（`error`）给错误说明；输出段只在有话要说时出现（中止、schema 校验放宽、工作池进度）。
 * 形状见 oryn-core `tools/yield.ts`（参数 `data | error | type | key`，details `YieldDetails`）。
 */
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import {
	ErrorBlock,
	JsonTree,
	MarkdownBlock,
	Note,
	PathText,
	ResultText,
} from "../parts";
import type { ToolRenderer, ToolRenderProps } from "../types";
import {
	detailsRecord,
	display,
	isRecord,
	normalizeWs,
	num,
	resultTextOf,
	str,
	truncate,
} from "../util";

/** `type` 参数：结果分节名（字符串或字符串数组）。 */
function sections(args: Record<string, unknown>): string[] {
	if (typeof args.type === "string") return [args.type];
	return Array.isArray(args.type)
		? args.type.filter((v): v is string => typeof v === "string")
		: [];
}

/** 看起来像仓库路径的字符串：数组里全是这种时按路径列表显示。 */
const PATH_RE = /^[\w.~@-]*\/[\w./@-]*$|^[\w-]+\.[a-z0-9]{1,6}$/i;

/** 一个字段值的原生形态。 */
function Value({ value }: { value: unknown }): ReactNode {
	if (typeof value === "string") {
		if (value.includes("\n") || value.length > 120)
			return <MarkdownBlock text={value} maxHeight={160} />;
		return <span className="tv-yield-text">{value}</span>;
	}
	if (typeof value === "number" || typeof value === "boolean" || value === null)
		return <span className="tv-yield-lit">{display(value) || "null"}</span>;
	if (
		Array.isArray(value) &&
		value.length > 0 &&
		value.every((v) => typeof v === "string" && !v.includes("\n"))
	) {
		const paths = value.every((v) => PATH_RE.test(v as string));
		return (
			<ul className="tv-yield-list">
				{value.map((v, i) => (
					<li key={i}>
						{paths ? <PathText path={v as string} /> : (v as string)}
					</li>
				))}
			</ul>
		);
	}
	return <JsonTree value={value} openDepth={1} />;
}

function Summary({ args }: ToolRenderProps): ReactNode {
	const { t } = useTranslation();
	const error = str(args.error);
	const labels = sections(args);
	const key = num(args.key);
	const data = args.data;
	let digest = "";
	if (isRecord(data)) {
		const lead = str(data.summary) ?? str(data.title) ?? str(data.result);
		digest = lead ?? Object.keys(data).join(" · ");
	} else if (data !== undefined) digest = display(data);
	return (
		<>
			{key !== null && (
				<span className="tv-faint">
					{t("toolRender.yield.item", { n: key })}
				</span>
			)}
			{labels.length > 0 && (
				<span className="tv-faint">{labels.join(", ")}</span>
			)}
			{error !== null ? (
				<span className="tv-err-text">{truncate(normalizeWs(error), 96)}</span>
			) : (
				digest && <span>{truncate(normalizeWs(digest), 96)}</span>
			)}
		</>
	);
}

/** 右侧读数：失败提交 / 中止为红，schema 校验被放宽为黄，工作池全部完成为绿；普通成功无读数（状态勾已说明）。 */
function Meta({ args, result }: ToolRenderProps): ReactNode {
	const { t } = useTranslation();
	const d = detailsRecord(result);
	if (
		str(d?.status) === "aborted" ||
		(str(args.error) !== null && result && !result.isError)
	)
		return <span className="tv-meta-err">{t("toolRender.yield.aborted")}</span>;
	if (d?.schemaOverridden === true)
		return (
			<span className="tv-meta-warn">
				{t("toolRender.yield.schemaOverridden")}
			</span>
		);
	if (d?.complete === true)
		return <span className="tv-meta-ok">{t("toolRender.yield.complete")}</span>;
	if (isRecord(args.data))
		return (
			<span>
				{t("toolRender.yield.fields", { count: Object.keys(args.data).length })}
			</span>
		);
	return null;
}

/** 输入：提交的数据（对象逐字段、其余按原生形态）或失败原因。 */
function Input({ args }: ToolRenderProps): ReactNode {
	const { t } = useTranslation();
	const error = str(args.error);
	const labels = sections(args);
	const data = args.data;
	return (
		<>
			{labels.length > 0 && (
				<div className="tv-yield-sections">
					<span className="tv-yield-key">{t("toolRender.yield.sections")}</span>
					{labels.map((label) => (
						<span key={label} className="tv-yield-section">
							{label}
						</span>
					))}
				</div>
			)}
			{error !== null && <ErrorBlock text={error} />}
			{isRecord(data) && Object.keys(data).length > 0 ? (
				<dl className="tv-yield-fields">
					{Object.entries(data).map(([k, v]) => (
						<div key={k} className="tv-yield-field">
							<dt className="tv-yield-key">{k}</dt>
							<dd>
								<Value value={v} />
							</dd>
						</div>
					))}
				</dl>
			) : (
				data !== undefined && <Value value={data} />
			)}
		</>
	);
}

/** 输出：只在不是一句「Result submitted.」时出现（中止原因、校验放宽、工作池剩余项）。 */
function Result({ args, result }: ToolRenderProps): ReactNode {
	if (!result) return null;
	const text = resultTextOf(result).trim();
	if (text === "" || text === "Result submitted.") return null;
	const d = detailsRecord(result);
	// 失败原因已在输入段（args.error）醒目给出时不再重复
	if (str(d?.status) === "aborted")
		return str(args.error) ? null : <ErrorBlock text={text} />;
	if (d?.schemaOverridden === true) return <Note tone="warn">{text}</Note>;
	return <ResultText result={result} maxLines={6} />;
}

export const yieldRenderer: ToolRenderer = { Summary, Meta, Input, Result };
