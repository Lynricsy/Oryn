/** 线程页头的 PR 徽标（M14）：CI 状态点、状态文案与菜单操作。 */
import { act, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Schemas } from "../../api/client";
import i18n from "../../i18n";
import { PrChip } from "./PrChip";

const PULL: Schemas["ThreadPull"] = {
	repo: "oryn-demo/webapp",
	number: 128,
	title: "修复登录重定向循环",
	state: "open",
	draft: false,
	url: "https://github.com/oryn-demo/webapp/pull/128",
	checks: "success",
	listening: true,
};

beforeEach(() => {
	i18n.changeLanguage("zh-CN");
});

afterEach(() => {
	vi.unstubAllGlobals();
	vi.restoreAllMocks();
});

describe("PrChip", () => {
	it("CI 失败：徽标里有错误色状态点，标签写明状态与 CI", () => {
		render(<PrChip threadId="T-1" pull={{ ...PULL, checks: "failure" }} />);
		const chip = screen.getByTestId("pr-chip");
		expect(chip).toHaveAttribute("aria-label", "PR #128 · 打开 · CI 失败");
		expect(
			chip.querySelector('[data-checks="failure"] .bg-err'),
		).not.toBeNull();
		expect(chip).toHaveTextContent("#128");
	});

	it("没有 CI 汇总时不画状态点；草稿与关闭用弱化色，合并用合并图标与默认前景色", () => {
		const { rerender } = render(
			<PrChip
				threadId="T-1"
				pull={{ ...PULL, checks: undefined, draft: true }}
			/>,
		);
		const chip = () => screen.getByTestId("pr-chip");
		expect(chip().querySelector("[data-checks]")).toBeNull();
		expect(chip()).toHaveClass("text-faint");
		expect(chip()).toHaveAttribute("aria-label", "PR #128 · 草稿");
		rerender(<PrChip threadId="T-1" pull={{ ...PULL, state: "merged" }} />);
		expect(chip()).toHaveClass("text-fg");
		expect(chip()).toHaveAttribute("data-state", "merged");
	});

	it("菜单「暂停事件回流」发出 PUT …/pulls/128，body 为 { listening: false }", async () => {
		const fetch = vi.fn(
			async (_req: Request) =>
				new Response(JSON.stringify({}), {
					status: 200,
					headers: { "Content-Type": "application/json" },
				}),
		);
		vi.stubGlobal("fetch", fetch);
		render(<PrChip threadId="T-1" pull={PULL} />);
		fireEvent.click(screen.getByTestId("pr-chip"));
		await act(async () => {
			fireEvent.click(screen.getByRole("menuitem", { name: "暂停事件回流" }));
		});
		expect(fetch).toHaveBeenCalledTimes(1);
		const req = fetch.mock.calls[0]?.[0] as Request;
		expect(req.method).toBe("PUT");
		expect(new URL(req.url).pathname).toBe(
			"/api/v1/threads/T-1/github/pulls/128",
		);
		expect(await req.json()).toEqual({ listening: false });
	});

	it("已暂停时菜单是「恢复事件回流」；「在 GitHub 打开」新标签页打开 PR", () => {
		const open = vi.spyOn(window, "open").mockReturnValue(null);
		render(<PrChip threadId="T-1" pull={{ ...PULL, listening: false }} />);
		fireEvent.click(screen.getByTestId("pr-chip"));
		expect(
			screen.getByRole("menuitem", { name: "恢复事件回流" }),
		).toBeInTheDocument();
		fireEvent.click(screen.getByRole("menuitem", { name: "在 GitHub 打开" }));
		expect(open).toHaveBeenCalledWith(PULL.url, "_blank", "noopener");
	});
});
