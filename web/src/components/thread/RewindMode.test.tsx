import {
	act,
	fireEvent,
	render,
	screen,
	waitFor,
} from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Entry } from "../../api/client";
import {
	getComposerDraft,
	patchComposerDraft,
} from "../../state/composer-drafts";
import { getThreadStore, resetThreadStores } from "../../state/stores";
import { toastsStore } from "../../state/toasts";
import { RewindDockBar, RewindTopPill, useRewindMode } from "./RewindMode";

const cmdMock = vi.fn();
vi.mock("../../realtime/socket", () => ({
	realtime: {
		cmd: (...args: unknown[]) => cmdMock(...args),
	},
}));

const THREAD_ID = "th_rewind_test";

function sampleEntries(): Entry[] {
	return [
		{
			seq: 1,
			file: "s1.jsonl",
			entryId: "u1",
			type: "message",
			payload: { message: { role: "user", content: "第一轮提问" } },
		},
		{
			seq: 2,
			file: "s1.jsonl",
			entryId: "a1",
			parentId: "u1",
			type: "message",
			payload: {
				message: {
					role: "assistant",
					content: [{ type: "text", text: "第一轮回复" }],
				},
			},
		},
		{
			seq: 3,
			file: "s1.jsonl",
			entryId: "u2",
			parentId: "a1",
			type: "message",
			payload: { message: { role: "user", content: "检查 nginx 日志" } },
		},
		{
			seq: 4,
			file: "s1.jsonl",
			entryId: "a2",
			parentId: "u2",
			type: "message",
			payload: {
				message: {
					role: "assistant",
					content: [
						{
							type: "toolCall",
							id: "tc1",
							name: "bash",
							intent: "查 nginx 报错",
							arguments: { command: "tail -n 20 /var/log/nginx/error.log" },
						},
					],
				},
			},
		},
		{
			seq: 5,
			file: "s1.jsonl",
			entryId: "tr1",
			parentId: "a2",
			type: "message",
			payload: {
				message: {
					role: "toolResult",
					toolCallId: "tc1",
					toolName: "bash",
					content: [{ type: "text", text: "502 Bad Gateway" }],
				},
			},
		},
		{
			seq: 6,
			file: "s1.jsonl",
			entryId: "a3",
			parentId: "tr1",
			type: "message",
			payload: {
				message: {
					role: "assistant",
					content: [{ type: "text", text: "上游网关返回 502" }],
				},
			},
		},
		// 在 a1 处留一条兄弟分支，测 ←/→ 切换分支
		{
			seq: 7,
			file: "s1.jsonl",
			entryId: "u2_alt",
			parentId: "a1",
			type: "message",
			payload: { message: { role: "user", content: "另一条分支的问题" } },
		},
		{
			seq: 8,
			file: "s1.jsonl",
			entryId: "a2_alt",
			parentId: "u2_alt",
			type: "message",
			payload: {
				message: {
					role: "assistant",
					content: [{ type: "text", text: "另一条分支的回答" }],
				},
			},
		},
	];
}

function Harness({
	entries,
	leafEntryId,
	active = true,
	startAt,
	onClose = () => {},
}: {
	entries: Entry[];
	leafEntryId: string;
	active?: boolean;
	startAt?: string;
	onClose?: () => void;
}) {
	const ctrl = useRewindMode({
		threadId: THREAD_ID,
		entries,
		leafEntryId,
		active,
		startAt,
		onClose,
	});
	return (
		<div
			data-testid="harness"
			data-selected-key={ctrl.transcript?.selectedKey ?? ""}
			data-selected-target={ctrl.selectedTarget?.targetEntryId ?? ""}
			data-dropped={[...(ctrl.transcript?.droppedKeys ?? [])].join(",")}
			data-expanded={ctrl.toolsExpand.expanded ? "1" : "0"}
			data-expand-epoch={ctrl.toolsExpand.epoch}
			data-preview-leaf={ctrl.previewLeafId ?? ""}
		>
			<RewindTopPill ctrl={ctrl} top={56} />
			<RewindDockBar ctrl={ctrl} />
		</div>
	);
}

