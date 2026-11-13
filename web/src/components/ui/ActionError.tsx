/**
 * 步骤失败提示：「<哪一步>失败：<为什么>」+ 一句下一步建议，原始错误收在「详细信息」里备查。
 * 诊断规则见 `lib/action-error.ts`。
 */
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import {
	type ActionStep,
	diagnoseDeliveryError,
	diagnoseRequestError,
} from "../../lib/action-error";
import { Icon } from "./icons";

function Failure({
	step,
	reason,
	hint,
	detail,
	compact,
}: {
	step: ActionStep;
	reason: string;
	hint: string;
	detail: string;
	compact?: boolean;
}): ReactNode {
	const { t } = useTranslation();
	return (
		<div
			role="alert"
			className={`flex min-w-0 items-start gap-1.5 ${compact ? "text-xs" : "text-sm"}`}
		>
			<Icon
				name="alert"
				size={compact ? 12 : 13}
				className={`shrink-0 text-err ${compact ? "mt-[2px]" : "mt-[3px]"}`}
			/>
			<div className="flex min-w-0 flex-col gap-0.5">
				<p className="text-err">
					{t("actionError.failed", {
						step: t(`actionError.step.${step}`),
						reason,
					})}
				</p>
				<p className="text-muted text-xs">{hint}</p>
				{detail && detail !== reason && (
					<details className="text-faint text-xs">
						<summary className="cursor-pointer select-none hover:text-muted">
							{t("actionError.detail")}
						</summary>
						<code className="mt-0.5 block break-all font-mono">{detail}</code>
					</details>
				)}
			</div>
		</div>
	);
}

/** 控制面请求（或浏览器推送 API）失败；没有错误时不渲染。 */
export function ActionError({
	step,
	error,
	compact,
}: {
	step: ActionStep;
	error: unknown;
	/** 行内小号（列表行里的就地结果） */
	compact?: boolean;
}): ReactNode {
	const { t } = useTranslation();
	if (!error) return null;
	const d = diagnoseRequestError(error);
	return (
		<Failure
			step={step}
			reason={t(`actionError.reason.${d.reason}`)}
			hint={t(`actionError.hint.${d.reason}`)}
			detail={d.detail}
			compact={compact}
		/>
	);
}

/** 通知渠道的测试消息没有送达（服务端回传的发送错误）。 */
export function DeliveryFailure({ message }: { message: string }): ReactNode {
	const { t } = useTranslation();
	const d = diagnoseDeliveryError(message);
	return (
		<Failure
			step="channelTest"
			reason={t(`deliveryError.reason.${d.reason}`)}
			hint={t(`deliveryError.hint.${d.reason}`)}
			detail={d.detail}
			compact
		/>
	);
}
