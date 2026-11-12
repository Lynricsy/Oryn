import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Thread } from "../../api/client";
import { getThreadStore, resetThreadStores } from "../../state/stores";
import { terminalHub } from "../../state/terminals";
import { uiStore } from "../../state/ui";
import { RightPanel } from "./RightPanel";

vi.mock("../terminal/TerminalPanel", () => ({
	default: ({ threadId }: { threadId: string }) => (
		<div data-testid="stub-terminal">{threadId}</div>
	),
}));
vi.mock("../files/FilesPanel", () => ({
	default: ({ threadId, orbId }: { threadId: string; orbId?: string }) => (
		<div data-testid="stub-files">{`${threadId}|${orbId ?? ""}`}</div>
	),
}));
vi.mock("../changes/ChangesPanel", () => ({
	default: ({ orbId }: { threadId: string; orbId?: string }) => (
		<div data-testid="stub-changes">{orbId ?? ""}</div>
	),
}));

const TID = "T-rightpanel";

function renderPanel(onClose?: () => void) {
	const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
	return render(
		<QueryClientProvider client={qc}>
			<RightPanel threadId={TID} onClose={onClose} />
		</QueryClientProvider>,
	);
}

beforeEach(() => {
	resetThreadStores();
	uiStore.setState({ panelTab: "info", panelMax: false });
	getThreadStore(TID).setState({
		thread: { id: TID, orbId: "orb-rp", state: "idle" } as Thread,
	});
});

afterEach(() => {
	uiStore.setState({ panelTab: "info" });
});

describe("RightPanel", () => {
	it("四个标签；默认 Info 渲染信息面板", () => {
		renderPanel();
		const tabs = screen.getAllByRole("tab").map((t) => t.textContent);
		expect(tabs).toEqual(["信息", "改动", "文件", "终端"]);
		expect(screen.getByRole("tab", { name: "信息" })).toHaveAttribute(
			"aria-selected",
			"true",
		);
		expect(screen.getByText("orb")).toBeInTheDocument();
	});

	it("切换标签按需加载对应面板并传入 threadId / orbId", async () => {
		renderPanel();
		fireEvent.click(screen.getByRole("tab", { name: "文件" }));
		expect(uiStore.getState().panelTab).toBe("files");
		expect(await screen.findByTestId("stub-files")).toHaveTextContent(
			`${TID}|orb-rp`,
		);
		fireEvent.click(screen.getByRole("tab", { name: "改动" }));
		expect(await screen.findByTestId("stub-changes")).toHaveTextContent(
			"orb-rp",
		);
		fireEvent.click(screen.getByRole("tab", { name: "终端" }));
		expect(await screen.findByTestId("stub-terminal")).toHaveTextContent(TID);
	});

	it("有终端等待输入时自动切到 Terminal；离开后标签亮注意点", async () => {
		const detach = terminalHub.attach(TID);
		try {
			renderPanel();
			expect(screen.queryByTestId("terminal-attention")).toBeNull();
			terminalHub.onFrame(TID, {
				type: "terminal_opened",
				terminal: {
					terminalId: "c1",
					kind: "command",
					title: "read",
					cwd: "/",
					running: true,
					cols: 80,
					rows: 24,
				},
			});
			// 文档可见：自动切到 Terminal（每个终端只自动切一次）
			expect(uiStore.getState().panelTab).toBe("terminal");
			expect(screen.queryByTestId("terminal-attention")).toBeNull();
			fireEvent.click(screen.getByRole("tab", { name: "信息" }));
			expect(await screen.findByTestId("terminal-attention")).toBeVisible();
			fireEvent.click(screen.getByRole("tab", { name: "终端" }));
			expect(screen.queryByTestId("terminal-attention")).toBeNull();
		} finally {
			detach();
		}
	});

	it("关闭按钮调用 onClose", () => {
		const onClose = vi.fn();
		renderPanel(onClose);
		fireEvent.click(screen.getByRole("button", { name: "关闭" }));
		expect(onClose).toHaveBeenCalled();
	});
});
