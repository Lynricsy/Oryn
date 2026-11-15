/**
 * 当前工具报告（`/tools`）：按来源分组（内置 / MCP / 扩展 / 插件 / xd:// 挂载设备），可搜索；描述先显示第一段，
 * 更长的可展开全文。挂载设备靠 Core 的 `mounted` 标记区分（oryn-core 161c2f9 起）。
 */
import { type ReactNode, useState } from "react";
import { useTranslation } from "react-i18next";
import type {
	ResponseData,
	RpcActiveTool,
	RpcToolSource,
} from "../../../omp/types";
import { Badge } from "../../ui/data";
import { Icon, type IconName } from "../../ui/icons";
import { EmptyState, TextInput } from "../../ui/primitives";
import { ReportSection } from "./parts";

type ToolsData = ResponseData<"get_active_tools">;
/** oryn-core 161c2f9 给 xd:// 挂载项标 `mounted`；生成的 rpc-types.d.ts 替换前的本地扩展，替换后删除。 */
type Tool = RpcActiveTool & { mounted?: true };
type Group = RpcToolSource | "mounted";

const GROUPS: readonly Group[] = [
	"builtin",
	"mcp",
	"extension",
	"plugin",
	"mounted",
];

const GROUP_ICON: Record<Group, IconName> = {
	builtin: "wrench",
	mcp: "plugs",
	extension: "box",
	plugin: "box",
	mounted: "link",
};

/** `mcp__<server>__<tool>` → server；不是这种命名返回 null。 */
function mcpServer(name: string): string | null {
	const m = /^mcp__(.+?)__/.exec(name);
	return m?.[1] ?? null;
}

function ToolRow({ tool }: { tool: Tool }): ReactNode {
	const { t } = useTranslation();
	const [open, setOpen] = useState(false);
	const desc = (tool.description ?? "").trim();
	// 第一段（到第一个空行）作摘要，后面的收起
	const cut = desc.search(/\n\s*\n/);
	const head = cut > 0 ? desc.slice(0, cut) : desc;
	const more = cut > 0;
	const server = tool.source === "mcp" ? mcpServer(tool.name) : null;
	return (
		<li
			data-tool={tool.name}
			className="flex min-w-0 flex-col gap-1 border-border border-t py-2.5 first:border-t-0"
		>
			<div className="flex min-w-0 flex-wrap items-center gap-1.5">
				<span className="min-w-0 break-all font-mono text-sm">
					{tool.mounted ? `xd://${tool.name}` : tool.name}
				</span>
				{server && <Badge>{server}</Badge>}
			</div>
			{desc ? (
				<p className="whitespace-pre-wrap break-words text-muted text-xs leading-relaxed">
					{open ? desc : head}
				</p>
			) : (
				<p className="text-faint text-xs">{t("report.tools.noDescription")}</p>
			)}
			{more && (
				<button
					type="button"
					aria-expanded={open}
					onClick={() => setOpen((v) => !v)}
					className="inline-flex items-center gap-0.5 self-start rounded-xs text-faint text-xs hover:text-fg"
				>
					{open ? t("report.tools.less") : t("report.tools.more")}
					<Icon
						name={open ? "chevronUp" : "chevronDown"}
						size={11}
						aria-hidden
					/>
				</button>
			)}
		</li>
	);
}

export function ToolsReport({ data }: { data: ToolsData }): ReactNode {
	const { t } = useTranslation();
	const [q, setQ] = useState("");
	const tools = data.tools as Tool[];
	if (tools.length === 0)
		return (
			<EmptyState icon="wrench" title={t("report.tools.emptyTitle")}>
				{t("report.tools.emptyBody")}
			</EmptyState>
		);
	const needle = q.trim().toLowerCase();
	const shown = needle
		? tools.filter((tool) =>
				`${tool.name} ${tool.description ?? ""}`.toLowerCase().includes(needle),
			)
		: tools;
	const groups = GROUPS.map((g) => ({
		g,
		list: shown.filter(
			(tool) => (tool.mounted ? "mounted" : tool.source) === g,
		),
	})).filter((x) => x.list.length > 0);
	return (
		<div className="flex flex-col gap-4">
			<div className="flex items-center gap-3">
				<TextInput
					type="search"
					aria-label={t("report.tools.search")}
					placeholder={t("report.tools.search")}
					value={q}
					onChange={(e) => setQ(e.target.value)}
				/>
				<span className="shrink-0 text-faint text-xs tabular-nums">
					{needle
						? t("report.tools.matched", {
								count: shown.length,
								total: tools.length,
							})
						: t("report.tools.total", { count: tools.length })}
				</span>
			</div>
			{groups.length === 0 ? (
				<p className="py-6 text-center text-muted text-sm">
					{t("report.tools.noMatch", { q: q.trim() })}
				</p>
			) : (
				groups.map(({ g, list }) => (
					<ReportSection
						key={g}
						title={
							<span className="inline-flex items-center gap-2">
								<Badge icon={GROUP_ICON[g]}>
									{t(`report.tools.source.${g}`)}
								</Badge>
								<span className="tabular-nums">{list.length}</span>
							</span>
						}
					>
						<ul
							data-group={g}
							className="flex flex-col rounded-md border border-border bg-raised px-3"
						>
							{list.map((tool) => (
								<ToolRow key={`${g}:${tool.name}`} tool={tool} />
							))}
						</ul>
					</ReportSection>
				))
			)}
		</div>
	);
}