beforeEach(() => {
	resetThreadStores();
	cmdMock.mockReset();
	toastsStore.setState({ toasts: [] });
	patchComposerDraft(THREAD_ID, { text: "未发出的草稿" });
});

afterEach(() => {
	resetThreadStores();
});

describe("RewindMode", () => {
	it("默认框选所有可 rewind 对象的最后一个（含工具调用），支持 ↑/↓ 逐条与 ←/→ 用户回合和兄弟分支切换", () => {
		// 故意把当前叶子设在工具调用结果 tr1（最后一条是工具调用，不是用户消息！）
		const entries = sampleEntries().filter((e) => e.seq <= 5);
		getThreadStore(THREAD_ID)
			.getState()
			.apply([{ type: "entries", msg: { entries, leafEntryId: "tr1" } }]);

		render(<Harness entries={entries} leafEntryId="tr1" />);
		const h = screen.getByTestId("harness");
		// 4 个目标：u1(e1), a1(e2), u2(e3), a2+tr1(e4)；默认选中最后一条 e4（工具调用）
		expect(screen.getByTestId("rewind-counter")).toHaveTextContent("4/4");
		expect(h).toHaveAttribute("data-selected-key", "e4");
		expect(h).toHaveAttribute("data-selected-target", "tr1");
		expect(h).toHaveAttribute("data-dropped", "");

		// ↑ 步进到用户消息 u2(e3)，e4 标为将被裁掉
		fireEvent.keyDown(window, { key: "ArrowUp" });
		expect(screen.getByTestId("rewind-counter")).toHaveTextContent("3/4");
		expect(h).toHaveAttribute("data-selected-key", "e3");
		expect(h).toHaveAttribute("data-dropped", "e4");

		// ↑ 再步进到助手回复 a1(e2)
		fireEvent.keyDown(window, { key: "ArrowUp" });
		expect(screen.getByTestId("rewind-counter")).toHaveTextContent("2/4");
		expect(h).toHaveAttribute("data-selected-key", "e2");
		expect(h).toHaveAttribute("data-dropped", "e3,e4");

		// ← 直接跳到上一个用户回合 u1(e1)
		fireEvent.keyDown(window, { key: "ArrowLeft" });
		expect(screen.getByTestId("rewind-counter")).toHaveTextContent("1/4");
		expect(h).toHaveAttribute("data-selected-key", "e1");

		// → 跳回下一个用户回合 u2(e3)
		fireEvent.keyDown(window, { key: "ArrowRight" });
		expect(screen.getByTestId("rewind-counter")).toHaveTextContent("3/4");
		expect(h).toHaveAttribute("data-selected-key", "e3");
	});

	it("在分叉点支持 ←/→ 切换兄弟分支预览，Ctrl+O 切换全量展开，f 过滤后 Esc 保留选中", () => {
		const entries = sampleEntries();
		const onClose = vi.fn();
		getThreadStore(THREAD_ID)
			.getState()
			.apply([{ type: "entries", msg: { entries, leafEntryId: "a3" } }]);

		render(<Harness entries={entries} leafEntryId="a3" onClose={onClose} />);
		const h = screen.getByTestId("harness");
		// 5 个目标：u1, a1, u2, a2+tr1, a3；默认选中 5/5 (a3)
		expect(screen.getByTestId("rewind-counter")).toHaveTextContent("5/5");

		// 移到分叉点 u2（↑↑）：显示分支 1/2
		fireEvent.keyDown(window, { key: "ArrowUp" });
		fireEvent.keyDown(window, { key: "ArrowUp" });
		expect(h).toHaveAttribute("data-selected-key", "e3");
		expect(screen.getByTestId("rewind-branch-badge")).toHaveTextContent("1/2");

		// → 切换到兄弟分支预览（叶子变为 a2_alt，框选落到 u2_alt = e7）
		fireEvent.keyDown(window, { key: "ArrowRight" });
		expect(screen.getByTestId("rewind-branch-badge")).toHaveTextContent("2/2");
		expect(h).toHaveAttribute("data-preview-leaf", "a2_alt");
		expect(h).toHaveAttribute("data-selected-key", "e7");

		// ← 切回当前分支
		fireEvent.keyDown(window, { key: "ArrowLeft" });
		expect(h).toHaveAttribute("data-preview-leaf", "a3");
		expect(h).toHaveAttribute("data-selected-key", "e3");

		// Ctrl+O 切换全量展开
		fireEvent.keyDown(window, { key: "o", ctrlKey: true });
		expect(h).toHaveAttribute("data-expanded", "1");
		expect(h).toHaveAttribute("data-expand-epoch", "1");
		fireEvent.keyDown(window, { key: "o", ctrlKey: true });
		expect(h).toHaveAttribute("data-expanded", "0");

		// f 打开过滤，搜索 toolResult 里的 502，命中 a2(e4) 与 a3(e6)
		fireEvent.keyDown(window, { key: "f" });
		const input = screen.getByTestId("rewind-filter-input");
		fireEvent.change(input, { target: { value: "bash 502" } });
		expect(h).toHaveAttribute("data-selected-key", "e4");
		expect(screen.getByTestId("rewind-counter")).toHaveTextContent("1/1");

		// Esc 退出过滤但保留 e4 选中，不关闭 Rewind 模式
		fireEvent.keyDown(window, { key: "Escape" });
		expect(onClose).not.toHaveBeenCalled();
		expect(screen.queryByTestId("rewind-filter-input")).toBeNull();
		expect(h).toHaveAttribute("data-selected-key", "e4");

		// 再按 Esc 才取消 Rewind 模式
		fireEvent.keyDown(window, { key: "Escape" });
		expect(onClose).toHaveBeenCalledTimes(1);
	});

	it("对工具调用按 Enter 发送折叠后的 toolResult id 且不覆盖输入区草稿；对最后一条非用户消息提示已在该位置", async () => {
		const entries = sampleEntries();
		const onClose = vi.fn();
		getThreadStore(THREAD_ID)
			.getState()
			.apply([{ type: "entries", msg: { entries, leafEntryId: "a3" } }]);
		cmdMock.mockResolvedValue({ cancelled: false, activeLeafId: "tr1" });

		render(<Harness entries={entries} leafEntryId="a3" onClose={onClose} />);
		// 先测试当前叶子 a3（非用户消息）直接按 Enter：不发 switch_leaf，提示已在该位置
		fireEvent.keyDown(window, { key: "Enter" });
		await waitFor(() => expect(onClose).toHaveBeenCalledTimes(1));
		expect(cmdMock).not.toHaveBeenCalled();
		expect(toastsStore.getState().toasts[0]?.text).toContain("位置");

		// 再把框移到工具调用 a2（折叠后 targetEntryId = tr1），按 Enter
		onClose.mockReset();
		toastsStore.setState({ toasts: [] });
		fireEvent.keyDown(window, { key: "ArrowUp" });
		fireEvent.keyDown(window, { key: "Enter" });
		await waitFor(() =>
			expect(cmdMock).toHaveBeenCalledWith(THREAD_ID, {
				type: "switch_leaf",
				entryId: "tr1",
			}),
		);
		await waitFor(() => expect(onClose).toHaveBeenCalledTimes(1));
		// 草稿未被覆盖，且弹出的撤销按钮可把叶子恢复回 a3
		expect(getComposerDraft(THREAD_ID).text).toBe("未发出的草稿");
		const undo = toastsStore.getState().toasts[0]?.action;
		expect(undo).toBeDefined();
		cmdMock.mockResolvedValueOnce({ cancelled: false, activeLeafId: "a3" });
		await act(async () => {
			undo?.run();
		});
		await waitFor(() =>
			expect(cmdMock).toHaveBeenLastCalledWith(THREAD_ID, {
				type: "switch_leaf",
				entryId: "a3",
			}),
		);
		expect(getComposerDraft(THREAD_ID).text).toBe("未发出的草稿");
	});
});
