/** 报告 / 目标 / 模型对话框测试共用：经 `ThreadDialogs` 打开对话框（各测试文件自己 mock `realtime/socket`）。 */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import type { Mock } from "vitest";
import { type DialogState, uiStore } from "../../../state/ui";
import { ThreadDialogs } from "../Dialogs";

export type Frame = { type: string } & Record<string, unknown>;
export type CmdMock = Mock<
	(threadId: string, frame: Frame) => Promise<unknown>
>;

export function openThreadDialog(threadId: string, dialog: DialogState) {
	uiStore.getState().open(dialog);
	return render(
		<QueryClientProvider
			client={
				new QueryClient({ defaultOptions: { queries: { retry: false } } })
			}
		>
			<MemoryRouter>
				<ThreadDialogs threadId={threadId} />
			</MemoryRouter>
		</QueryClientProvider>,
	);
}

/** mock 的 `realtime.cmd` 收到的、除 `get_*` / `list_*` 读取外的命令帧。 */
export function sentFrames(cmd: CmdMock): Frame[] {
	return cmd.mock.calls
		.map((c) => c[1])
		.filter((f) => !/^(get_|list_)/.test(f.type));
}

/** 某个读取命令被调用的次数。 */
export function readCount(cmd: CmdMock, type: string): number {
	return cmd.mock.calls.filter((c) => c[1].type === type).length;
}
