import { act, fireEvent, render, screen } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { realtime } from "../../realtime/socket";
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

	it("带 <oryn-attachments> 的引导消息解析为正文 + 文件芯片，不暴露原始标签；编辑保存时保留附件块", async () => {
		const attBlock =
			"<oryn-attachments>\n" +
			"The user attached these files. They are stored outside the repository; read them when relevant and do not copy them into the repository unless asked.\n" +
			"- /home/user/.oryn/attachments/T-queue/att_01JCZ800000000000000000001/template.md (text/markdown, 2048 bytes)\n" +
			"</oryn-attachments>";
		const cmdSpy = vi
			.spyOn(realtime, "cmd")
			.mockResolvedValue({ steering: [], followUp: [] } as never);

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
								text: `每个报告要用这个模板写\n\n${attBlock}`,
								editable: true,
								timestamp: 3000,
							},
						],
						followUp: [],
					},
				},
			]);

		render(<QueueBar threadId={TID} />);
		expect(screen.getByText("每个报告要用这个模板写")).toBeVisible();
		expect(screen.getByText("template.md")).toBeVisible();
		expect(screen.queryByText(/<oryn-attachments>/)).toBeNull();

		// 点击编辑：输入框只显示纯正文，不含附件标签
		fireEvent.click(screen.getByRole("button", { name: "编辑" }));
		const input = screen.getByDisplayValue("每个报告要用这个模板写");
		fireEvent.change(input, { target: { value: "改成按新模板写" } });
		await act(async () => {
			fireEvent.click(screen.getByRole("button", { name: "保存" }));
		});

		expect(cmdSpy).toHaveBeenCalledWith(TID, {
			type: "queue_edit",
			queueId: "s1",
			text: `改成按新模板写\n\n${attBlock}`,
		});
		cmdSpy.mockRestore();
	});

	it("带附件的乐观引导项在权威 queue_update（追加了 <oryn-attachments>）到达后精准去重", () => {
		const attBlock =
			"<oryn-attachments>\n" +
			"The user attached these files.\n" +
			"- /home/user/.oryn/attachments/T-queue/att_01JCZ800000000000000000002/spec.pdf (application/pdf, 1024 bytes)\n" +
			"</oryn-attachments>";
		addOptimisticQueue(TID, "steering", "参考这份规范", undefined, [
			{ name: "spec.pdf", mime: "application/pdf", size: 1024 },
		]);
		render(<QueueBar threadId={TID} />);
		expect(screen.getByText("参考这份规范")).toBeVisible();
		expect(screen.getByText("spec.pdf")).toBeVisible();

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
									text: `参考这份规范\n\n${attBlock}`,
									editable: true,
									timestamp: 4000,
								},
							],
							followUp: [],
						},
					},
				]);
		});
		expect(screen.getAllByText("参考这份规范")).toHaveLength(1);
		expect(screen.queryByText("发送中")).toBeNull();
	});

	it("排队消息里的图片附件显示小预览缩略图，鼠标点击打开灯箱预览", () => {
		const attBlock =
			"<oryn-attachments>\n" +
			"The user attached these files.\n" +
			"- /home/user/.oryn/attachments/T-queue/att_01JCZ800000000000000000009/ui.png (image/png, 4096 bytes)\n" +
			"</oryn-attachments>";
		getThreadStore(TID)
			.getState()
			.apply([
				{
					type: "frame",
					frame: {
						type: "queue_update",
						steering: [
							{
								id: "s-img",
								text: `看看这张截图\n\n${attBlock}`,
								editable: true,
								timestamp: 5000,
							},
						],
						followUp: [],
					},
				},
			]);
		render(<QueueBar threadId={TID} />);
		const thumb = screen.getByRole("img", { name: "ui.png" });
		expect(thumb).toHaveAttribute(
			"src",
			"/api/v1/attachments/att_01JCZ800000000000000000009/content?inline=true",
		);
		fireEvent.click(screen.getByText("ui.png"));
		const dialog = screen.getByRole("dialog", { name: "ui.png" });
		expect(dialog.querySelector("img")?.getAttribute("src")).toBe(
			"/api/v1/attachments/att_01JCZ800000000000000000009/content?inline=true",
		);
	});
});
