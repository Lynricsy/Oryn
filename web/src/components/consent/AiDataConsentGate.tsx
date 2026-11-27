/**
 * AI 数据共享同意框（App Store 审核指南 5.1.2(i)）：服务端报告 `required`（有接收方还没被同意）时整页拦住，不能用 Esc
 * 或点遮罩关闭。按用途列出每个接收方（名称 + 主机），说明发送什么、为什么，数据从主人的 Oryn 服务器直接发给它们、
 * Oryn 的开发者收不到，并链接隐私政策。「同意」以当前全部接收方 id 记录同意；「不同意」说明 AI 功能用不了，
 * 给出更换服务器（两种壳）或退出登录（浏览器），下次加载再问。新接收方出现时 `required` 重新变真，随即再问。
 * 挂在 AppShell 上（两种布局各一处）。
 */
import { useQueryClient } from "@tanstack/react-query";
import { type ReactNode, useState } from "react";
import { useTranslation } from "react-i18next";
import { useNavigate } from "react-router";
import {
	type AiDataPurpose,
	type AiDataRecipient,
	agreeAiDataConsent,
	useAiDataConsent,
	useAiDataConsentInvalidation,
} from "../../api/ai-consent-queries";
import { api, unwrap } from "../../api/client";
import { changeServerFn } from "../../lib/change-server";
import { privacyPolicyUrl } from "../../lib/privacy";
import { useAsync } from "../settings/useAsync";
import { Button, Dialog, ErrorText } from "../ui/primitives";

const PURPOSES: readonly AiDataPurpose[] = ["chat", "memory", "web"];

/** 按用途分组的接收方列表（同意框与设置页共用）。 */
export function RecipientGroups({
	recipients,
}: {
	recipients: readonly AiDataRecipient[];
}): ReactNode {
	const { t } = useTranslation();
	return (
		<div className="flex flex-col gap-3">
			{PURPOSES.map((p) => {
				const list = recipients.filter((r) => r.purposes.includes(p));
				if (list.length === 0) return null;
				return (
					<section
						key={p}
						data-purpose={p}
						className="rounded-md border border-border bg-raised px-3 py-2.5"
					>
						<h3 className="font-medium text-fg text-sm">
							{t(`aiConsent.purpose.${p}.title`)}
						</h3>
						<p className="mt-0.5 text-muted text-xs">
							{t(`aiConsent.purpose.${p}.data`)}
						</p>
						<ul className="mt-2 flex flex-col gap-1">
							{list.map((r) => (
								<li
									key={r.id}
									className="flex flex-wrap items-baseline gap-x-2 text-sm"
								>
									{/* 联网的接收方是公网网站：名字随界面语言；其余用服务端给的名字 */}
									<span className="text-fg">
										{r.id === "web:public"
											? t("aiConsent.webRecipient")
											: r.name}
									</span>
									{r.host && (
										<span className="break-all font-mono text-faint text-xs">
											{r.host}
										</span>
									)}
								</li>
							))}
						</ul>
					</section>
				);
			})}
		</div>
	);
}

/** 隐私政策链接（按当前界面语言）。 */
export function PrivacyPolicyLink(): ReactNode {
	const { t, i18n } = useTranslation();
	return (
		<a
			href={privacyPolicyUrl(i18n.language)}
			target="_blank"
			rel="noopener noreferrer"
			className="text-accent underline-offset-2 hover:underline"
		>
			{t("aiConsent.policy")}
		</a>
	);
}

export function AiDataConsentGate(): ReactNode {
	const { t } = useTranslation();
	const qc = useQueryClient();
	const navigate = useNavigate();
	useAiDataConsentInvalidation();
	const consent = useAiDataConsent();
	const [declined, setDeclined] = useState(false);
	const agree = useAsync();
	const logout = useAsync();
	const data = consent.data;
	if (data?.required !== true || data.recipients.length === 0) return null;
	const changeServer = changeServerFn();

	if (declined)
		return (
			<Dialog
				open
				wrapTitle
				title={t("aiConsent.declinedTitle")}
				footer={
					<>
						<Button onClick={() => setDeclined(false)}>
							{t("aiConsent.back")}
						</Button>
						{changeServer ? (
							<Button variant="danger" onClick={() => changeServer()}>
								{t("account.changeServer")}
							</Button>
						) : (
							<Button
								variant="danger"
								disabled={logout.busy}
								onClick={() =>
									void logout
										.run(() => unwrap(api.POST("/api/v1/auth/logout")))
										.then((ok) => {
											if (ok) navigate("/login");
										})
								}
							>
								{t("account.logout")}
							</Button>
						)}
					</>
				}
			>
				<p className="text-muted text-sm">{t("aiConsent.declinedBody")}</p>
				<ErrorText error={logout.error} />
			</Dialog>
		);

	return (
		<Dialog
			open
			wide
			wrapTitle
			title={t("aiConsent.title")}
			bodyTestId="ai-consent-body"
			footer={
				<>
					<Button disabled={agree.busy} onClick={() => setDeclined(true)}>
						{t("aiConsent.decline")}
					</Button>
					<Button
						variant="primary"
						disabled={agree.busy}
						onClick={() =>
							void agree.run(() =>
								agreeAiDataConsent(
									qc,
									data.recipients.map((r) => r.id),
								),
							)
						}
					>
						{t("aiConsent.agree")}
					</Button>
				</>
			}
		>
			<div className="flex flex-col gap-3 text-sm">
				<p className="text-muted">{t("aiConsent.intro")}</p>
				<RecipientGroups recipients={data.recipients} />
				<p className="text-muted">{t("aiConsent.terms")}</p>
				<p>
					<PrivacyPolicyLink />
				</p>
				<ErrorText error={agree.error} />
			</div>
		</Dialog>
	);
}
