/** 结构化数据卡片：把报告类 RPC 的结果渲染成键值表 / 列表（未知结构的通用兜底）。 */
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { formatCount } from "../../lib/format";
import { isRecord } from "../../tool-render/util";
import { Badge, KeyValue } from "./data";

function Scalar({ value }: { value: unknown }) {
	if (value === null || value === undefined)
		return <span className="text-faint">—</span>;
	if (typeof value === "boolean")
		return <Badge tone={value ? "ok" : "neutral"}>{String(value)}</Badge>;
	if (typeof value === "number")
		return <span className="tabular-nums">{formatCount(value)}</span>;
	return <span className="break-all">{String(value)}</span>;
}

// 记录列表里每项一张小卡
const ITEM_CARD = "rounded-lg border border-border bg-panel p-2.5";

export function StructuredView({
	value,
	depth = 0,
}: {
	value: unknown;
	depth?: number;
}): ReactNode {
	const { t } = useTranslation();
	if (Array.isArray(value)) {
		if (value.length === 0)
			return <span className="text-faint">{t("common.none")}</span>;
		// 纯标量数组：每项一个徽标
		if (value.every((v) => !isRecord(v) && !Array.isArray(v))) {
			return (
				<span className="flex flex-wrap gap-1">
					{value.map((v, i) => (
						// biome-ignore lint/suspicious/noArrayIndexKey: 标量列表可能重复，无稳定 id
						<Badge key={i} title={String(v)}>
							{String(v)}
						</Badge>
					))}
				</span>
			);
		}
		return (
			<div className="flex flex-col gap-2">
				{value.map((v, i) => (
					// biome-ignore lint/suspicious/noArrayIndexKey: 报告列表无稳定 id
					<div key={i} className={ITEM_CARD}>
						<StructuredView value={v} depth={depth + 1} />
					</div>
				))}
			</div>
		);
	}
	if (isRecord(value)) {
		// 嵌套过深：退回 JSON 代码块
		if (depth > 3)
			return (
				<pre className="overflow-auto rounded-lg bg-inset px-2.5 py-2 font-mono text-2xs leading-relaxed">
					{JSON.stringify(value, null, 2)}
				</pre>
			);
		const rows = Object.entries(value)
			.filter(([, v]) => v !== undefined)
			.map(([k, v]) => ({
				key: k,
				label: <span className="font-mono text-xs">{k}</span>,
				value:
					isRecord(v) || Array.isArray(v) ? (
						<StructuredView value={v} depth={depth + 1} />
					) : (
						<Scalar value={v} />
					),
			}));
		return <KeyValue rows={rows} />;
	}
	return <Scalar value={value} />;
}
