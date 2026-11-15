/** 文档开头 YAML frontmatter 的字段网格（Markdown 组件在正文前渲染；解析见 `lib/frontmatter.ts`）。 */
import type { ReactNode } from "react";
import type { FrontmatterField } from "../../lib/frontmatter";

/** 字段值：数组逐项等宽小标签，字符串原样（块标量保留换行），空值一条短横。 */
export function FrontmatterValueView({
	value,
	chipClass,
}: {
	value: FrontmatterField["value"];
	chipClass: string;
}): ReactNode {
	if (value.length === 0) return <span className="text-faint">—</span>;
	if (!Array.isArray(value))
		return <span className="whitespace-pre-wrap break-words">{value}</span>;
	return (
		<span className="flex flex-wrap gap-1">
			{value.map((v, i) => (
				// biome-ignore lint/suspicious/noArrayIndexKey: 列表项可能重复，无稳定 id
				<code key={i} className={chipClass}>
					{v}
				</code>
			))}
		</span>
	);
}

export function FrontmatterFields({
	fields,
}: {
	fields: readonly FrontmatterField[];
}): ReactNode {
	return (
		<dl className="md-frontmatter" data-frontmatter="">
			{fields.map((f, i) => (
				// biome-ignore lint/suspicious/noArrayIndexKey: key 可能重复（YAML 允许后者覆盖前者，这里都列出）
				<div key={i} className="contents">
					<dt>{f.key}</dt>
					<dd>
						<FrontmatterValueView value={f.value} chipClass="md-fm-chip" />
					</dd>
				</div>
			))}
		</dl>
	);
}
