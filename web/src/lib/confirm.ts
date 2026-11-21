/**
 * 应用内确认（替代原生 `window.confirm`）：`confirmAction` 返回 Promise，用户点确认为 true，取消 / Esc / 点遮罩为 false。
 * 全局同时只有一个；新的请求会先把还开着的那个按取消结算。宿主见 `components/ui/ConfirmHost.tsx`（挂在 App 根上）。
 */
import { createStore } from "zustand/vanilla";

export interface ConfirmRequest {
	/** 问句，作对话框标题（可换行） */
	title: string;
	/** 后果说明；没有就只显示标题 */
	body?: string;
	/** 确认按钮上的动作词（「删除」「归档」……），不用笼统的「确定」 */
	action: string;
	/** 删除、吊销、清除这类不可撤销的操作：确认按钮用危险样式 */
	danger?: boolean;
}

interface PendingConfirm extends ConfirmRequest {
	resolve: (ok: boolean) => void;
}

export const confirmStore = createStore<{ pending: PendingConfirm | null }>(
	() => ({ pending: null }),
);

export function confirmAction(request: ConfirmRequest): Promise<boolean> {
	return new Promise((resolve) => {
		settleConfirm(false);
		confirmStore.setState({ pending: { ...request, resolve } });
	});
}

/** 结算当前确认并关闭对话框；没有打开的就什么也不做。 */
export function settleConfirm(ok: boolean): void {
	const pending = confirmStore.getState().pending;
	if (!pending) return;
	confirmStore.setState({ pending: null });
	pending.resolve(ok);
}
