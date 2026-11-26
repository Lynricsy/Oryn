/**
 * 网关审计分组：orb 经网关的连接与命令、页面与命令行的测试 / 终端 / 文件操作、agent 的批量执行，按时间倒序分页加载。
 * 可按主机筛选；有线程时时间列链接到线程。
 */
import { type ReactNode, useState } from "react";
import { useTranslation } from "react-i18next";
import { Link } from "react-router";
import { useProjects } from "../../../../api/queries";
import { useSshAudit, useSshHosts } from "../../../../api/ssh-queries";
import {
	Button,
	ErrorText,
	Select,
	Spinner,
} from "../../../../components/ui/primitives";
import { Group, GroupPanel } from "../../../../components/ui/workbench";
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
		<Group title={t("sshPage.audit.title")}>
			<GroupPanel>
				<p className="text-[12.5px] text-muted">{t("sshPage.audit.intro")}</p>
				<Select
					className="w-full max-w-64"
					aria-label={t("sshPage.audit.hostFilter")}
					value={host}
					onChange={setHost}
					options={[
						{ value: "", label: t("sshPage.audit.allHosts") },
						...(hosts.data?.hosts ?? []).map((h) => ({
							value: h.id,
							label: h.projectId
								? `${h.name} · ${projectName.get(h.projectId) ?? h.projectId}`
								: h.name,
						})),
					]}
				/>
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
					<div>
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
			</GroupPanel>
		</Group>
	);
}
