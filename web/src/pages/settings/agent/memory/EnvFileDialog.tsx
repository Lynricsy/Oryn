/**
 * 生成 Hindsight 的 `.env`（R3）：生成即轮换 Hindsight API key 与中转令牌，旧值立即失效。
 * 内容只显示这一次：关闭对话框就丢弃，不进查询缓存，再打开只能重新生成。
 */
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { useCreateMemoryEnvFile } from "../../../../api/memory-queries";
import { CopyButton } from "../../../../components/ui/CopyButton";
import { Callout } from "../../../../components/ui/data";
import { Button, Dialog } from "../../../../components/ui/primitives";
import { MemoryError } from "./errors";

export function EnvFileDialog({
	open,
	regenerate,
	onClose,
}: {
	open: boolean;
	/** 已经生成过：提醒旧密钥会立即失效 */
	regenerate: boolean;
	onClose: () => void;
}): ReactNode {
	const { t } = useTranslation();
	const gen = useCreateMemoryEnvFile();
	const content = gen.data?.content;
	const close = () => {
		gen.reset();
		onClose();
	};
	return (
		<Dialog
			open={open}
			onClose={close}
			wide
			title={t("memoryPage.env.title")}
			footer={
				content === undefined ? (
					<>
						<Button variant="ghost" onClick={close}>
							{t("common.cancel")}
						</Button>
						<Button
							variant={regenerate ? "danger" : "primary"}
							icon="key"
							disabled={gen.isPending}
							onClick={() => gen.mutate()}
						>
							{regenerate
								? t("memoryPage.env.regenerate")
								: t("memoryPage.env.generate")}
						</Button>
					</>
				) : (
					<Button variant="primary" onClick={close}>
						{t("memoryPage.env.done")}
					</Button>
				)
			}
		>
			<div className="flex flex-col gap-3 text-sm">
				{content === undefined ? (
					<>
						<p className="text-muted">{t("memoryPage.env.intro")}</p>
						{regenerate && (
							<Callout tone="warn" title={t("memoryPage.env.rotateTitle")}>
								{t("memoryPage.env.rotateHint")}
							</Callout>
						)}
						<MemoryError step="generateEnv" error={gen.error} />
					</>
				) : (
					<>
						<Callout tone="warn" title={t("memoryPage.env.onceTitle")}>
							{t("memoryPage.env.onceHint")}
						</Callout>
						<div className="flex items-center justify-between gap-2">
							<span className="font-medium text-muted text-xs">.env</span>
							<CopyButton
								text={content}
								label={t("common.copy")}
								testId="memory-env-copy"
							/>
						</div>
						<pre
							data-testid="memory-env-content"
							className="max-h-80 overflow-auto whitespace-pre rounded-md bg-inset px-3 py-2 font-mono text-xs"
						>
							{content}
						</pre>
						<p className="text-faint text-xs">{t("memoryPage.env.next")}</p>
					</>
				)}
			</div>
		</Dialog>
	);
}
