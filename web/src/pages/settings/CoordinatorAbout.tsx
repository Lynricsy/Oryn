/**
 * 设置 → 关于里的「协调宿主」一组（M9）：全局助手与 Space 协调者跑在控制面宿主上的 omp 进程里。
 * 行：状态（不可用时行下方显示原因原文）、omp 版本、隔离摘要（网络、Landlock ABI、PID 命名空间、seccomp、运行用户 uid）。
 * 设置里没有别的 orb 列表：协调 orb（provider `@host`）在这里各占一行，带「协调」标注（首页的执行位置不列它）。
 */
import { Fragment, type ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { useCoordinatorStatus } from "../../api/coordinator-queries";
import { useOrbs } from "../../api/queries";
import { Badge } from "../../components/ui/data";
import { ErrorText, Spinner } from "../../components/ui/primitives";
import {
	Group,
	GroupList,
	GroupPanel,
	GroupRow,
	StatusText,
} from "../../components/ui/workbench";

/** 行尾值的样式（与关于页的信息行一致） */
const VALUE = "break-all text-[12.5px] text-muted";

export function CoordinatorAbout(): ReactNode {
	const { t } = useTranslation();
	const status = useCoordinatorStatus();
	const orbs = useOrbs();
	const coordOrbs = (orbs.data?.orbs ?? []).filter(
		(o) => o.coordinator && o.state !== "deleted",
	);
	const s = status.data;
	const sb = s?.sandbox;
	const isolation = sb
		? [
				t(`coordinator.network.${sb.network}`),
				t("coordinator.landlock", { abi: sb.landlockAbi }),
				sb.pidNamespace && t("coordinator.pidNs"),
				sb.seccomp && "seccomp",
				sb.ipcScope && t("coordinator.ipcScope"),
				t("coordinator.uid", { uid: sb.uid }),
			].filter((x): x is string => !!x)
		: [];
	return (
		<div data-testid="coordinator-about" className="contents">
			<Group title={t("coordinator.title")}>
				{status.isPending ? (
					<GroupPanel>
						<Spinner />
					</GroupPanel>
				) : (
					s && (
						<GroupList>
							<GroupRow
								title={t("coordinator.status")}
								meta={t("coordinator.hint")}
								trailing={
									<StatusText tone={s.available ? "ok" : "err"}>
										{t(
											s.available
												? "coordinator.available"
												: "coordinator.unavailable",
										)}
									</StatusText>
								}
							>
								{!s.available && s.reason && (
									<p className="break-words text-[12.5px] text-err">
										{s.reason}
									</p>
								)}
							</GroupRow>
							<GroupRow
								title={t("coordinator.ompVersion")}
								trailing={
									<span className={`${VALUE} font-mono`}>
										{s.ompVersion || "—"}
									</span>
								}
							/>
							<GroupRow
								title={t("coordinator.isolation")}
								meta={
									sb ? (
										isolation.map((x, i) => (
											<Fragment key={x}>
												{i > 0 && (
													<span aria-hidden className="text-faint">
														·
													</span>
												)}
												<span>{x}</span>
											</Fragment>
										))
									) : (
										<span className="text-faint">
											{t("coordinator.notStarted")}
										</span>
									)
								}
							/>
							{coordOrbs.map((o) => (
								<GroupRow
									key={o.id}
									title={t("coordinator.orb")}
									trailing={
										<span
											data-testid="coordinator-orb"
											className="flex flex-wrap items-center justify-end gap-1.5"
										>
											<span className={`${VALUE} font-mono`}>{o.id}</span>
											<Badge icon="robot">{t("coordinator.orbTag")}</Badge>
											<span className={VALUE}>{t(`orbState.${o.state}`)}</span>
										</span>
									}
								/>
							))}
						</GroupList>
					)
				)}
				<ErrorText error={status.error} />
			</Group>
		</div>
	);
}
