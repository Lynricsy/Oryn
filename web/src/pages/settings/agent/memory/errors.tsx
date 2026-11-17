/**
 * 记忆页的失败提示：写明哪一步失败、为什么、下一步怎么做（web-guidelines「信息与文案」）。
 * 已知错误码各有原因与建议；其余按网络失败或 HTTP 状态给一句通用说明，不把原始错误串直接给人看。
 */
import type { TFunction } from "i18next";
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { ApiError } from "../../../../api/client";
import { Callout } from "../../../../components/ui/data";

/** 记忆页里会失败的操作；文案在 `memoryPage.steps.<step>`。 */
export type MemoryStep =
	| "loadSettings"
	| "saveSettings"
	| "loadStatus"
	| "generateEnv"
	| "loadProjects"
	| "saveProject"
	| "loadModels"
	| "saveModels"
	| "confirmDeployed"
	| "loadEndpoints"
	| "saveEndpoint"
	| "deleteEndpoint"
	| "loadUsage"
	| "loadDocuments"
	| "deleteDocument"
	| "search"
	| "forget"
	| "loadPendingDeletes"
	| "loadCandidates"
	| "approve"
	| "reject"
	| "createGlobal"
	| "updateGlobal"
	| "loadMentalModels"
	| "refreshMentalModel"
	| "createExport"
	| "loadExport"
	| "ownerLoadSettings"
	| "ownerSaveSettings"
	| "ownerSaveDecide"
	| "ownerDeleteDecide"
	| "ownerLoadStatus"
	| "ownerLoadTopics"
	| "ownerRefreshTopic"
	| "ownerCurate"
	| "ownerLoadBrief"
	| "ownerLoadFacts"
	| "ownerSetFactState"
	| "ownerLoadSources"
	| "ownerForgetSource"
	| "ownerLoadExclusions"
	| "ownerSaveExclusion"
	| "ownerDeleteExclusion"
	| "ownerLoadExplicit"
	| "ownerCreateExplicit"
	| "ownerUpdateExplicit"
	| "ownerDeleteExplicit"
	| "ownerApproveExplicit";

/** 有专门原因与建议的错误码；文案在 `memoryPage.errors.<code>.why / .next`。 */
const KNOWN_CODES = [
	"insecure_url",
	"unsupported_model",
	"embed_locked",
	"hindsight_running",
	"family_mismatch",
	"export_not_ready",
	"memory_disabled",
	"owner_memory_disabled",
	"memory_upstream",
	"not_pending",
	"not_implemented",
	"not_found",
	"invalid_request",
	"unauthorized",
	"forbidden",
	"rate_limited",
] as const;
type KnownCode = (typeof KNOWN_CODES)[number];

function knownCode(error: unknown): KnownCode | null {
	if (!(error instanceof ApiError)) return null;
	const code = error.status === 501 ? "not_implemented" : error.code;
	return (KNOWN_CODES as readonly string[]).includes(code)
		? (code as KnownCode)
		: null;
}

/** 原因与建议两句。 */
export function explainMemoryError(
	error: unknown,
	t: TFunction,
): { why: string; next: string } {
	const code = knownCode(error);
	if (code)
		return {
			why: t(`memoryPage.errors.${code}.why`),
			next: t(`memoryPage.errors.${code}.next`),
		};
	if (error instanceof ApiError)
		return {
			why: t("memoryPage.errors.http.why", { status: error.status }),
			next: t("memoryPage.errors.http.next"),
		};
	return {
		why: t("memoryPage.errors.network.why"),
		next: t("memoryPage.errors.network.next"),
	};
}

export function MemoryError({
	step,
	error,
}: {
	step: MemoryStep;
	error: unknown;
}): ReactNode {
	const { t } = useTranslation();
	if (!error) return null;
	const { why, next } = explainMemoryError(error, t);
	return (
		<Callout
			tone="err"
			role="alert"
			title={t("memoryPage.failed", { step: t(`memoryPage.steps.${step}`) })}
		>
			{why} {next}
		</Callout>
	);
}
