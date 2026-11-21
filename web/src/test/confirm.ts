/**
 * 单测里作答应用内确认（`confirmAction`）：等确认请求出现，按 `ok` 结算，返回请求内容供断言标题、说明与动作词。
 * 对话框本身的渲染、键盘与焦点由 `ConfirmHost.test.tsx` 覆盖。
 */
import { act, waitFor } from "@testing-library/react";
import {
	type ConfirmRequest,
	confirmStore,
	settleConfirm,
} from "../lib/confirm";

export async function answerConfirm(ok: boolean): Promise<ConfirmRequest> {
	const request = await waitFor(() => {
		const pending = confirmStore.getState().pending;
		if (!pending) throw new Error("没有待作答的确认");
		const { resolve: _, ...rest } = pending;
		return rest;
	});
	await act(async () => {
		settleConfirm(ok);
	});
	return request;
}
