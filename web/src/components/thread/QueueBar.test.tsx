import { act, render, screen } from "@testing-library/react";
import { beforeEach, describe, expect, it } from "vitest";
import {
	addOptimisticQueue,
	getThreadStore,
	removeOptimisticQueue,
	resetThreadStores,
	settleOptimisticQueue,
} from "../../state/stores";
import { QueueBar } from "./QueueBar";

const TID = "T-queue";

describe("QueueBar：引导与排队消息展示", () => {
	beforeEach(() => {
		resetThreadStores();
	});

	it("收到新版 omp 对象格式 queue_update 后立即渲染引导与排队消息（带可编辑控制）", () => {
		getThreadStore(TID)
			.getState()
			.apply([
				{
					type: "frame",
					frame: {
						type: "queue_update",
						steering: [
							{
								id: "s1",
								text: "先检查日志再改配置",
								editable: true,
								timestamp: 1000,
							},
						],
						followUp: [
							{
								id: "f1",
								text: "顺便跑下测试",
								editable: true,
								timestamp: 1001,
							},
						],
					},
				},
			]);

		render(<QueueBar threadId={TID} />);
		expect(screen.getByTestId("queue-bar")).toBeVisible();
		expect(screen.getByText("先检查日志再改配置")).toBeVisible();
		expect(screen.getByText("顺便跑下测试")).toBeVisible();
		expect(screen.getAllByRole("button", { name: "编辑" })).toHaveLength(2);
		expect(screen.getByRole("button", { name: "提前发送" })).toBeVisible();
	});

	it("发送引导消息时乐观项立即可见，权威 queue_update 到达后无缝接管不重复", () => {
		const optId = addOptimisticQueue(TID, "steering", "立刻停下来看报错");
		render(<QueueBar threadId={TID} />);
		expect(screen.getByText("立刻停下来看报错")).toBeVisible();
		expect(screen.getByText("发送中")).toBeVisible();

		// 服务端广播权威 queue_update，无缝替换乐观项且不出现重复条目
		act(() => {
			getThreadStore(TID)
				.getState()
				.apply([
					{
						type: "frame",
						frame: {
							type: "queue_update",
							steering: [
								{
									id: "s1",
									text: "立刻停下来看报错",
									editable: true,
									timestamp: 2000,
								},
							],
							followUp: [],
						},
					},
				]);
			settleOptimisticQueue(TID, optId);
		});
		expect(screen.getAllByText("立刻停下来看报错")).toHaveLength(1);
		expect(screen.queryByText("发送中")).toBeNull();
	});

	it("发送失败撤回乐观项后排队条自动消失", () => {
		const optId = addOptimisticQueue(TID, "steering", "失败测试");
		const { container } = render(<QueueBar threadId={TID} />);
		expect(screen.getByText("失败测试")).toBeVisible();
		act(() => {
			removeOptimisticQueue(TID, optId);
		});
		expect(container).toBeEmptyDOMElement();
	});
});
