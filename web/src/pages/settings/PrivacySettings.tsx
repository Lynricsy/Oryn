/**
 * 设置 → 隐私与数据：
 * 1. AI 数据共享：当前会收到数据的第三方（按用途分组）、同意时间、撤回（撤回后同意框随即出现）。
 * 2. 推送通知：手机推送经 Oryn 推送网关中转的说明。
 * 3. 隐私政策链接。
 * 加载中、失败（可重试）两种占位。
 */
import { useQueryClient } from "@tanstack/react-query";
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import {
	revokeAiDataConsent,
	useAiDataConsent,
} from "../../api/ai-consent-queries";
import {
	PrivacyPolicyLink,
	RecipientGroups,
} from "../../components/consent/AiDataConsentGate";
import { useAsync } from "../../components/settings/useAsync";
import { Button, ErrorText, Spinner } from "../../components/ui/primitives";
import { Group, GroupList, GroupRow } from "../../components/ui/workbench";
import { confirmAction } from "../../lib/confirm";
import { formatDateTime } from "../../lib/format";

function AiSharingGroup(): ReactNode {
	const { t } = useTranslation();
	const qc = useQueryClient();
	const q = useAiDataConsent();
	const revoke = useAsync();
	const data = q.data;
	return (
		<Group title={t("privacy.aiTitle")}>
			{q.isPending && <Spinner />}
			{q.error && (
				<div className="flex flex-wrap items-center gap-2 px-1.5">
					<ErrorText error={q.error} />
					<Button size="sm" icon="refresh" onClick={() => void q.refetch()}>
						{t("privacy.retry")}
					</Button>
				</div>
			)}
			{data &&
				(data.recipients.length === 0 ? (
					<p className="px-1.5 text-faint text-sm">{t("privacy.none")}</p>
				) : (
					<>
						<p className="px-1.5 text-muted text-sm">{t("aiConsent.intro")}</p>
						<RecipientGroups recipients={data.recipients} />
					</>
				))}
			{data && (
				<GroupList>
					<GroupRow
						icon="check"
						title={t("privacy.consent")}
						meta={
							data.consentedAt != null
								? t("privacy.consentedAt", {
										when: formatDateTime(data.consentedAt),
									})
								: t("privacy.notConsented")
						}
						trailing={
							data.consentedAt != null && (
								<Button
									size="sm"
									variant="danger"
									disabled={revoke.busy}
									onClick={async () => {
										if (
											await confirmAction({
												title: t("privacy.revokeTitle"),
												body: t("privacy.revokeBody"),
												action: t("privacy.revoke"),
												danger: true,
											})
										)
											void revoke.run(() => revokeAiDataConsent(qc));
									}}
								>
									{t("privacy.revoke")}
								</Button>
							)
						}
					>
						{revoke.error ? <ErrorText error={revoke.error} /> : undefined}
					</GroupRow>
				</GroupList>
			)}
		</Group>
	);
}

export default function PrivacySettings(): ReactNode {
	const { t } = useTranslation();
	return (
		<>
			<AiSharingGroup />
			<Group title={t("privacy.pushTitle")}>
				<p className="px-1.5 text-muted text-sm">{t("privacy.pushBody")}</p>
			</Group>
			<Group title={t("privacy.policyTitle")}>
				<p className="px-1.5 text-sm">
					<PrivacyPolicyLink />
				</p>
			</Group>
		</>
	);
}
