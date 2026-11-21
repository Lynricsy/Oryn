/**
 * `confirmAction` 的宿主：用通用 Dialog 渲染当前确认请求。标题是问句、可换行；正文是后果说明；
 * 页脚「取消」与动作按钮，初始焦点在「取消」（误按 Enter 不会执行删除这类操作）。
 */
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { useStore } from "zustand";
import { confirmStore, settleConfirm } from "../../lib/confirm";
import { Button, Dialog } from "./primitives";

export function ConfirmHost(): ReactNode {
	const { t } = useTranslation();
	const pending = useStore(confirmStore, (s) => s.pending);
	return (
		<Dialog
			open={pending !== null}
			onClose={() => settleConfirm(false)}
			title={pending?.title ?? ""}
			wrapTitle
			footer={
				<>
					<Button
						variant="ghost"
						data-testid="confirm-cancel"
						autoFocus
						onClick={() => settleConfirm(false)}
					>
						{t("common.cancel")}
					</Button>
					<Button
						variant={pending?.danger ? "danger" : "primary"}
						data-testid="confirm-ok"
						onClick={() => settleConfirm(true)}
					>
						{pending?.action}
					</Button>
				</>
			}
		>
			{pending?.body && <p className="text-muted text-sm">{pending.body}</p>}
		</Dialog>
	);
}
