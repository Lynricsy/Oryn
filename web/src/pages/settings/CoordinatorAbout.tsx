/**
 * 设置 → 关于里的「协调宿主」一栏（M9）：全局助手与 Space 协调者跑在控制面宿主上的 omp 进程里。
 * 显示可用性、omp 版本与隔离摘要（网络、Landlock ABI、PID 命名空间、seccomp、运行用户 uid）；不可用时显示原因原文。
 * 设置里没有别的 orb 列表：协调 orb（provider `@host`）在这里列出，带「协调」标注（首页的执行位置不列它）。
 */
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { useCoordinatorStatus } from "../../api/coordinator-queries";
import { useOrbs } from "../../api/queries";
import { Badge, KeyValue } from "../../components/ui/data";
import { ErrorText, Spinner } from "../../components/ui/primitives";

export function CoordinatorAbout(): ReactNode {
	const { t } = useTranslation();
	const status = useCoordinatorStatus();
	const orbs = useOrbs();
	const coordOrbs = (orbs.data?.orbs ?? []).filter(
		(o) => o.coordinator && o.state !== "deleted",
	);
	const s = status.data;
	const sb = s?.sandbox;
	return (
		<section
			data-testid="coordinator-about"
			className="mt-4 flex flex-col gap-2 border-border border-t pt-3"
		>
			<h3 className="font-medium text-sm">{t("coordinator.title")}</h3>
			<p className="text-faint text-xs">{t("coordinator.hint")}</p>
			{status.isPending && <Spinner />}
			<ErrorText error={status.error} />
			{s && (
				<KeyValue
					rows={[
						{
							key: "available",
							label: t("coordinator.status"),
							value: s.available ? (
								<Badge tone="ok" icon="checkCircle">
									{t("coordinator.available")}
								</Badge>
							) : (
								<span className="flex flex-col items-start gap-1">
									<Badge tone="err" icon="alert">
										{t("coordinator.unavailable")}
									</Badge>
									{s.reason && (
										<span className="break-words text-muted text-xs">
											{s.reason}
										</span>
									)}
								</span>
							),
						},
						{
							key: "omp",
							label: t("coordinator.ompVersion"),
							value: <span className="font-mono">{s.ompVersion || "—"}</span>,
						},
						{
							key: "sandbox",
							label: t("coordinator.isolation"),
							value: sb ? (
								<span className="flex flex-wrap gap-1">
									<Badge>{t(`coordinator.network.${sb.network}`)}</Badge>
									<Badge>
										{t("coordinator.landlock", { abi: sb.landlockAbi })}
									</Badge>
									{sb.pidNamespace && <Badge>{t("coordinator.pidNs")}</Badge>}
									{sb.seccomp && <Badge>seccomp</Badge>}
									{sb.ipcScope && <Badge>{t("coordinator.ipcScope")}</Badge>}
									<Badge>{t("coordinator.uid", { uid: sb.uid })}</Badge>
								</span>
							) : (
								<span className="text-faint">
									{t("coordinator.notStarted")}
								</span>
							),
						},
						...(coordOrbs.length > 0
							? [
									{
										key: "orb",
										label: t("coordinator.orb"),
										value: (
											<span className="flex flex-col items-start gap-1">
												{coordOrbs.map((o) => (
													<span
														key={o.id}
														data-testid="coordinator-orb"
														className="flex flex-wrap items-center gap-1.5"
													>
														<span className="font-mono text-xs">{o.id}</span>
														<Badge icon="robot">
															{t("coordinator.orbTag")}
														</Badge>
														<span className="text-muted text-xs">
															{t(`orbState.${o.state}`)}
														</span>
													</span>
												))}
											</span>
										),
									},
								]
							: []),
					]}
				/>
			)}
		</section>
	);
}
