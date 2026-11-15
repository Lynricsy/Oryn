/** `/mcp list`、`/ssh list` 命令输出的表格（解析见 `lib/config-list.ts`）。 */
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import type { ConfigList } from "../../lib/config-list";
import { Badge } from "../ui/data";

const HEAD = {
	mcp: ["name", "type", "status", "location"],
	ssh: ["name", "host", "user", "port"],
} as const;

export function ConfigListTable({ list }: { list: ConfigList }): ReactNode {
	const { t } = useTranslation();
	return (
		<div className="overflow-x-auto" data-config-list={list.kind}>
			<table className="w-full border-collapse text-left text-xs">
				<thead>
					<tr className="text-faint">
						{HEAD[list.kind].map((h) => (
							<th key={h} className="whitespace-nowrap px-3 py-1.5 font-medium">
								{t(`notice.configList.${h}`)}
							</th>
						))}
						<th className="px-3 py-1.5 font-medium">
							{t("notice.configList.scope")}
						</th>
					</tr>
				</thead>
				<tbody>
					{list.rows.map(({ cells: [name, b, c, d], scope }, i) => (
						// biome-ignore lint/suspicious/noArrayIndexKey: 输出行无稳定 id，名称在 Core 侧已去重但不保证
						<tr key={i} className="border-border border-t align-baseline">
							<td className="px-3 py-1.5 font-medium [overflow-wrap:anywhere]">
								{name}
							</td>
							<td className="whitespace-nowrap px-3 py-1.5 font-mono text-muted">
								{b}
							</td>
							<td className="px-3 py-1.5">
								{list.kind === "mcp" ? (
									<Badge tone={c === "enabled" ? "ok" : "neutral"}>
										{t(
											c === "enabled"
												? "notice.configList.enabled"
												: "notice.configList.disabled",
										)}
									</Badge>
								) : (
									<span className="font-mono text-muted">{c}</span>
								)}
							</td>
							<td
								className={`px-3 py-1.5 font-mono text-muted ${list.kind === "ssh" ? "tabular-nums" : "[overflow-wrap:anywhere]"}`}
							>
								{d}
							</td>
							<td className="px-3 py-1.5">
								<Badge>
									{t(
										scope === "project"
											? "notice.configList.scopeProject"
											: "notice.configList.scopeUser",
									)}
								</Badge>
							</td>
						</tr>
					))}
				</tbody>
			</table>
		</div>
	);
}
