/**
 * 设置 → Agent → 模型角色：上下移与拖动改 `cycleOrder`；模型写 `modelRoles`、显示名写 `modelTags.<role>.name`；
 * 新建 / 删除自定义 role；头像写 role 头像接口；config.yml 里的注释保留。
 */
import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { readConfigDoc } from "../../../lib/omp-config";
import { agentFetch, home, renderPage, resetHome } from "./agent-config-mock";
import RolesPage, { knownRoles, moveRole, rolesConfig } from "./RolesPage";

vi.mock("../../../components/editor/CodeEditor", () => ({
	default: () => null,
}));

let rolePuts: unknown[] = [];

/** 在 omp-home 文件接口之外补上 role 头像与模型目录。 */
const fetchMock = vi.fn(
	async (input: RequestInfo | URL, init?: RequestInit) => {
		const req =
			input instanceof Request
				? input
				: new Request(new URL(String(input), "http://x"), init);
		const url = new URL(req.url, "http://x");
		if (url.pathname === "/api/v1/settings/role-identities") {
			if (req.method === "PUT") {
				const body = (await req.json()) as { role: string };
				rolePuts.push(body);
				return Response.json({ ...body, inCycle: true });
			}
			return Response.json({
				roles: [
					{ role: "smol", inCycle: true },
					{ role: "default", inCycle: true },
					{ role: "slow", inCycle: true },
					{ role: "gone", inCycle: false, avatarIcon: "🦊" },
				],
			});
		}
		if (url.pathname === "/api/v1/models/catalog")
			return Response.json({
				models: [
					{
						provider: "openai",
						id: "gpt-5",
						selector: "openai/gpt-5",
						name: "GPT-5",
						kind: "chat",
					},
				],
				fetchedAt: 1,
				stale: false,
			});
		return agentFetch(input, init);
	},
);

function lastConfig(): Record<string, unknown> | undefined {
	const put = home.puts.filter((p) => p.path === "config.yml").at(-1);
	return put ? readConfigDoc(put.body).toJS() : undefined;
}

function roleRow(role: string): HTMLElement {
	const li = document.querySelector<HTMLElement>(`li[data-role="${role}"]`);
	if (!li) throw new Error(`no row ${role}`);
	return li;
}

beforeEach(() => {
	resetHome({});
	rolePuts = [];
	vi.stubGlobal("fetch", fetchMock);
});

afterEach(() => {
	vi.unstubAllGlobals();
});

