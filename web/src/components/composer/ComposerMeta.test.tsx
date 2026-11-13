import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { mockSpeedCaps } from "../../mock/fixtures";
import type { OmpCommand, SessionState } from "../../omp/types";
import { realtime } from "../../realtime/socket";
import { getThreadStore, resetThreadStores } from "../../state/stores";
import { ModelChip, SessionReadout } from "./ComposerMeta";

const TID = "T-meta-test";

/** SessionReadout 里的成本读数要 QueryClient（用量查询失败时退回转录累计） */
function readout() {
	const client = new QueryClient({
		defaultOptions: { queries: { retry: false } },
	});
	const wrapper = ({ children }: { children: ReactNode }) => (
		<QueryClientProvider client={client}>{children}</QueryClientProvider>
	);
	return render(<SessionReadout threadId={TID} />, { wrapper });
}

function setState(patch: Partial<SessionState>) {
	getThreadStore(TID)
		.getState()
		.patchLocal({
			state: {
				model: {
					id: "claude-sonnet-5",
					name: "Claude Sonnet 5",
					provider: "anthropic",
				},
				thinkingLevel: "high",
				thinkingConfigured: "high",
				availableThinkingLevels: ["off", "low", "high"],
				tokensPerSecond: 64,
				...mockSpeedCaps("anthropic"),
				...patch,
			} as unknown as SessionState,
		});
}

const sent: OmpCommand[] = [];

beforeEach(() => {
	sent.length = 0;
	vi.spyOn(realtime, "cmd").mockImplementation(async (_tid, frame) => {
		if (frame.type === "get_state")
			return getThreadStore(TID).getState().state as never;
		sent.push(frame);
		return {} as never;
	});
});

afterEach(() => {
	vi.restoreAllMocks();
	resetThreadStores();
});

describe("ModelChip 思考级别菜单", () => {
	it("点圆圈列出模型支持的级别（加 auto），勾选当前配置，选中后发 set_thinking_level", async () => {
		setState({});
		render(<ModelChip threadId={TID} />);
		fireEvent.click(screen.getByRole("button", { name: /思考级别：高/ }));
		const items = screen.getAllByRole("menuitemradio");
		expect(items.map((i) => i.textContent)).toEqual([
			"自动",
			"关闭",
			"低",
			"高",
		]);
		expect(screen.getByRole("menuitemradio", { name: "高" })).toHaveAttribute(
			"aria-checked",
			"true",
		);
		fireEvent.click(screen.getByRole("menuitemradio", { name: "低" }));
		await waitFor(() =>
			expect(sent).toEqual([{ type: "set_thinking_level", level: "low" }]),
		);
	});

	it("模型不推理时没有圆圈按钮", () => {
		setState({ availableThinkingLevels: [] });
		render(<ModelChip threadId={TID} />);
		expect(screen.queryByRole("button", { name: /思考级别/ })).toBeNull();
	});
});

describe("SessionReadout 速度档位", () => {
	it("只列当前模型支持的档位：Claude 有快速与全局慢速、没有极速", () => {
		setState({});
		readout();
		fireEvent.click(screen.getByRole("button", { name: /速度：标准/ }));
		expect(
			screen.getAllByRole("menuitemradio").map((i) => i.textContent),
		).toEqual(["快速", "标准", "慢速所有会话"]);
	});

	it("OpenAI 模型多一档极速", () => {
		setState({ ...mockSpeedCaps("openai") });
		readout();
		fireEvent.click(screen.getByRole("button", { name: /速度：标准/ }));
		expect(
			screen.getAllByRole("menuitemradio").map((i) => i.textContent),
		).toEqual(["极速", "快速", "标准", "慢速"]);
	});

	it("快速切到慢速：先关快速再开慢速", async () => {
		setState({ fastModeEnabled: true, fastModeActive: true });
		readout();
		fireEvent.click(screen.getByRole("button", { name: /速度：快速/ }));
		fireEvent.click(screen.getByRole("menuitemradio", { name: /慢速/ }));
		await waitFor(() =>
			expect(sent).toEqual([
				{ type: "set_fast_mode", enabled: false },
				{ type: "set_slow_mode", enabled: true },
			]),
		);
	});

	it("模型只有标准档时是静态读数，不是按钮", () => {
		setState({ ...mockSpeedCaps("google-vertex") });
		readout();
		expect(screen.queryByRole("button", { name: /速度/ })).toBeNull();
		expect(screen.getByText(/64/)).toBeInTheDocument();
	});
});
