import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { ToolView } from "../ToolView";
import type { ToolResultLike } from "../types";

/**
 * 以 `write xd://<tool>` 派发完成的形式渲染一个设备卡片。参数校验通过后内层工具出错时
 * Core 同样带着 `xdev.args`（只是没有 inner），所以出错样例也走真实工具的渲染器。
 */
function renderDevice(
	tool: string,
	args: Record<string, unknown>,
	result?: { text: string; inner?: unknown; isError?: boolean },
	opts: { open?: boolean; running?: boolean } = {},
) {
	const res: ToolResultLike | undefined = result && {
		content: [{ type: "text", text: result.text }],
		details: { xdev: { tool, mode: "execute", args, inner: result.inner } },
		isError: result.isError,
	};
	return render(
		<ToolView
			name="write"
			args={{ path: `xd://${tool}`, content: JSON.stringify(args) }}
			result={res}
			running={opts.running}
			defaultOpen={opts.open}
		/>,
	);
}

const sum = (c: HTMLElement) => c.querySelector(".tv-sum");
const meta = (c: HTMLElement) => c.querySelector(".tv-meta");
const body = (c: HTMLElement) => c.querySelector(".tv-body");

describe("checkpoint / rewind 卡片", () => {
	it("checkpoint 摘要给出调查目标，展开体给目标全文与开始时间", () => {
		const goal = "弄清重连为什么丢消息";
		const { container } = renderDevice(
			"checkpoint",
			{ goal },
			{
				text: `Checkpoint: ${goal}\nFinish exploration and formulate findings.`,
				inner: { goal, startedAt: "2026-11-15T08:30:00.000Z" },
			},
			{ open: true },
		);
		expect(sum(container)).toHaveTextContent(goal);
		// 头部摘要会被窄屏截断：短目标也在展开体里给出全文
		expect(body(container)?.querySelector(".tv-prose")).toHaveTextContent(goal);
		expect(body(container)).toHaveTextContent("开始于");
		// 工具的固定提示语不重复展示
		expect(body(container)).not.toHaveTextContent("Finish exploration");
	});

	it("checkpoint 的长目标在展开体给出全文", () => {
		const goal = `${"弄清重连为什么丢消息，".repeat(8)}并确认服务端重放边界`;
		const { container } = renderDevice(
			"checkpoint",
			{ goal },
			{ text: "Checkpoint", inner: { goal, startedAt: "x" } },
			{ open: true },
		);
		expect(body(container)?.querySelector(".tv-prose")).toHaveTextContent(
			"并确认服务端重放边界",
		);
	});

	it("rewind 成功时标「已回退」，展开体按 Markdown 渲染报告", () => {
		const report = "## 结论\n\n重连时 **afterSeq** 没带上。";
		const { container } = renderDevice(
			"rewind",
			{ report },
			{
				text: "Rewind requested.\nReport captured for context replacement.",
				inner: { report, rewound: true },
			},
			{ open: true },
		);
		expect(sum(container)?.querySelector(".tv-badge--ok")).toHaveTextContent(
			"已回退",
		);
		// 摘要跳过标题、去掉 Markdown 记号
		expect(sum(container)).toHaveTextContent("已回退 重连时 afterSeq 没带上。");
		expect(screen.getByRole("heading", { name: "结论" })).toBeInTheDocument();
		expect(body(container)?.querySelector("strong")).toHaveTextContent(
			"afterSeq",
		);
	});

	it("rewind 出错时不标已回退，展开体给出错误文本", () => {
		const { container } = renderDevice(
			"rewind",
			{ report: "无" },
			{
				text: "No active checkpoint. Create a checkpoint before calling rewind.",
				isError: true,
			},
			{ open: true },
		);
		expect(sum(container)?.querySelector(".tv-badge--ok")).toBeNull();
		expect(body(container)).toHaveTextContent("No active checkpoint");
	});
});

