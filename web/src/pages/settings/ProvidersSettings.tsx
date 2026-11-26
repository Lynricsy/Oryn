/**
 * 设置页「Provider」（default export，由设置页接入）：新建并一次性显示令牌；列表每行 = 名字、驱动、在线状态、
 * 能力（冻结 / 快照）、各 orb 类型规格；删除带确认。
 */
import { useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { Link } from "react-router";
import { api, unwrap } from "../../api/client";
import { qk, useProviders } from "../../api/queries";
import { useAsync } from "../../components/settings/useAsync";
import { CopyButton } from "../../components/ui/CopyButton";
import {
	Badge,
	Callout,
	Card,
	ListRow,
	StatusDot,
} from "../../components/ui/data";
import { Icon } from "../../components/ui/icons";
import { DockerBadge, typeHasDocker } from "../../components/ui/OrbBadges";
import {
	Button,
	ErrorText,
	IconButton,
	Spinner,
	TextInput,
} from "../../components/ui/primitives";
import { confirmAction } from "../../lib/confirm";
import { formatMemoryMB } from "../../lib/format";
import { LeadIcon, RowList } from "./parts";

/** provider 能力：支持为绿底勾、不支持为中性叉；读屏另读「支持 / 不支持」。 */
function Capability({ on, label }: { on: boolean; label: string }) {
	const { t } = useTranslation();
	const state = on ? t("providers.supported") : t("providers.unsupported");
	return (
		<Badge
			tone={on ? "ok" : "neutral"}
			icon={on ? "check" : "close"}
			title={state}
		>
			{label}
			<span className="sr-only">{`：${state}`}</span>
		</Badge>
	);
}

export default function ProvidersSettings() {
	const { t } = useTranslation();
	const qc = useQueryClient();
	const providers = useProviders();
	const [name, setName] = useState("");
	const [token, setToken] = useState<string | null>(null);
	const create = useAsync();
	const del = useAsync();
	return (
		<>
			<Card title={t("providers.create")} icon="plus">
				<form
					className="flex flex-wrap gap-2"
					onSubmit={async (e) => {
						e.preventDefault();
						await create.run(async () => {
							const res = await unwrap(
								api.POST("/api/v1/providers", { body: { name: name.trim() } }),
							);
							setToken(res.token);
							setName("");
							await qc.invalidateQueries({ queryKey: qk.providers });
						});
					}}
				>
					<TextInput
						className="max-w-60"
						placeholder={t("providers.namePlaceholder")}
						value={name}
						onChange={(e) => setName(e.target.value)}
					/>
					<Button
						type="submit"
						variant="primary"
						disabled={create.busy || !name.trim()}
					>
						{t("common.create")}
					</Button>
				</form>
				{token && (
					<div className="mt-3">
						<Callout tone="warn" title={t("providers.tokenOnce")}>
							<div className="mt-1.5 flex items-center gap-2">
								<code className="min-w-0 flex-1 break-all rounded-sm bg-inset px-2 py-1 font-mono text-fg text-xs">
									{token}
								</code>
								<CopyButton text={token} />
							</div>
						</Callout>
					</div>
				)}
				<ErrorText error={create.error} />
			</Card>
			<Card
				title={t("providers.title")}
				icon="box"
				action={
					<Link
						to="/providers"
						className="inline-flex h-7 items-center gap-1 rounded-md border border-border bg-panel px-2.5 font-medium text-fg text-xs shadow-xs transition-colors hover:bg-hover"
					>
						<Icon name="gauge" size={13} />
						{t("providers.openOverview")}
					</Link>
				}
			>
				{providers.isPending && <Spinner />}
				<ErrorText error={providers.error ?? del.error} />
				<RowList>
					{providers.data?.providers.map((p) => (
						<li key={p.id}>
							<ListRow
								leading={<LeadIcon name="box" />}
								title={
									<>
										<span className="truncate font-medium">{p.name}</span>
										{p.driver && <Badge icon="box">{p.driver}</Badge>}
									</>
								}
								meta={
									<>
										{/* 在线状态：圆点与文字同一处，不再单独占前置位 */}
										<span className="inline-flex items-center gap-1.5">
											<StatusDot tone={p.online ? "ok" : "neutral"} />
											{p.online
												? t("providers.online")
												: t("providers.offline")}
										</span>
										{p.capabilities && (
											<>
												<Capability
													on={p.capabilities.freeze}
													label={t("providers.freeze")}
												/>
												<Capability
													on={p.capabilities.snapshot}
													label={t("providers.snapshot")}
												/>
											</>
										)}
										{/* 每个 orb 类型一组：名称 + 规格，组间留出明显间隔；「默认」只是标注，用中性色 */}
										{p.orbTypes.length > 0 && (
											<span className="mt-1 flex basis-full flex-wrap gap-2">
												{p.orbTypes.map((ot) => (
													<span
														key={ot.name}
														className="inline-flex h-6 items-center gap-2 rounded-md bg-inset px-2 text-muted"
													>
														<span className="font-medium text-fg">
															{ot.name}
														</span>
														<span className="inline-flex items-center gap-1 tabular-nums">
															<Icon name="cpu" size={11} aria-hidden />
															{t("panel.cores", { n: ot.cpus })}
														</span>
														<span className="inline-flex items-center gap-1 tabular-nums">
															<Icon name="memory" size={11} aria-hidden />
															{formatMemoryMB(ot.memoryMB)}
														</span>
														{p.defaultType === ot.name && (
															<Badge>{t("providers.default")}</Badge>
														)}
														{typeHasDocker(ot) && <DockerBadge />}
													</span>
												))}
											</span>
										)}
									</>
								}
								trailing={
									<IconButton
										icon="trash"
										label={t("common.delete")}
										onClick={async () => {
											if (
												!(await confirmAction({
													title: t("providers.deleteConfirm", { name: p.name }),
													action: t("common.delete"),
													danger: true,
												}))
											)
												return;
											void del
												.run(() =>
													unwrap(
														api.DELETE("/api/v1/providers/{id}", {
															params: { path: { id: p.id } },
														}),
													),
												)
												.then(() =>
													qc.invalidateQueries({ queryKey: qk.providers }),
												);
										}}
									/>
								}
							/>
						</li>
					))}
				</RowList>
				{providers.data?.providers.length === 0 && (
					<p className="text-faint text-sm">{t("providers.empty")}</p>
				)}
			</Card>
		</>
	);
}
