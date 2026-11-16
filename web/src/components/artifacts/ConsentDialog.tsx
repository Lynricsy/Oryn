/**
 * 页面能力的同意对话框（M14 计划步骤 7.3）：artifact 页面第一次 use `files` 或 `sample` 时，控制面向正在看它的连接
 * 发 `artifact.consent {kind: "asked"}`，这里一次显示一个。「允许」「不允许」回答请求；`Esc`、关闭按钮与点背景都等于
 * 不允许。别处先回答了（收到同一请求的 `settled`），或这个客户端不再观看那个 artifact 时，对话框自动收走。
 * 挂在 AppShell 上，和具体查看器无关。
 */
import { useQueryClient } from "@tanstack/react-query";
import { type ReactNode, useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { answerArtifactConsent, artifactQk } from "../../api/artifact-queries";
import { ApiError } from "../../api/client";
import {
	type ArtifactConsentEvent,
	type RealtimeClient,
	realtime,
} from "../../realtime/socket";
import { Icon } from "../ui/icons";
import { Button, Dialog, ErrorText } from "../ui/primitives";

type ConsentClient = Pick<
	RealtimeClient,
	"onArtifactConsent" | "isWatchingArtifact" | "onArtifactWatchesChanged"
>;

export function ConsentDialog({
	client = realtime,
}: {
	client?: ConsentClient;
}): ReactNode {
	const { t } = useTranslation();
	const qc = useQueryClient();
	const [queue, setQueue] = useState<ArtifactConsentEvent[]>([]);
	const [busy, setBusy] = useState(false);
	// 回答失败的错误，只对出错的那个请求显示
	const [error, setError] = useState<{ id: string; error: unknown } | null>(
		null,
	);

	useEffect(() => {
		const offConsent = client.onArtifactConsent((m) => {
			if (m.kind === "settled") {
				setQueue((q) => q.filter((x) => x.requestId !== m.requestId));
				return;
			}
			if (!client.isWatchingArtifact(m.artifactId)) return;
			setQueue((q) =>
				q.some((x) => x.requestId === m.requestId) ? q : [...q, m],
			);
		});
		const offWatch = client.onArtifactWatchesChanged(() =>
			setQueue((q) => q.filter((x) => client.isWatchingArtifact(x.artifactId))),
		);
		return () => {
			offConsent();
			offWatch();
		};
	}, [client]);

	const current = queue[0];
	if (!current) return null;

	const answer = async (allow: boolean) => {
		if (busy) return;
		setBusy(true);
		setError(null);
		try {
			await answerArtifactConsent(current.requestId, allow);
			if (allow) void qc.invalidateQueries({ queryKey: artifactQk.consents });
		} catch (e) {
			// 404：请求已经结束（别处回答过或页面已关），直接收走；其它失败留着让用户再试
			if (!(e instanceof ApiError && e.status === 404)) {
				setError({ id: current.requestId, error: e });
				setBusy(false);
				return;
			}
		}
		setBusy(false);
		setQueue((q) => q.filter((x) => x.requestId !== current.requestId));
	};

	const title = current.title ?? "";
	const files = current.capability === "files";
	const heading = files
		? current.project
			? t("artifacts.consent.filesTitle", { title, project: current.project })
			: t("artifacts.consent.filesTitleNoProject", { title })
		: t("artifacts.consent.sampleTitle", { title });
	return (
		<Dialog
			open
			title={heading}
			onClose={() => void answer(false)}
			footer={
				<>
					<Button disabled={busy} onClick={() => void answer(false)}>
						{t("artifacts.consent.deny")}
					</Button>
					<Button
						variant="primary"
						disabled={busy}
						onClick={() => void answer(true)}
					>
						{t("artifacts.consent.allow")}
					</Button>
				</>
			}
		>
			<div className="flex items-start gap-3">
				<span className="grid h-8 w-8 shrink-0 place-items-center rounded-md border border-border bg-raised text-muted">
					<Icon name={files ? "folder" : "sparkle"} size={15} />
				</span>
				<p className="text-muted text-sm">
					{files
						? t("artifacts.consent.filesBody")
						: t("artifacts.consent.sampleBody")}
				</p>
			</div>
			<div className="mt-3">
				<ErrorText
					error={error?.id === current.requestId ? error.error : null}
				/>
			</div>
		</Dialog>
	);
}
