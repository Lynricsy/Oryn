/**
 * 「注入预览」（M17 R19）：助手对话启动时追加到系统提示词的记忆段。可填一个对话 id 看它将得到的内容
 * （有未过期的快照时用快照，否则现生成且不写入）；不填 = 现生成。助手记忆关闭时服务端返回空串，不附加记忆段。
 */
import { type ReactNode, useState } from "react";
import { useTranslation } from "react-i18next";
import { useOwnerMemoryBrief } from "../../../../api/owner-memory-queries";
import { Badge, Callout } from "../../../../components/ui/data";
import {
	Button,
	Dialog,
	Field,
	Spinner,
	TextInput,
} from "../../../../components/ui/primitives";
import { formatDateTime } from "../../../../lib/format";
import { MemoryError } from "./errors";

export function OwnerBriefDialog({
	open,
	onClose,
}: {
	open: boolean;
	onClose: () => void;
}): ReactNode {
	const { t } = useTranslation();
	const [draft, setDraft] = useState("");
	const [threadId, setThreadId] = useState("");
	const q = useOwnerMemoryBrief(threadId || undefined, open);
	const close = () => {
		setDraft("");
		setThreadId("");
		onClose();
	};
	const b = q.data;
	return (
		<Dialog
			open={open}
			onClose={close}
			wide
			title={t("memoryPage.owner.brief.title")}
			footer={
				<Button variant="primary" onClick={close}>
					{t("common.close")}
				</Button>
			}
		>
			<div className="flex flex-col gap-3 text-sm">
				<p className="text-muted">{t("memoryPage.owner.brief.intro")}</p>
				<form
					className="flex flex-col gap-2"
					onSubmit={(e) => {
						e.preventDefault();
						// 同一个对话再点一次 = 重新生成
						const next = draft.trim();
						if (next === threadId) void q.refetch();
						else setThreadId(next);
					}}
				>
					<Field
						label={t("memoryPage.owner.brief.threadId")}
						hint={t("memoryPage.owner.brief.threadIdHint")}
					>
						<div className="flex min-w-0 flex-wrap gap-2">
							<TextInput
								value={draft}
								onChange={(e) => setDraft(e.target.value)}
								autoComplete="off"
								spellCheck={false}
								className="min-w-0 flex-1 basis-56 font-mono"
							/>
							<Button type="submit" icon="refresh" disabled={q.isFetching}>
								{t("memoryPage.owner.brief.generate")}
							</Button>
						</div>
					</Field>
				</form>
				{q.isPending ? (
					<Spinner />
				) : q.error ? (
					<MemoryError step="ownerLoadBrief" error={q.error} />
				) : b ? (
					<div className="flex flex-col gap-2">
						<div className="flex flex-wrap items-center gap-2 text-faint text-xs">
							<span>
								{t("memoryPage.owner.brief.generatedAt", {
									when: formatDateTime(b.generatedAt),
								})}
							</span>
							<Badge tone={b.cached ? "ok" : "neutral"}>
								{b.cached
									? t("memoryPage.owner.brief.cached")
									: t("memoryPage.owner.brief.fresh")}
							</Badge>
						</div>
						{b.content ? (
							<pre
								data-testid="owner-memory-brief"
								className="max-h-96 overflow-auto whitespace-pre-wrap break-words rounded-md bg-inset px-3 py-2 font-mono text-xs"
							>
								{b.content}
							</pre>
						) : (
							<Callout tone="neutral" role="status">
								{t("memoryPage.owner.brief.empty")}
							</Callout>
						)}
					</div>
				) : null}
			</div>
		</Dialog>
	);
}
