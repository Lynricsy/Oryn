/** 命令菜单：TUI 专属命令不列、参数提示、高亮项展开完整描述、参数阶段的用法。 */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, within } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { beforeEach, describe, expect, it, vi } from "vitest";
import i18n from "../../i18n";
import type { SlashCommandInfo } from "../../omp/types";
import { getThreadStore, resetThreadStores } from "../../state/stores";
import { Composer } from "./Composer";

vi.mock("../../realtime/socket", () => ({
	realtime: {
		cmd: async () => ({}),
		uiResponse: () => true,
		subscribe: () => () => undefined,
		focus: () => undefined,
	},
	OmpCommandError: class extends Error {},
}));

const A = "T-0193a1b2-0000-7000-8000-0000000000f2";

const COMMANDS: SlashCommandInfo[] = [
	{ name: "hotkeys", source: "builtin", textModeExecutable: false },
	{ name: "branch", source: "builtin", textModeExecutable: false },
	{
		name: "effort",
		source: "builtin",
		description: "Set the thinking effort level for this session",
		input: { hint: "[level]" },
		allowArgs: true,
		textModeExecutable: true,
	},
	{
		name: "goal",
		source: "builtin",
		allowArgs: true,
		textModeExecutable: false,
		subcommands: [
			{
				name: "set",
				description: "Set or replace the goal",
				usage: "<objective>",
			},
			{
				name: "budget",
				description: "Adjust the token budget",
				usage: "<N|off>",
			},
		],
	},
	{ name: "skill:review", source: "skill", description: "Review a diff" },
];

function mount() {
	getThreadStore(A).getState().patchLocal({ commands: COMMANDS });
	const client = new QueryClient();
	render(
		<QueryClientProvider client={client}>
			<MemoryRouter>
				<Composer threadId={A} />
			</MemoryRouter>
		</QueryClientProvider>,
	);
	return screen.getByRole("textbox", { name: i18n.t("composer.label") });
}

function type(box: HTMLElement, value: string) {
	fireEvent.change(box, { target: { value } });
}

function labels(): string[] {
	return screen
		.getAllByRole("option")
		.map((o) => o.querySelector(".font-mono")?.textContent ?? "");
}

describe("CommandMenu", () => {
	beforeEach(() => resetThreadStores());

	it("只能在 TUI 执行、客户端没接管的命令不列；客户端接管的照列", () => {
		const box = mount();
		type(box, "/");
		const all = labels();
		expect(all).not.toContain("/hotkeys");
		expect(all).toContain("/branch");
		expect(all).toContain("/git");
		expect(all).toContain("/effort");
	});

	it("命令名后显示参数提示：客户端命令用自带提示，omp 命令用 input.hint", () => {
		const box = mount();
		type(box, "/ta");
		const tan = screen.getByRole("option", { selected: true });
		expect(within(tan).getByTestId("menu-hint")).toHaveTextContent("<work>");
		type(box, "/eff");
		expect(
			within(screen.getByRole("option", { selected: true })).getByTestId(
				"menu-hint",
			),
		).toHaveTextContent("[level]");
	});

	it("高亮项另起一行写全描述，方向键换高亮项后跟着换", () => {
		const box = mount();
		type(box, "/lo");
		const desc = i18n.t("slash.desc.loop");
		const active = screen.getByRole("option", { selected: true });
		expect(active).toHaveTextContent("/loop");
		const full = within(active).getByText(desc);
		expect(full.className).not.toMatch(/truncate/);
		type(box, "/");
		fireEvent.keyDown(box, { key: "ArrowDown" });
		const next = screen.getByRole("option", { selected: true });
		expect(
			within(next).getByText(i18n.t("slash.desc.plan-review")),
		).toBeTruthy();
		// 非高亮项单行截断，完整描述放进 title
		const first = screen.getAllByRole("option")[0] as HTMLElement;
		expect(first).toHaveAttribute("title", i18n.t("slash.desc.plan"));
	});

	it("参数阶段：列出子命令（含用法提示），底部给出该命令的用法", () => {
		const box = mount();
		type(box, "/goal ");
		expect(labels()).toEqual(["set", "budget"]);
		const usage = screen.getByTestId("menu-usage");
		expect(usage).toHaveTextContent("/goal");
		expect(usage).toHaveTextContent(i18n.t("slash.desc.goal"));
		type(box, "/goal b");
		expect(
			within(screen.getByRole("option", { selected: true })).getByTestId(
				"menu-hint",
			),
		).toHaveTextContent("<N|off>");
	});

	it("参数阶段没有候选时只显示用法，不显示「没有匹配的命令」", () => {
		const box = mount();
		type(box, "/tan 顺便修");
		expect(screen.queryByRole("listbox")).toBeNull();
		expect(screen.queryByText(i18n.t("composer.noCommands"))).toBeNull();
		expect(screen.getByTestId("menu-usage")).toHaveTextContent("<work>");
	});

	it("不认识的命令进入参数阶段后不弹菜单", () => {
		const box = mount();
		type(box, "/hotkeys x");
		expect(screen.queryByTestId("menu-usage")).toBeNull();
		type(box, "/nope x");
		expect(screen.queryByTestId("menu-usage")).toBeNull();
		expect(screen.queryByRole("listbox")).toBeNull();
	});
});
