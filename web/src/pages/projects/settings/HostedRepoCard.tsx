/**
 * 设置「概览」的「托管仓库」卡（M24 阶段 ③）。与「仓库镜像」卡同一位置、互斥：项目有外部仓库地址时不渲染。
 * - 托管项目：克隆地址（只读、可复制，下面提示本机用 `oryn clone`）与推送后 Webhook（地址、可选签名密钥、最近一次投递）；
 * - 没有仓库的项目：说明与「创建托管仓库」（确认后 POST `repo/hosted`）；已归档的项目按钮禁用并说明原因。
 */
import { useQueryClient } from "@tanstack/react-query";
import { type ReactNode, useId, useState } from "react";
import { useTranslation } from "react-i18next";
import { api, type Project, unwrap } from "../../../api/client";
import { qk } from "../../../api/queries";
import { repoKeys } from "../../../api/repo-queries";
import { useNow } from "../../../components/layout/pane-parts";
import { useAsync } from "../../../components/settings/useAsync";
import { Card } from "../../../components/ui/data";
import {
	Button,
	ErrorText,
	Field,
	TextInput,
} from "../../../components/ui/primitives";
import { confirmAction } from "../../../lib/confirm";
import { formatRelative } from "../../../lib/format";
import { shellArg } from "../../../lib/shell-quote";
import { CopyIconButton } from "../home/HeaderActions";

/** 最近一次投递：没有投递过 / 成功（状态码）/ 失败（原因）。 */
function DeliveryLine({
	hook,
}: {
	hook: NonNullable<Project["webhook"]>;
}): ReactNode {
	const { t } = useTranslation();
	const now = useNow();
	if (hook.lastDeliveryAt === undefined) return t("hostedRepo.webhook.never");
	const ago = formatRelative(hook.lastDeliveryAt, now);
	return hook.lastError ? (
		<span className="text-err">
			{t("hostedRepo.webhook.lastFailed", { ago, error: hook.lastError })}
		</span>
	) : (
		t("hostedRepo.webhook.lastOk", { ago, status: hook.lastStatus ?? 0 })
	);
}

/**
 * 推送后 Webhook：地址与可选签名密钥（已设置时留空 = 不改，「清除密钥」= 发空串）、保存 / 移除（要确认）与最近一次投递。
 * 父组件按服务端的地址与是否有密钥给 key：别处改了就还原表单。
 */
function WebhookSection({ project }: { project: Project }): ReactNode {
	const { t } = useTranslation();
	const qc = useQueryClient();
	const headingId = useId();
	const hook = project.webhook;
	const [url, setUrl] = useState(hook?.url ?? "");
	const [secret, setSecret] = useState("");
	const [clearSecret, setClearSecret] = useState(false);
	const save = useAsync();
	const remove = useAsync();
	const refresh = async (next?: Project) => {
		if (next) qc.setQueryData(qk.project(project.id), next);
		await qc.invalidateQueries({ queryKey: qk.projects });
	};
	const dirty =
		url.trim() !== (hook?.url ?? "") || secret !== "" || clearSecret;
	const secretBody = secret ? { secret } : clearSecret ? { secret: "" } : {};
	return (
		<section
			data-testid="hosted-webhook"
			aria-labelledby={headingId}
			className="flex flex-col gap-3 border-border border-t pt-3"
		>
			<div className="flex flex-col gap-0.5">
				<h4 id={headingId} className="font-medium text-sm">
					{t("hostedRepo.webhook.title")}
				</h4>
				<p className="text-faint text-xs">{t("hostedRepo.webhook.hint")}</p>
			</div>
			<div className="grid gap-3 sm:grid-cols-2">
				<Field label={t("hostedRepo.webhook.url")}>
					<TextInput
						type="url"
						maxLength={2048}
						placeholder="https://"
						className="font-mono"
						value={url}
						onChange={(e) => setUrl(e.target.value)}
					/>
				</Field>
				<Field
					label={t("hostedRepo.webhook.secret")}
					hint={t("hostedRepo.webhook.secretHint")}
				>
					<TextInput
						type="password"
						autoComplete="new-password"
						maxLength={256}
						placeholder={
							hook?.hasSecret && !clearSecret
								? t("hostedRepo.webhook.secretKept")
								: undefined
						}
						value={secret}
						onChange={(e) => setSecret(e.target.value)}
					/>
				</Field>
			</div>
			<div className="flex flex-wrap items-center gap-2">
				<Button
					size="sm"
					variant="primary"
					disabled={!url.trim() || !dirty || save.busy}
					onClick={() =>
						void save.run(async () => {
							const next = await unwrap(
								api.PUT("/api/v1/projects/{id}/webhook", {
									params: { path: { id: project.id } },
									body: { url: url.trim(), ...secretBody },
								}),
							);
							setSecret("");
							setClearSecret(false);
							await refresh(next);
						})
					}
				>
					{t("hostedRepo.webhook.save")}
				</Button>
				{hook?.hasSecret && (
					<Button
						size="sm"
						variant={clearSecret ? "secondary" : "ghost"}
						aria-pressed={clearSecret}
						onClick={() => {
							setClearSecret((v) => !v);
							setSecret("");
						}}
					>
						{t("hostedRepo.webhook.clearSecret")}
					</Button>
				)}
				{hook && (
					<Button
						size="sm"
						variant="ghost"
						icon="trash"
						disabled={remove.busy}
						onClick={async () => {
							if (
								!(await confirmAction({
									title: t("hostedRepo.webhook.removeConfirm", {
										name: project.name,
									}),
									body: t("hostedRepo.webhook.removeConfirmBody"),
									action: t("hostedRepo.webhook.remove"),
									danger: true,
								}))
							)
								return;
							await remove.run(async () => {
								await unwrap(
									api.DELETE("/api/v1/projects/{id}/webhook", {
										params: { path: { id: project.id } },
									}),
								);
								setUrl("");
								await refresh();
							});
						}}
					>
						{t("hostedRepo.webhook.remove")}
					</Button>
				)}
			</div>
			<ErrorText error={save.error ?? remove.error} />
			{hook && (
				<p data-testid="hosted-webhook-delivery" className="text-faint text-xs">
					<DeliveryLine hook={hook} />
				</p>
			)}
		</section>
	);
}

