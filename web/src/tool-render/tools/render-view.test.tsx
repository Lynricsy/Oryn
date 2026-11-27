/** 视图的直出渲染器（M33，docs/plan-m33.md 的 C3 / C10）：不出卡片外壳、规格来源优先级、各状态下的可用性。 */
import { render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import type { ViewAnswer } from "../../lib/view/answer";
import { ToolView, type ToolViewProps } from "../ToolView";
import type { ViewHost } from "../types";

const ARGS = {
	title: "选一个",
	blocks: [
		{
			type: "actions",
			buttons: [
				{ label: "好", prompt: "好的" },
				{ label: "不", prompt: "不要" },
			],
		},
	],
};

function host(
	opts: { awaiting?: boolean; answers?: Map<string, ViewAnswer> } = {},
): ViewHost {
	return {
		answers: () => opts.answers ?? new Map(),
		awaiting: () => opts.awaiting ?? false,
		submit: vi.fn(async () => undefined),
	};
}

function show(props: Partial<ToolViewProps>) {
	return render(
		<ToolView name="render_ui" toolCallId="call_1" args={ARGS} {...props} />,
	);
}

const ok = { content: [{ type: "text", text: "Rendered." }] };

describe("render_ui 直出", () => {
	it("不出工具卡外壳；已结束时可作答", () => {
		const { container } = show({
			result: { ...ok, details: { view: { outcome: "rendered" } } },
			host: { view: host() },
		});
		expect(container.querySelector(".tv-card")).toBeNull();
		expect(screen.getByRole("button", { name: "好" })).toBeEnabled();
	});

	it("参数没通过校验（isError 且没有 details.view）：只留一行提示", () => {
		show({
			result: {
				content: [{ type: "text", text: "Validation failed for tool" }],
				isError: true,
			},
			host: { view: host() },
		});
		expect(screen.getByTestId("view-failed")).toBeInTheDocument();
		expect(screen.queryByRole("button", { name: "好" })).toBeNull();
	});

	it("参数流式中、或开始执行但没有挂起请求时，控件禁用", () => {
		const { unmount } = show({ streaming: true, host: { view: host() } });
		expect(screen.getByRole("button", { name: "好" })).toBeDisabled();
		unmount();
		show({ started: true, running: true, host: { view: host() } });
		expect(screen.getByRole("button", { name: "好" })).toBeDisabled();
	});

	it("执行中且挂起等待作答时可作答，并提示在等你", () => {
		show({
			started: true,
			running: true,
			host: { view: host({ awaiting: true }) },
		});
		expect(screen.getByRole("button", { name: "好" })).toBeEnabled();
		expect(screen.getByTestId("view-awaiting")).toBeInTheDocument();
	});

	it("只读（没有 submit）或 callId 不能放进回执时全禁用", () => {
		const result = { ...ok, details: { view: { outcome: "rendered" } } };
		const { unmount } = show({
			result,
			host: { view: { answers: () => new Map(), awaiting: () => false } },
		});
		expect(screen.getByRole("button", { name: "好" })).toBeDisabled();
		unmount();
		show({ result, toolCallId: 'bad "id"', host: { view: host() } });
		expect(screen.getByRole("button", { name: "好" })).toBeDisabled();
	});

	it("规格以结果的 details.view.spec 为准，其次是已修复的参数", () => {
		const repaired = {
			blocks: [
				{ type: "actions", buttons: [{ label: "修复后", prompt: "p" }] },
			],
		};
		// 原始参数里 blocks 被二次编码成字符串：归一化取不到块
		const raw = { blocks: JSON.stringify(repaired.blocks) };
		const { unmount } = show({
			args: raw,
			liveArgs: repaired,
			started: true,
			running: true,
			host: { view: host() },
		});
		expect(screen.getByRole("button", { name: "修复后" })).toBeInTheDocument();
		unmount();
		show({
			args: raw,
			result: {
				...ok,
				details: { view: { outcome: "rendered", spec: repaired } },
			},
			host: { view: host() },
		});
		expect(screen.getByRole("button", { name: "修复后" })).toBeEnabled();
	});

	it("还没流到第一个完整的块：骨架", () => {
		show({ args: { blocks: [{}] }, streaming: true, running: true });
		expect(screen.getByTestId("view-pending")).toBeInTheDocument();
	});
});
