/**
 * 改动面板的「推送并开 PR」/「推送更新」（M14）：只在线程的项目已绑定 GitHub、且线程不是 runner 线程时出现。
 * 点击向线程发一条固定提示词（`buildShipPrompt`），commit / push / `gh pr create` 都由 agent 完成；
 * 线程已有打开的关联 PR 时改为「推送更新」。发送中禁用，成功后 3 秒内图标换成对勾。
 * 托管项目（M24 阶段 ③）改为「提交并推送到 <基线>」：基线取项目的 baseBranch（服务端自动交付认的也是它），
 * 提示词是 `buildHostedShipPrompt`；线程把提交推上基线后由服务端标为已交付。
 */
import { type ReactNode, useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { buildHostedShipPrompt, buildShipPrompt } from "../../lib/ship-prompt";
import { sendGeneratedPrompt } from "../../state/actions";
import { useIndex } from "../../state/stores";
import { useOrbOf, useThreadProject } from "../thread/ThreadHeader";
import { Button, ErrorText } from "../ui/primitives";

const DONE_MS = 3000;

export function ShipButton({
	threadId,
	base,
}: {
	threadId: string;
	base?: string;
}): ReactNode {
	const { t } = useTranslation();
	const project = useThreadProject(
		useIndex((s) => s.threads[threadId]?.projectId),
	);
	const update = useIndex(
		(s) => s.threads[threadId]?.pulls?.some((p) => p.state === "open") ?? false,
	);
	const orb = useOrbOf(threadId);
	const [sending, setSending] = useState(false);
	const [done, setDone] = useState(false);
	const [error, setError] = useState<unknown>(null);
	useEffect(() => {
		if (!done) return;
		const id = setTimeout(() => setDone(false), DONE_MS);
		return () => clearTimeout(id);
	}, [done]);
	const hosted = project?.hosted === true;
	if ((!hosted && !project?.github?.bound) || orb?.kind === "runner")
		return null;
	const target = project?.baseBranch || "main";
	const send = async () => {
		setSending(true);
		setError(null);
		try {
			await sendGeneratedPrompt(
				threadId,
				hosted
					? buildHostedShipPrompt(target)
					: buildShipPrompt({ base, update }),
			);
			setDone(true);
		} catch (e) {
			setError(e);
		} finally {
			setSending(false);
		}
	};
	const label = hosted
		? t("changes.ship.hosted", { base: target })
		: update
			? t("changes.ship.update")
			: t("changes.ship.open");
	return (
		<>
			<Button
				size="sm"
				variant="ghost"
				icon={done ? "check" : hosted ? "upload" : "pullRequest"}
				data-testid="ship-button"
				disabled={sending}
				onClick={() => void send()}
			>
				{label}
			</Button>
			<ErrorText error={error} />
		</>
	);
}
