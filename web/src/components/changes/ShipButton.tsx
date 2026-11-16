/**
 * 改动面板的「推送并开 PR」/「推送更新」（M14）：只在线程的项目已绑定 GitHub、且线程不是 runner 线程时出现。
 * 点击向线程发一条固定提示词（`buildShipPrompt`），commit / push / `gh pr create` 都由 agent 完成；
 * 线程已有打开的关联 PR 时改为「推送更新」。发送中禁用，成功后 3 秒内图标换成对勾。
 */
import { type ReactNode, useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { buildShipPrompt } from "../../lib/ship-prompt";
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
	if (!project?.github?.bound || orb?.kind === "runner") return null;
	const send = async () => {
		setSending(true);
		setError(null);
		try {
			await sendGeneratedPrompt(threadId, buildShipPrompt({ base, update }));
			setDone(true);
		} catch (e) {
			setError(e);
		} finally {
			setSending(false);
		}
	};
	return (
		<>
			<Button
				size="sm"
				variant="ghost"
				icon={done ? "check" : "pullRequest"}
				data-testid="ship-button"
				disabled={sending}
				onClick={() => void send()}
			>
				{update ? t("changes.ship.update") : t("changes.ship.open")}
			</Button>
			<ErrorText error={error} />
		</>
	);
}
