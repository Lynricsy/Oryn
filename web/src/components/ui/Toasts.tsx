/**
 * 操作结果的轻提示（`state/toasts.ts`）：屏幕底部居中的一列玻璃条，带可选的操作按钮（撤销、重试）与关闭。
 * 与右上角的通知 toast 分开放，互不遮挡。挂在 AppShell。
 */
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { dismissToast, type Toast, useToasts } from "../../state/toasts";
import { Icon } from "./icons";
import { Button, IconButton, Spinner } from "./primitives";

function ToastRow({ toast }: { toast: Toast }): ReactNode {
	const { t } = useTranslation();
	return (
		<div
			role={toast.tone === "err" ? "alert" : "status"}
			data-testid="action-toast"
			className="glass pop-in pointer-events-auto flex items-center gap-2 rounded-lg py-1.5 pr-1.5 pl-3"
		>
			{toast.busy ? (
				<span className="shrink-0 text-faint">
					<Spinner size={13} />
				</span>
			) : (
				toast.tone === "err" && (
					<Icon name="alert" size={14} className="shrink-0 text-err" />
				)
			)}
			<span className="min-w-0 flex-1 break-words text-fg text-sm">
				{toast.text}
			</span>
			{toast.action && (
				<Button
					size="sm"
					variant="ghost"
					onClick={() => {
						dismissToast(toast.id);
						toast.action?.run();
					}}
				>
					{toast.action.label}
				</Button>
			)}
			<IconButton
				icon="close"
				box="xs"
				size={13}
				label={t("common.close")}
				onClick={() => dismissToast(toast.id)}
			/>
		</div>
	);
}

export function Toasts(): ReactNode {
	const { t } = useTranslation();
	const toasts = useToasts();
	if (toasts.length === 0) return null;
	return (
		<section
			aria-label={t("toasts.region")}
			className="pointer-events-none fixed inset-x-0 bottom-4 z-[60] flex flex-col items-center gap-2 px-3 pb-safe"
		>
			{toasts.map((x) => (
				<div key={x.id} className="w-[min(26rem,100%)]">
					<ToastRow toast={x} />
				</div>
			))}
		</section>
	);
}
