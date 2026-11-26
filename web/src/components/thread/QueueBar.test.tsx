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

	it("混合乐观附件（系统分享仅文件图片 A + 可内联图片 B）：按附件 ID 各自取缩略图，点 A 不会看到 B", () => {
		const imgB =
			"iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg==";
		addOptimisticQueue(
			TID,
			"steering",
			"对比两张图",
			[{ type: "image", data: imgB, mimeType: "image/png" }],
			[
				{
					id: "att_01SHAREONLYAAAAAAAAAAAAAAA",
					name: "shared-a.jpg",
					mime: "image/jpeg",
					size: 10240,
				},
				{
					id: "att_01INLINEBBBBBBBBBBBBBBBB",
					name: "inline-b.png",
					mime: "image/png",
					size: 2048,
				},
			],
		);
		render(<QueueBar threadId={TID} />);

		const thumbA = screen.getByRole("img", { name: "shared-a.jpg" });
		const thumbB = screen.getByRole("img", { name: "inline-b.png" });
		expect(thumbA).toHaveAttribute(
			"src",
			"/api/v1/attachments/att_01SHAREONLYAAAAAAAAAAAAAAA/content?inline=true",
		);
		expect(thumbB).toHaveAttribute(
			"src",
			"/api/v1/attachments/att_01INLINEBBBBBBBBBBBBBBBB/content?inline=true",
		);

		// 点击 A：灯箱打开的必须是 A 自己的地址，绝不是 B 的内联 data URL
		fireEvent.click(screen.getByText("shared-a.jpg"));
		const dialog = screen.getByRole("dialog", { name: "shared-a.jpg" });
		expect(dialog.querySelector("img")?.getAttribute("src")).toBe(
			"/api/v1/attachments/att_01SHAREONLYAAAAAAAAAAAAAAA/content?inline=true",
		);
	});

	it("无附件 ID 且图片附件数与内联图数不等时不强行配对，内联图单独显示", () => {
		const imgB =
			"iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg==";
		addOptimisticQueue(
			TID,
			"steering",
			"混合无 ID",
			[{ type: "image", data: imgB, mimeType: "image/png" }],
			[
				{ name: "shared-a.jpg", mime: "image/jpeg", size: 10240 },
				{ name: "inline-b.png", mime: "image/png", size: 2048 },
			],
		);
		render(<QueueBar threadId={TID} />);

		// 附件芯片不错误绑定内联图 B，而是内联图 B 作为独立缩略图展示
		expect(screen.queryByRole("img", { name: "shared-a.jpg" })).toBeNull();
		expect(screen.queryByRole("img", { name: "inline-b.png" })).toBeNull();
		const standalone = screen.getByRole("img", { name: "图片 1" });
		expect(standalone).toHaveAttribute("src", `data:image/png;base64,${imgB}`);
		fireEvent.click(standalone);
		const dialog = screen.getByRole("dialog", { name: "图片 1" });
		expect(dialog.querySelector("img")?.getAttribute("src")).toBe(
			`data:image/png;base64,${imgB}`,
		);
	});

	it("已有纯附件 A 在队列中时再发纯附件 B：B 立即显示，无关 queue_update 不误删 B，直到 B 的权威条目到达才接管", () => {
		const attA =
			"<oryn-attachments>\nThe user attached these files.\n" +
			"- /home/user/.oryn/attachments/T-queue/att_01AAAAAAAAAAAAAAAAAAAAAAAA/first.pdf (application/pdf, 100 bytes)\n" +
			"</oryn-attachments>";
		const attB =
			"<oryn-attachments>\nThe user attached these files.\n" +
			"- /home/user/.oryn/attachments/T-queue/att_01BBBBBBBBBBBBBBBBBBBBBBBB/second.pdf (application/pdf, 200 bytes)\n" +
			"</oryn-attachments>";

		getThreadStore(TID)
			.getState()
			.apply([
				{
					type: "frame",
					frame: {
						type: "queue_update",
						steering: [
							{ id: "s1", text: attA, editable: true, timestamp: 1000 },
						],
						followUp: [],
					},
				},
			]);

		addOptimisticQueue(TID, "steering", "", undefined, [
			{
				id: "att_01BBBBBBBBBBBBBBBBBBBBBBBB",
				name: "second.pdf",
				mime: "application/pdf",
				size: 200,
			},
		]);

		render(<QueueBar threadId={TID} />);
		expect(screen.getByText("first.pdf")).toBeVisible();
		expect(screen.getByText("second.pdf")).toBeVisible();
		expect(screen.getByText("发送中")).toBeVisible();

		// 无关 queue_update（followUp 变动，steering 仍只有 s1）：不能误删在途的 second.pdf
		act(() => {
			getThreadStore(TID)
				.getState()
				.apply([
					{
						type: "frame",
						frame: {
							type: "queue_update",
							steering: [
								{ id: "s1", text: attA, editable: true, timestamp: 1000 },
							],
							followUp: [
								{
									id: "f1",
									text: "另外一条排队",
									editable: true,
									timestamp: 1001,
								},
							],
						},
					},
				]);
		});
		expect(screen.getByText("second.pdf")).toBeVisible();
		expect(screen.getByText("发送中")).toBeVisible();

		// 真正包含 s2（second.pdf）的 queue_update 到达：无缝接管，不重复
		act(() => {
			getThreadStore(TID)
				.getState()
				.apply([
					{
						type: "frame",
						frame: {
							type: "queue_update",
							steering: [
								{ id: "s1", text: attA, editable: true, timestamp: 1000 },
								{ id: "s2", text: attB, editable: true, timestamp: 1002 },
							],
							followUp: [],
						},
					},
				]);
		});
		expect(screen.getAllByText("first.pdf")).toHaveLength(1);
		expect(screen.getAllByText("second.pdf")).toHaveLength(1);
		expect(screen.queryByText("发送中")).toBeNull();
	});
});
