/**
 * 项目设置「密钥与环境变量」（M24 阶段 ④）：列表显示（不保密的值可见、保密的只有徽标）、新增、Git 令牌总是保密、
 * 删除走应用内确认、粘贴 .env 批量导入（按顺序逐个 PUT，失败的留在对话框里）。数据走 mock 后端。
 */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
	fireEvent,
	render,
	screen,
	waitFor,
	within,
} from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Project } from "../../../api/client";
import { projects } from "../../../mock/fixtures";
import { MockServer } from "../../../mock/server";
import { answerConfirm } from "../../../test/confirm";
import { SecretsTab } from "./SecretsTab";

const ORYN = projects[0] as Project;
const BASE = `/api/v1/projects/${ORYN.id}/secrets`;

interface Call {
	method: string;
	path: string;
	body?: unknown;
}
let calls: Call[];

beforeEach(() => {
	calls = [];
	const server = new MockServer();
	vi.stubGlobal(
		"fetch",
		async (input: RequestInfo | URL, init?: RequestInit) => {
			const req = input instanceof Request ? input : new Request(input, init);
			const url = new URL(req.url);
			if (req.method !== "GET")
				calls.push({
					method: req.method,
					path: url.pathname,
					body: req.method === "PUT" ? await req.clone().json() : undefined,
				});
			if (req.method === "PUT" && url.pathname.endsWith("/FAIL_ME"))
				return Response.json(
					{ code: "internal", message: "写入失败" },
					{ status: 500 },
				);
			return (
				(await server.handleFetch(req)) ?? new Response(null, { status: 404 })
			);
		},
	);
});

afterEach(() => {
	vi.unstubAllGlobals();
});

function mount() {
	const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
	return render(
		<QueryClientProvider client={qc}>
			<SecretsTab project={ORYN} />
		</QueryClientProvider>,
	);
}

function row(name: string): HTMLElement {
	const el = document.querySelector<HTMLElement>(
		`[data-testid=project-secret-row][data-name="${name}"]`,
	);
	if (!el) throw new Error(`没有 ${name} 这一行`);
	return el;
}