/** 克隆地址与本机用法提示。 */
function CloneUrlField({
	project,
	cloneUrl,
}: {
	project: Project;
	cloneUrl: string;
}): ReactNode {
	const { t } = useTranslation();
	return (
		<Field
			label={t("hostedRepo.cloneUrl")}
			hint={t("hostedRepo.cloneHint", {
				cmd: `oryn clone ${shellArg(project.name)}`,
			})}
		>
			<div className="flex min-w-0 items-center gap-2">
				<TextInput
					readOnly
					// 同一个 label 里还有复制按钮：名字只取字段名
					aria-label={t("hostedRepo.cloneUrl")}
					className="min-w-0 flex-1 font-mono"
					value={cloneUrl}
					onFocus={(e) => e.currentTarget.select()}
				/>
				<CopyIconButton
					icon="copy"
					label={t("hostedRepo.copyCloneUrl")}
					text={cloneUrl}
				/>
			</div>
		</Field>
	);
}

/** 没有仓库的项目：创建托管仓库（确认框写明基线分支；已归档时禁用）。 */
function CreateHosted({ project }: { project: Project }): ReactNode {
	const { t } = useTranslation();
	const qc = useQueryClient();
	const create = useAsync();
	const archived = !!project.archivedAt;
	return (
		<div className="flex flex-col gap-2">
			<p className="text-muted text-sm">{t("hostedRepo.createBody")}</p>
			<div className="flex flex-wrap items-center gap-x-3 gap-y-1">
				<Button
					size="sm"
					icon="hostedRepo"
					disabled={archived || create.busy}
					onClick={async () => {
						if (
							!(await confirmAction({
								title: t("hostedRepo.createConfirm", { name: project.name }),
								body: t("hostedRepo.createConfirmBody", {
									base: project.baseBranch || "main",
								}),
								action: t("hostedRepo.createAction"),
							}))
						)
							return;
						await create.run(async () => {
							const next = await unwrap(
								api.POST("/api/v1/projects/{id}/repo/hosted", {
									params: { path: { id: project.id } },
								}),
							);
							qc.setQueryData(qk.project(project.id), next);
							await Promise.all([
								qc.invalidateQueries({ queryKey: qk.projects }),
								qc.invalidateQueries({ queryKey: repoKeys.all(project.id) }),
							]);
						});
					}}
				>
					{t("hostedRepo.create")}
				</Button>
				{archived && (
					<span className="text-faint text-xs">
						{t("hostedRepo.createArchived")}
					</span>
				)}
			</div>
			<ErrorText error={create.error} />
		</div>
	);
}

export function HostedRepoCard({ project }: { project: Project }): ReactNode {
	const { t } = useTranslation();
	if (project.repoUrl) return null;
	return (
		<div data-testid="hosted-repo-card">
			<Card title={t("hostedRepo.title")} icon="hostedRepo">
				{project.hosted ? (
					<div className="flex flex-col gap-4">
						{project.cloneUrl && (
							<CloneUrlField project={project} cloneUrl={project.cloneUrl} />
						)}
						<WebhookSection
							key={`${project.webhook?.url ?? ""}:${project.webhook?.hasSecret ?? false}`}
							project={project}
						/>
					</div>
				) : (
					<CreateHosted project={project} />
				)}
			</Card>
		</div>
	);
}
