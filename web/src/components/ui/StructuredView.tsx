/** 结构化数据卡片：把报告类 RPC 的结果渲染成键值表 / 列表（未知结构的通用兜底）。 */
import type { ReactNode } from "react";
import { isRecord } from "../../tool-render/util";

function Scalar({ value }: { value: unknown }) {
	if (value === null || value === undefined)
		return <span className="text-faint">—</span>;
	if (typeof value === "boolean")
		return (
			<span className={value ? "text-ok" : "text-faint"}>{String(value)}</span>
		);
	if (typeof value === "number")
		return <span className="font-mono">{value.toLocaleString()}</span>;
	return <span className="break-all">{String(value)}</span>;
}

export function StructuredView({
	value,
	depth = 0,
}: {
	value: unknown;
	depth?: number;
}): ReactNode {
	if (Array.isArray(value)) {
		if (value.length === 0) return <span className="text-faint">[]</span>;
		if (value.every((v) => !isRecord(v) && !Array.isArray(v))) {
			return (
				<span className="font-mono text-xs">
					{value.map((v) => String(v)).join(", ")}
				</span>
			);
		}
		return (
			<div className="flex flex-col gap-2">
				{value.map((v, i) => (
					// biome-ignore lint/suspicious/noArrayIndexKey: 报告列表无稳定 id
					<div key={i} className="rounded-md border border-border p-2">
						<StructuredView value={v} depth={depth + 1} />
					</div>
				))}
			</div>
		);
	}
	if (isRecord(value)) {
		const entries = Object.entries(value).filter(([, v]) => v !== undefined);
		if (depth > 3)
			return (
				<pre className="overflow-auto font-mono text-2xs">
					{JSON.stringify(value, null, 2)}
				</pre>
			);
		return (
			<div className="grid grid-cols-[minmax(6rem,max-content)_1fr] gap-x-3 gap-y-1 text-xs">
				{entries.map(([k, v]) => (
					<div key={k} className="contents">
						<span className="text-muted">{k}</span>
						<div className="min-w-0">
							{isRecord(v) || Array.isArray(v) ? (
								<StructuredView value={v} depth={depth + 1} />
							) : (
								<Scalar value={v} />
							)}
						</div>
					</div>
				))}
			</div>
		);
	}
	return <Scalar value={value} />;
}
