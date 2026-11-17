/**
 * 输入框的下一条消息预测：幽灵字显示、Tab / → / 点击插入（从不发送）、Enter 照常发送已输入的文字、
 * 不匹配时隐藏、多个候选时插入后 Tab 轮换，新一轮开始（agent_start）时撤掉。
 */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, fireEvent, render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { beforeEach, describe, expect, it, vi } from "vitest";
import i18n from "../../i18n";
import { resetComposerDrafts } from "../../state/composer-drafts";
import { getThreadStore, resetThreadStores } from "../../state/stores";
import type { OmpFrame } from "../../state/thread-reducer";
import { Composer } from "./Composer";

const { cmd } = vi.hoisted(() => ({ cmd: vi.fn() }));
vi.mock("../../realtime/socket", () => ({
	realtime: {
		cmd: (...args: unknown[]) => cmd(...args),
		uiResponse: () => true,
		subscribe: () => () => undefined,
		focus: () => undefined,
	},
	OmpCommandError: class extends Error {},
}));

const A = "T-0193a1b2-0000-7000-8000-0000000000f1";

function mount() {
	const client = new QueryClient({
		defaultOptions: { queries: { retry: false } },
	});
	return render(
		<QueryClientProvider client={client}>
			<MemoryRouter>
				<Composer threadId={A} />
			</MemoryRouter>
		</QueryClientProvider>,
	);
}

function box(): HTMLTextAreaElement {
	return screen.getByRole("textbox", {
		name: i18n.t("composer.label"),
	}) as HTMLTextAreaElement;
}

function frame(f: OmpFrame) {
	act(() =>
		getThreadStore(A)
			.getState()
			.apply([{ type: "frame", frame: f }]),
	);
}

function predict(choices: string[]) {
	frame({ type: "oryn_composer_prediction", choices });
}

function ghost(): string | null {
	return screen.queryByTestId("composer-ghost")?.textContent ?? null;
}

function type(value: string) {
	fireEvent.change(box(), { target: { value } });
}

function key(k: string) {
	fireEvent.keyDown(box(), { key: k });
}

function prompts(): Record<string, unknown>[] {
	return cmd.mock.calls
		.map((c) => c[1] as Record<string, unknown>)
		.filter((f) => f.type === "prompt");
}

describe("Composer：下一条消息预测", () => {
	beforeEach(() => {
		cmd.mockReset();
		cmd.mockResolvedValue({});
		resetThreadStores();
		resetComposerDrafts();
	});

	it("空输入框显示幽灵字（代替占位文字），Tab 插入且不发送", () => {
		mount();
		expect(ghost()).toBeNull();
		predict(["Run the tests"]);
		expect(ghost()).toBe("Run the tests");
		expect(box().placeholder).toBe("");
		key("Tab");
		expect(box().value).toBe("Run the tests");
		expect(ghost()).toBeNull();
		expect(cmd).not.toHaveBeenCalled();
	});

	it("草稿是前缀时 → 补全剩余部分；输入不匹配的字符后隐藏", () => {
		mount();
		predict(["Run the tests"]);
		type("Run");
		expect(ghost()).toBe("Run the tests");
		expect(
			screen.getByTestId("composer-ghost").lastElementChild?.textContent,
		).toBe(" the tests");
		key("ArrowRight");
		expect(box().value).toBe("Run the tests");
		type("Run x");
		expect(ghost()).toBeNull();
		key("ArrowRight");
		expect(box().value).toBe("Run x");
	});

	it("光标不在末尾时 → 保持默认行为", () => {
		mount();
		predict(["Run the tests"]);
		type("Run");
		box().setSelectionRange(1, 1);
		fireEvent.select(box());
		expect(ghost()).toBeNull();
		key("ArrowRight");
		expect(box().value).toBe("Run");
	});

	it("Enter 照常发送已输入的文字，不带上预测", async () => {
		mount();
		predict(["Run the tests"]);
		type("Run");
		expect(ghost()).not.toBeNull();
		await act(async () => {
			key("Enter");
		});
		expect(prompts()).toMatchObject([{ message: "Run" }]);
	});

	it("点击幽灵字插入（触屏没有 Tab）", () => {
		mount();
		predict(["Ship it"]);
		const suffix = screen.getByTestId("composer-ghost").lastElementChild;
		if (!suffix) throw new Error("no ghost suffix");
		fireEvent.click(suffix);
		expect(box().value).toBe("Ship it");
		expect(cmd).not.toHaveBeenCalled();
	});

	it("多个候选：插入后不再延伸，Tab 轮换并显示序号", () => {
		mount();
		predict(["Run the tests", "Run the tests and fix failures", "Ship it"]);
		key("Tab");
		expect(box().value).toBe("Run the tests");
		expect(ghost()).toBeNull();
		expect(screen.getByRole("status")).toHaveTextContent("1/3");
		key("Tab");
		expect(box().value).toBe("Run the tests and fix failures");
		expect(screen.getByRole("status")).toHaveTextContent("2/3");
		key("Tab");
		key("Tab");
		expect(box().value).toBe("Run the tests");
		// 手动编辑后回到普通前缀匹配：草稿等于首个候选时幽灵字延伸到更长的那个
		type("Run the test");
		type("Run the tests");
		expect(screen.queryByRole("status")).toBeNull();
		expect(ghost()).toBe("Run the tests and fix failures");
	});

	it("插入后把光标移到中间或选中文字，Tab 不再整段替换", () => {
		mount();
		predict(["Run the tests", "Ship it"]);
		key("Tab");
		expect(box().value).toBe("Run the tests");
		expect(screen.getByRole("status")).toHaveTextContent("1/2");

		box().setSelectionRange(3, 3);
		fireEvent.select(box());
		expect(screen.queryByRole("status")).toBeNull();
		key("Tab");
		expect(box().value).toBe("Run the tests");

		box().setSelectionRange(4, 13);
		fireEvent.select(box());
		key("Tab");
		expect(box().value).toBe("Run the tests");
	});

	it("撤回或新一轮开始时隐藏", () => {
		mount();
		predict(["Run the tests"]);
		predict([]);
		expect(ghost()).toBeNull();
		predict(["Run the tests"]);
		frame({ type: "agent_start" });
		expect(ghost()).toBeNull();
		frame({ type: "agent_end", isTerminal: true });
		expect(ghost()).toBeNull();
	});

	it("agent 工作中不显示", () => {
		mount();
		frame({ type: "agent_start" });
		predict(["Run the tests"]);
		expect(ghost()).toBeNull();
		key("Tab");
		expect(box().value).toBe("");
	});
});
