import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, fireEvent, render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import i18n from "../i18n";
import type { Attachment } from "../lib/attachments";
import type { SessionState } from "../omp/types";
import { MODEL_CAPS_KEY, modelCapsStore } from "../state/model-caps";
import { shareIntakeStore } from "../state/share-intake";
import { getThreadStore, resetThreadStores } from "../state/stores";
import { HomePage } from "./HomePage";

const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
	const url = new URL(
		typeof input === "object" && "url" in input ? input.url : String(input),
		"http://x",
	);
	if (url.pathname === "/api/v1/providers")
		return Response.json({ providers: [] });
	if (url.pathname === "/api/v1/projects")
		return Response.json({ projects: [] });
	if (url.pathname === "/api/v1/orbs") return Response.json({ orbs: [] });
	return Response.json({ code: "not_found", message: "" }, { status: 404 });
});

beforeEach(() => {
	vi.stubGlobal("fetch", fetchMock);
	localStorage.removeItem(MODEL_CAPS_KEY);
	modelCapsStore.setState({
		caps: {
			"openai/gpt-x": { thinking: ["low", "high"] },
			"openai/gpt-lite": { thinking: ["low"] },
			"acme/plain": { thinking: [] },
		},
	});
});

afterEach(() => {
	vi.unstubAllGlobals();
	resetThreadStores();
	shareIntakeStore.setState({ pending: null });
});

function renderHome() {
	const client = new QueryClient({
		defaultOptions: { queries: { retry: false } },
	});
	render(
		<QueryClientProvider client={client}>
			<MemoryRouter>
				<HomePage />
			</MemoryRouter>
		</QueryClientProvider>,
	);
}

/** 在模型弹层的输入框里填模型（弹层没开时先点开模型胶囊） */
function pickModel(model: string) {
	if (!screen.queryByPlaceholderText("provider/模型 id"))
		fireEvent.click(screen.getByRole("button", { name: "默认模型" }));
	fireEvent.change(screen.getByPlaceholderText("provider/模型 id"), {
		target: { value: model },
	});
}

function openThinking() {
	fireEvent.click(screen.getByRole("button", { name: /思考级别：/ }));
	return screen.getAllByRole("menuitemradio").map((i) => i.textContent);
}

describe("首页思考级别按所选模型的能力列出", () => {
	it("能力未知（默认模型）：只给「默认」并说明原因，不假装全部级别可用", () => {
		renderHome();
		expect(openThinking()).toEqual(["默认还不知道这个模型支持哪些级别"]);
	});

	it("见过的模型：列出它支持的级别（加自动）", () => {
		renderHome();
		pickModel("openai/gpt-x");
		expect(openThinking()).toEqual(["默认", "自动", "低", "高"]);
	});

	it("模型不推理：没有思考级别圆圈", () => {
		renderHome();
		pickModel("acme/plain");
		expect(screen.queryByRole("button", { name: /思考级别：/ })).toBeNull();
	});

	it("换到不支持已选级别的模型时退回「默认」", () => {
		renderHome();
		pickModel("openai/gpt-x");
		openThinking();
		fireEvent.click(screen.getByRole("menuitemradio", { name: "高" }));
		expect(
			screen.getByRole("button", { name: /思考级别：高/ }),
		).toBeInTheDocument();
		pickModel("openai/gpt-lite");
		expect(
			screen.getByRole("button", { name: /思考级别：默认/ }),
		).toBeInTheDocument();
	});
});

describe("模型能力缓存", () => {
	it("会话状态到达时按 provider/id 记下支持的级别，并存进本机", () => {
		getThreadStore("T-caps")
			.getState()
			.apply([
				{
					type: "state",
					state: {
						model: { provider: "anthropic", id: "claude-x", reasoning: true },
						availableThinkingLevels: ["off", "high"],
					} as unknown as SessionState,
				},
			]);
		expect(modelCapsStore.getState().caps["anthropic/claude-x"]).toEqual({
			thinking: ["off", "high"],
		});
		expect(
			JSON.parse(localStorage.getItem(MODEL_CAPS_KEY) ?? "{}")[
				"anthropic/claude-x"
			],
		).toEqual({ thinking: ["off", "high"] });
	});
});

describe("系统分享预填新建线程框", () => {
	const shot: Attachment = {
		id: "att_s1",
		name: "shot.png",
		mime: "image/png",
		size: 2048,
		sha256: "0".repeat(64),
		createdAt: 0,
	};

	function prompt(): HTMLTextAreaElement {
		return screen.getByRole("textbox", {
			name: i18n.t("newThread.prompt"),
		}) as HTMLTextAreaElement;
	}

	it("待取的分享：文字进输入框、附件成为条目、导入失败给出提示，然后取走", () => {
		shareIntakeStore.getState().set({
			id: "s-1",
			text: "https://example.com/post",
			attachments: [shot],
			failed: [
				{ name: "big.mov", reason: "too_large" },
				{ name: "x.bin", reason: "io" },
			],
		});
		renderHome();
		expect(prompt().value).toBe("https://example.com/post");
		expect(screen.getByText("shot.png")).toBeInTheDocument();
		expect(screen.getByRole("alert")).toHaveTextContent(
			i18n.t("mobile.shareFailed", { count: 2 }),
		);
		expect(shareIntakeStore.getState().pending).toBeNull();
	});

	it("已有内容时换行追加；没有失败项不显示提示", () => {
		renderHome();
		fireEvent.change(prompt(), { target: { value: "看看这个" } });
		act(() => {
			shareIntakeStore.getState().set({
				id: "s-2",
				text: "分享的文字",
				attachments: [],
				failed: [],
			});
		});
		expect(prompt().value).toBe("看看这个\n分享的文字");
		expect(screen.queryByRole("alert")).toBeNull();
		expect(shareIntakeStore.getState().pending).toBeNull();
	});
});
