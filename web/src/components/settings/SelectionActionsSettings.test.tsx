/**
 * 设置 → 选区快捷动作（M22）：添加后整体 PUT、写回缓存并同步进菜单读的 selectionActionsStore；
 * 上移 / 删除整体替换；满 12 条时「添加动作」禁用。fetch 换成按路径记账的桩。
 */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
	act,
	fireEvent,
	render,
	screen,
	waitFor,
	within,
} from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Schemas } from "../../api/client";
import {
	selectionActionsStore,
	useSelectionActionsSync,
} from "../../state/selection-actions";
import SelectionActionsSettings from "./SelectionActionsSettings";

let stored: Schemas["SelectionActions"];
let puts: Schemas["SelectionActions"][];

async function handle(req: Request): Promise<Response> {
	const path = new URL(req.url, "http://x").pathname;
	if (path !== "/api/v1/settings/selection-actions")
		return Response.json({ code: "not_found", message: path }, { status: 404 });
	if (req.method === "PUT") {
		const body = (await req.json()) as Schemas["SelectionActions"];
		puts.push(body);
		stored = {
			actions: body.actions.map((a) => ({
				...a,
				name: a.name.trim(),
				prompt: a.prompt.trim(),
			})),
		};
	}
	return Response.json(stored);
}

/** AppShell 里做的同步，测试里单独挂一份。 */
function Sync() {
	useSelectionActionsSync(true);
	return null;
}

function mount() {
	const client = new QueryClient({
		defaultOptions: { queries: { retry: false } },
	});
	return render(
		<QueryClientProvider client={client}>
			<Sync />
			<SelectionActionsSettings />
		</QueryClientProvider>,
	);
}

const action = (id: string, name: string, prompt = `${name} 的提示词`) => ({
	id,
	name,
	prompt,
});

beforeEach(() => {
	stored = { actions: [] };
	puts = [];
	selectionActionsStore.setState({ actions: [] });
	vi.stubGlobal("fetch", (input: RequestInfo | URL, init?: RequestInit) =>
		handle(
			input instanceof Request
				? input
				: new Request(new URL(String(input), "http://x"), init),
		),
	);
});

afterEach(() => {
	vi.unstubAllGlobals();
	vi.restoreAllMocks();
});

describe("SelectionActionsSettings", () => {
	it("添加一条动作：PUT 规整前的列表，id 为 8 位 [a-z0-9]，菜单副本随之更新", async () => {
		mount();
		await screen.findByText("还没有自定义动作");
		fireEvent.click(screen.getByRole("button", { name: "添加动作" }));
		const dialog = await screen.findByRole("dialog");
		fireEvent.change(within(dialog).getByLabelText("名称"), {
			target: { value: " 挑毛病 " },
		});
		fireEvent.change(within(dialog).getByLabelText(/提示词/), {
			target: { value: "找出这段话里的漏洞\n第二行" },
		});
		fireEvent.click(within(dialog).getByRole("button", { name: "保存" }));
		await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
		expect(puts).toHaveLength(1);
		const sent = puts[0]?.actions[0];
		expect(sent?.id).toMatch(/^[a-z0-9]{8}$/);
		expect(sent).toMatchObject({
			name: "挑毛病",
			prompt: "找出这段话里的漏洞\n第二行",
		});
		await waitFor(() =>
			expect(selectionActionsStore.getState().actions).toEqual([
				{ id: sent?.id, name: "挑毛病", prompt: "找出这段话里的漏洞\n第二行" },
			]),
		);
		const row = screen.getByTestId("selection-action-row");
		expect(row.textContent).toContain("挑毛病");
		expect(row.textContent).toContain("找出这段话里的漏洞");
		expect(row.textContent).not.toContain("第二行");
	});

	it("名称或提示词为空白时不能保存", async () => {
		mount();
		await screen.findByText("还没有自定义动作");
		fireEvent.click(screen.getByRole("button", { name: "添加动作" }));
		const dialog = await screen.findByRole("dialog");
		fireEvent.change(within(dialog).getByLabelText("名称"), {
			target: { value: "名字" },
		});
		fireEvent.change(within(dialog).getByLabelText(/提示词/), {
			target: { value: "   " },
		});
		expect(
			(
				within(dialog).getByRole("button", {
					name: "保存",
				}) as HTMLButtonElement
			).disabled,
		).toBe(true);
	});

	it("上移整体替换顺序；删除先确认，取消则不发请求", async () => {
		stored = { actions: [action("aaaaaaaa", "甲"), action("bbbbbbbb", "乙")] };
		mount();
		const rows = await screen.findAllByTestId("selection-action-row");
		expect(
			(
				within(rows[0] as HTMLElement).getByRole("button", {
					name: "上移",
				}) as HTMLButtonElement
			).disabled,
		).toBe(true);
		fireEvent.click(
			within(rows[1] as HTMLElement).getByRole("button", { name: "上移" }),
		);
		await waitFor(() => expect(puts).toHaveLength(1));
		expect(puts[0]?.actions.map((a) => a.id)).toEqual(["bbbbbbbb", "aaaaaaaa"]);
		await waitFor(() =>
			expect(
				screen.getAllByTestId("selection-action-row")[0]?.textContent,
			).toContain("乙"),
		);

		const confirm = vi.spyOn(window, "confirm").mockReturnValue(false);
		const first = () =>
			screen.getAllByTestId("selection-action-row")[0] as HTMLElement;
		fireEvent.click(within(first()).getByRole("button", { name: "删除" }));
		expect(confirm).toHaveBeenCalledWith("删除这个动作？");
		expect(puts).toHaveLength(1);

		confirm.mockReturnValue(true);
		await act(async () => {
			fireEvent.click(within(first()).getByRole("button", { name: "删除" }));
		});
		await waitFor(() => expect(puts).toHaveLength(2));
		expect(puts[1]?.actions.map((a) => a.id)).toEqual(["aaaaaaaa"]);
		await waitFor(() =>
			expect(selectionActionsStore.getState().actions.map((a) => a.id)).toEqual(
				["aaaaaaaa"],
			),
		);
	});

	it("满 12 条时「添加动作」禁用并提示上限", async () => {
		stored = {
			actions: Array.from({ length: 12 }, (_, i) =>
				action(`act${String(i).padStart(5, "0")}`, `动作${i}`),
			),
		};
		mount();
		await waitFor(() =>
			expect(screen.getAllByTestId("selection-action-row")).toHaveLength(12),
		);
		expect(
			(screen.getByRole("button", { name: "添加动作" }) as HTMLButtonElement)
				.disabled,
		).toBe(true);
		expect(screen.getByText("最多 12 个")).toBeTruthy();
	});
});