describe("SecretsTab", () => {
	it("不保密的值可见，保密的只显示徽标", async () => {
		mount();
		await screen.findAllByTestId("project-secret-row");
		expect(within(row("NODE_ENV")).getByText("development")).toBeVisible();
		const dsn = row("SENTRY_DSN");
		expect(within(dsn).getByText("保密")).toBeInTheDocument();
		expect(within(dsn).getByText("已隐藏")).toBeInTheDocument();
		expect(
			within(row("git/gitlab.com/token")).getByText("gitlab.com"),
		).toBeInTheDocument();
		expect(
			within(row("git/gitlab.com/token")).getByText("Git 令牌"),
		).toBeInTheDocument();
	});

	it("新增不保密的环境变量", async () => {
		mount();
		await screen.findAllByTestId("project-secret-row");
		fireEvent.change(screen.getByLabelText(/^变量名/), {
			target: { value: "FOO" },
		});
		fireEvent.change(screen.getByLabelText(/^值/), {
			target: { value: "bar" },
		});
		fireEvent.click(screen.getByLabelText("保密"));
		fireEvent.click(screen.getByRole("button", { name: "保存" }));
		await waitFor(() =>
			expect(calls).toContainEqual({
				method: "PUT",
				path: `${BASE}/FOO`,
				body: { value: "bar", mode: "env", secret: false },
			}),
		);
		await waitFor(() =>
			expect(within(row("FOO")).getByText("bar")).toBeInTheDocument(),
		);
		expect(screen.getByLabelText(/^变量名/)).toHaveValue("");
	});

	it("非法变量名不能保存", async () => {
		mount();
		await screen.findAllByTestId("project-secret-row");
		fireEvent.change(screen.getByLabelText(/^变量名/), {
			target: { value: "foo" },
		});
		fireEvent.change(screen.getByLabelText(/^值/), {
			target: { value: "bar" },
		});
		expect(screen.getByRole("button", { name: "保存" })).toBeDisabled();
	});

	it("Git 令牌总是保密，名称拼成 git/<主机>/token", async () => {
		mount();
		await screen.findAllByTestId("project-secret-row");
		fireEvent.change(screen.getByLabelText("类型"), {
			target: { value: "git" },
		});
		const secret = screen.getByLabelText("保密");
		expect(secret).toBeChecked();
		expect(secret).toBeDisabled();
		expect(screen.getByText("Git 令牌总是保密保存")).toBeInTheDocument();
		fireEvent.change(screen.getByLabelText(/^主机/), {
			target: { value: " Git.Example.com " },
		});
		fireEvent.change(screen.getByLabelText(/^值/), {
			target: { value: "tok" },
		});
		fireEvent.click(screen.getByRole("button", { name: "保存" }));
		await waitFor(() =>
			expect(calls).toContainEqual({
				method: "PUT",
				path: `${BASE}/git%2Fgit.example.com%2Ftoken`,
				body: { value: "tok", mode: "proxy", secret: true },
			}),
		);
	});

	it("编辑保密条目要重新输入值", async () => {
		mount();
		await screen.findAllByTestId("project-secret-row");
		fireEvent.click(screen.getByRole("button", { name: "编辑 SENTRY_DSN" }));
		const dialog = await screen.findByRole("dialog");
		const value = within(dialog).getByLabelText(/^值/);
		expect(value).toHaveValue("");
		expect(value).toHaveAttribute("placeholder", "输入新值");
		expect(within(dialog).getByLabelText(/^变量名/)).toHaveAttribute(
			"readonly",
		);
		const save = within(dialog).getByRole("button", { name: "保存" });
		expect(save).toBeDisabled();
		fireEvent.change(value, { target: { value: "https://x" } });
		fireEvent.click(within(dialog).getByLabelText("保密"));
		fireEvent.click(save);
		await waitFor(() =>
			expect(calls).toContainEqual({
				method: "PUT",
				path: `${BASE}/SENTRY_DSN`,
				body: { value: "https://x", mode: "env", secret: false },
			}),
		);
		await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
	});

	it("删除先确认", async () => {
		mount();
		await screen.findAllByTestId("project-secret-row");
		fireEvent.click(screen.getByRole("button", { name: "删除 NODE_ENV" }));
		const req = await answerConfirm(true);
		expect(req.title).toBe("删除 NODE_ENV？");
		expect(req.danger).toBe(true);
		await waitFor(() =>
			expect(calls).toContainEqual({
				method: "DELETE",
				path: `${BASE}/NODE_ENV`,
				body: undefined,
			}),
		);
		await waitFor(() =>
			expect(document.querySelector('[data-name="NODE_ENV"]')).toBeNull(),
		);
	});

	it("批量添加：预览与问题、按顺序导入、失败的留在对话框里", async () => {
		mount();
		await screen.findAllByTestId("project-secret-row");
		fireEvent.click(screen.getByRole("button", { name: "批量添加" }));
		const dialog = await screen.findByRole("dialog");
		fireEvent.change(within(dialog).getByRole("textbox"), {
			target: { value: "A=1\nfoo=2\nFAIL_ME=3\nNODE_ENV=production" },
		});
		expect(within(dialog).getByText("将导入 3 项")).toBeInTheDocument();
		expect(within(dialog).getByText("第 2 行")).toBeInTheDocument();
		expect(within(dialog).getByText("覆盖现有")).toBeInTheDocument();
		// NODE_ENV 改成不保密
		const node = within(dialog)
			.getAllByTestId("bulk-entry")
			.find((el) => el.dataset.name === "NODE_ENV") as HTMLElement;
		fireEvent.click(within(node).getByLabelText("保密"));
		expect(within(node).getByText("production")).toBeInTheDocument();
		fireEvent.click(within(dialog).getByRole("button", { name: "导入 3 项" }));
		expect(
			await within(dialog).findByText("1 项没有导入，原因见下"),
		).toBeInTheDocument();
		expect(calls.map((c) => c.path)).toEqual([
			`${BASE}/A`,
			`${BASE}/FAIL_ME`,
			`${BASE}/NODE_ENV`,
		]);
		expect(calls[0]?.body).toEqual({ value: "1", mode: "env", secret: true });
		expect(calls[2]?.body).toEqual({
			value: "production",
			mode: "env",
			secret: false,
		});
		expect(
			within(dialog)
				.getAllByTestId("bulk-entry")
				.map((el) => el.dataset.name),
		).toEqual(["FAIL_ME"]);
		expect(within(dialog).getByText("写入失败")).toBeInTheDocument();
		await waitFor(() =>
			expect(within(row("A")).getByText("保密")).toBeInTheDocument(),
		);
	});

	it("没有条目时是空状态", async () => {
		const qc = new QueryClient({
			defaultOptions: { queries: { retry: false } },
		});
		render(
			<QueryClientProvider client={qc}>
				<SecretsTab project={projects[1] as Project} />
			</QueryClientProvider>,
		);
		expect(
			await screen.findByText("还没有项目级的变量或令牌"),
		).toBeInTheDocument();
	});
});