describe("RolesPage", () => {
	it("标注作用范围；下移「快速」：PUT config.yml 的 cycleOrder 随之改变", async () => {
		renderPage(<RolesPage />);
		expect(
			screen.getByText("保存后下发到 orb，不作用于全局助手。"),
		).toBeInTheDocument();
		const down = await screen.findByRole("button", { name: "下移 快速" });
		await waitFor(() => expect(down).toBeEnabled());
		expect(screen.getByRole("button", { name: "上移 快速" })).toBeDisabled();
		fireEvent.click(down);
		await waitFor(() =>
			expect(lastConfig()).toEqual({
				cycleOrder: ["default", "smol", "slow"],
			}),
		);
	});

	it("拖动排序：把「快速」拖到「思考」上，写回 cycleOrder，保留注释", async () => {
		home.files["config.yml"] =
			"# 我的配置\ncycleOrder: [smol, default, slow] # 轮换\n";
		renderPage(<RolesPage />);
		await waitFor(() =>
			expect(screen.getByRole("button", { name: "下移 快速" })).toBeEnabled(),
		);
		const handle = roleRow("smol").querySelector("[draggable='true']");
		if (!handle) throw new Error("no drag handle");
		fireEvent.dragStart(handle);
		fireEvent.dragOver(roleRow("slow"));
		fireEvent.drop(roleRow("slow"));
		await waitFor(() =>
			expect(lastConfig()).toEqual({
				cycleOrder: ["default", "slow", "smol"],
			}),
		);
		const body = home.puts.at(-1)?.body ?? "";
		expect(body).toContain("# 我的配置");
		expect(body).toContain("# 轮换");
	});

	it("模型与显示名：失焦提交，写 modelRoles 与 modelTags", async () => {
		renderPage(<RolesPage />);
		fireEvent.click(await screen.findByRole("button", { name: "设置 默认" }));
		const model = screen.getByLabelText("默认 的模型");
		await waitFor(() => expect(model).toBeEnabled());
		fireEvent.change(model, { target: { value: "openai/gpt-5" } });
		fireEvent.blur(model);
		await waitFor(() =>
			expect(lastConfig()).toEqual({
				modelRoles: { default: "openai/gpt-5" },
			}),
		);
		const name = screen.getByLabelText("default 的显示名");
		fireEvent.change(name, { target: { value: "主力" } });
		fireEvent.keyDown(name, { key: "Enter" });
		await waitFor(() =>
			expect(lastConfig()).toEqual({
				modelRoles: { default: "openai/gpt-5" },
				modelTags: { default: { name: "主力" } },
			}),
		);
		expect(roleRow("default")).toHaveTextContent("主力");
	});

	it("新建自定义 role 加到轮换末尾；删除时清掉 cycleOrder、modelRoles 与 modelTags", async () => {
		home.files["config.yml"] =
			"modelRoles:\n  review: openai/gpt-5\nmodelTags:\n  review:\n    name: 审查\n";
		renderPage(<RolesPage />);
		// 只在 modelRoles 里的自定义 role 列在「其他角色」，可加入轮换
		const others = await screen.findByTestId("roles-others");
		expect(within(others).getByText("审查")).toBeInTheDocument();

		const create = screen.getByRole("button", { name: "新建角色" });
		await waitFor(() => expect(create).toBeEnabled());
		fireEvent.click(create);
		const dialog = await screen.findByRole("dialog");
		const id = within(dialog).getByLabelText(/^角色 ID/);
		fireEvent.change(id, { target: { value: "review" } });
		fireEvent.click(within(dialog).getByRole("button", { name: "创建" }));
		expect(within(dialog).getByText("已有同名条目。")).toBeInTheDocument();
		fireEvent.change(id, { target: { value: "draft" } });
		fireEvent.click(within(dialog).getByRole("button", { name: "创建" }));
		await waitFor(() =>
			expect(lastConfig()).toMatchObject({
				cycleOrder: ["smol", "default", "slow", "draft"],
			}),
		);

		fireEvent.click(screen.getByRole("button", { name: "设置 审查" }));
		fireEvent.click(
			within(roleRow("review")).getByRole("button", { name: "删除这个角色" }),
		);
		await waitFor(() =>
			expect(lastConfig()).toEqual({
				cycleOrder: ["smol", "default", "slow", "draft"],
			}),
		);
	});

	it("头像：选字形写 role 头像接口；遗留项可清除头像", async () => {
		renderPage(<RolesPage />);
		fireEvent.click(await screen.findByRole("button", { name: "设置 思考" }));
		fireEvent.click(
			within(roleRow("slow")).getByRole("button", { name: "🦊" }),
		);
		await waitFor(() =>
			expect(rolePuts).toEqual([{ role: "slow", avatarIcon: "🦊" }]),
		);
		const gone = await waitFor(() => roleRow("gone"));
		fireEvent.click(within(gone).getByRole("button", { name: "清除头像" }));
		await waitFor(() => expect(rolePuts).toHaveLength(2));
		expect(rolePuts[1]).toEqual({ role: "gone" });
	});
});

describe("rolesConfig / knownRoles / moveRole", () => {
	it("缺省 cycleOrder；已知 role 依次是轮换、modelRoles 其余键、内置 role", () => {
		const cfg = rolesConfig(
			readConfigDoc("modelRoles:\n  zeta: a/b\n  smol: [x/y, z/w]\n"),
		);
		expect(cfg.cycle).toEqual(["smol", "default", "slow"]);
		expect(cfg.models).toEqual({ zeta: "a/b", smol: "x/y, z/w" });
		expect(knownRoles(cfg).slice(0, 5)).toEqual([
			"smol",
			"default",
			"slow",
			"zeta",
			"vision",
		]);
	});

	it("moveRole：越界收拢，位置不变返回 null", () => {
		expect(moveRole(["a", "b", "c"], "a", 5)).toEqual(["b", "c", "a"]);
		expect(moveRole(["a", "b", "c"], "c", 0)).toEqual(["c", "a", "b"]);
		expect(moveRole(["a", "b"], "a", 0)).toBeNull();
		expect(moveRole(["a", "b"], "x", 0)).toBeNull();
	});
});
