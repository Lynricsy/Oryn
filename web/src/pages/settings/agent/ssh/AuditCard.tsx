/**
 * 网关审计卡片：orb 经网关的连接与命令、页面与命令行的测试 / 终端 / 文件操作、agent 的批量执行，按时间倒序分页加载。
 * 可按主机筛选；有线程时时间列链接到线程。
 */
import { type ReactNode, useState } from "react";
import { useTranslation } from "react-i18next";
import { Link } from "react-router";
import { useProjects } from "../../../../api/queries";
import { useSshAudit, useSshHosts } from "../../../../api/ssh-queries";
import { Card } from "../../../../components/ui/data";
import {
	Button,
	ErrorText,
	Select,
	Spinner,
} from "../../../../components/ui/primitives";
import { formatDateTime, formatDuration } from "../../../../lib/format";

export function AuditCard(): ReactNode {
	const { t } = useTranslation();
	const hosts = useSshHosts();
	const projects = useProjects();
	const [host, setHost] = useState("");
	const audit = useSshAudit(host ? { host } : {});
	const entries = audit.data?.pages.flatMap((p) => p.entries) ?? [];
	const projectName = new Map(
		(projects.data?.projects ?? []).map((p) => [p.id, p.name]),
	);
	const th = "px-1 py-1 font-medium";
	const td = "px-1 py-1.5 align-top";
	return (
		<Card title={t("sshPage.audit.title")} icon="list">
			<p className="mb-2 text-faint text-xs">{t("sshPage.audit.intro")}</p>
			<div className="mb-3 max-w-64">
				<Select
					aria-label={t("sshPage.audit.hostFilter")}
					value={host}
					onChange={(e) => setHost(e.target.value)}
				>
					<option value="">{t("sshPage.audit.allHosts")}</option>
					{hosts.data?.hosts.map((h) => (
						<option key={h.id} value={h.id}>
							{h.projectId
								? `${h.name} · ${projectName.get(h.projectId) ?? h.projectId}`
								: h.name}
						</option>
					))}
				</Select>
			</div>
			{audit.isPending && <Spinner size={12} />}
			<ErrorText error={audit.error} />
			{audit.isSuccess && entries.length === 0 && (
				<p className="text-faint text-xs">{t("sshPage.audit.empty")}</p>
			)}
			{entries.length > 0 && (
				<div className="-mx-1 overflow-x-auto">
					<table data-testid="ssh-audit" className="w-full text-xs">
						<thead>
							<tr className="text-left text-faint">
								<th className={th}>{t("sshPage.audit.col.time")}</th>
								<th className={th}>{t("sshPage.audit.col.source")}</th>
								<th className={th}>{t("sshPage.audit.col.host")}</th>
								<th className={th}>{t("sshPage.audit.col.kind")}</th>
								<th className={th}>{t("sshPage.audit.col.detail")}</th>
								<th className={`${th} text-right`}>
									{t("sshPage.audit.col.exit")}
								</th>
								<th className={`${th} text-right`}>
									{t("sshPage.audit.col.duration")}
								</th>
							</tr>
						</thead>
						<tbody>
							{entries.map((e) => (
								<tr
									key={e.id}
									data-testid="ssh-audit-row"
									className="border-border border-t text-muted"
								>
									<td className={`${td} whitespace-nowrap tabular-nums`}>
										{e.threadId ? (
											<Link
												to={`/t/${e.threadId}`}
												title={e.threadId}
												className="text-accent hover:underline"
											>
												{formatDateTime(e.at)}
											</Link>
										) : (
											formatDateTime(e.at)
										)}
									</td>
									<td className={`${td} whitespace-nowrap`}>
										{t(`sshPage.audit.source.${e.source}`)}
									</td>
									<td className={`${td} whitespace-nowrap font-mono text-fg`}>
										{e.hostName}
									</td>
									<td
										className={`${td} whitespace-nowrap ${e.ok ? "" : "text-err"}`}
									>
										{e.kind}
									</td>
									<td className={`${td} max-w-72`}>
										<span
											title={e.detail}
											className="block truncate font-mono text-fg"
										>
											{e.detail}
										</span>
									</td>
									<td
										className={`${td} text-right font-mono tabular-nums ${e.exitCode ? "text-err" : ""}`}
									>
										{e.exitCode ?? ""}
									</td>
									<td
										className={`${td} whitespace-nowrap text-right tabular-nums`}
									>
										{e.durationMs !== undefined
											? formatDuration(e.durationMs)
											: ""}
									</td>
								</tr>
							))}
						</tbody>
					</table>
				</div>
			)}
			{audit.hasNextPage && (
				<div className="mt-2">
					<Button
						size="sm"
						variant="ghost"
						disabled={audit.isFetchingNextPage}
						onClick={() => void audit.fetchNextPage()}
					>
						{audit.isFetchingNextPage && <Spinner size={12} />}
						{t("sshPage.audit.more")}
					</Button>
				</div>
			)}
		</Card>
	);
}