describe("memory_edit 卡片", () => {
	it("update：摘要给操作、记忆 id 与新内容，读数给结果，展开体给重要度与记忆库", () => {
		const { container } = renderDevice(
			"memory_edit",
			{
				op: "update",
				id: "m-42",
				content: "部署走 restart.sh",
				importance: 0.8,
			},
			{
				text: "Memory m-42 updated in bank proj (working).",
				inner: { status: "updated", bank: "proj", store: "working" },
			},
			{ open: true },
		);
		expect(sum(container)).toHaveTextContent("更新");
		expect(sum(container)).toHaveTextContent("m-42");
		expect(sum(container)).toHaveTextContent("部署走 restart.sh");
		expect(meta(container)?.querySelector(".tv-meta-ok")).toHaveTextContent(
			"已更新",
		);
		expect(body(container)).toHaveTextContent("0.80");
		expect(body(container)).toHaveTextContent("proj · working");
		// 成功时不再重复工具原文
		expect(body(container)).not.toHaveTextContent("Memory m-42 updated");
	});

	it("invalidate 指向替代记忆；只读事实用警告读数并给出工具原文", () => {
		const { container } = renderDevice(
			"memory_edit",
			{ op: "invalidate", id: "f-7", replacement_id: "m-9" },
			{
				text: "Memory f-7 is a read-only fact in bank proj (fact); it cannot be edited. Read it with memory://f-7.",
				inner: { status: "not_editable", bank: "proj", store: "fact" },
			},
			{ open: true },
		);
		expect(sum(container)).toHaveTextContent("作废");
		expect(sum(container)).toHaveTextContent("→ m-9");
		expect(meta(container)?.querySelector(".tv-meta-warn")).toHaveTextContent(
			"只读事实",
		);
		expect(body(container)).toHaveTextContent("memory://f-7");
	});
});

describe("tts 卡片", () => {
	it("摘要给输出路径与文本，读数给音频大小，展开体给长文本全文与音色格式", () => {
		const text = `构建完成，三个测试失败。${"失败的是重连、回放与截图三组用例，".repeat(3)}详情见 CI。`;
		const { container } = renderDevice(
			"tts",
			{ text, output_path: "out/done.mp3", voice_id: "eve" },
			{
				text: "Saved 48213 bytes to out/done.mp3 (voice=eve, codec=mp3).",
				inner: {
					bytes: 48213,
					voiceId: "eve",
					codec: "mp3",
					backend: "xai-tts",
				},
			},
			{ open: true },
		);
		expect(sum(container)).toHaveTextContent("out/done.mp3");
		expect(sum(container)).toHaveTextContent("构建完成，三个测试失败。");
		expect(meta(container)).toHaveTextContent("47 KB");
		expect(body(container)?.querySelector(".tv-prose")).toHaveTextContent(text);
		expect(body(container)).toHaveTextContent("eve");
		expect(body(container)).toHaveTextContent("xai-tts");
	});

	it("全部候选失败时展开体给出错误文本", () => {
		const { container } = renderDevice(
			"tts",
			{ text: "你好", output_path: "a.mp3" },
			{
				text: "No available speech model matches the speech role.",
				isError: true,
			},
			{ open: true },
		);
		expect(container.querySelector(".tv-card--error")).not.toBeNull();
		expect(body(container)).toHaveTextContent("No available speech model");
		expect(meta(container)).toBeEmptyDOMElement();
	});
});

describe("xd://github pr_create", () => {
	it("派发只带 {op, repo} 时摘要用结果里的 PR 编号", () => {
		const { container } = renderDevice(
			"github",
			{ op: "pr_create", repo: "Lynricsy/Oryn" },
			{
				text: "Created PR #128: fix reconnect\nhttps://github.com/Lynricsy/Oryn/pull/128",
			},
		);
		expect(sum(container)).toHaveTextContent("pr_create");
		expect(sum(container)).toHaveTextContent("#128");
		expect(sum(container)).toHaveTextContent("Lynricsy/Oryn");
	});
});
