/**
 * 设置页「SSH 主机」（M18），后端用 mock 的 SSH 实现（校验与错误码同 orynd）：
 * 新建主机后出现在列表并自动显示测试结果；重名的 409 文案显示在对话框里；删除被当跳板的主机时 409 显示在行下；
 * 生成钥匙后显示公钥；钥匙被引用时拒删的原因显示在行下。
 */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
	fireEvent,
	render,
	screen,
	waitFor,
	within,
} from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import i18n from "../../../i18n";
import { MockSsh } from "../../../mock/ssh";
import { answerConfirm } from "../../../test/confirm";
import SshPage from "./SshPage";

let ssh: MockSsh;

beforeEach(() => {
	i18n.changeLanguage("zh-CN");
	ssh = new MockSsh({ changed: () => {}, threads: new Map(), orbs: new Map() });
	vi.stubGlobal(
		"fetch",
		vi.fn(async (input: Request) => {
			const u = new URL(input.url);
			const p = u.pathname.slice("/api/v1/".length);
			if (p === "projects") return Response.json({ projects: [] });
			return (
				(await ssh.handle(input, p, input.method.toUpperCase(), u)) ??
				new Response(null, { status: 404 })
			);
		}),
	);
	render(
		<QueryClientProvider
			client={
				new QueryClient({ defaultOptions: { queries: { retry: false } } })
			}
		>
			<MemoryRouter>
				<SshPage />
			</MemoryRouter>
		</QueryClientProvider>,
	);
});

afterEach(() => vi.unstubAllGlobals());

const dialog = () => within(screen.getByRole("dialog"));

/** Field 的 label 里还有提示文字：按开头匹配。 */
function fill(label: string, value: string) {
	fireEvent.change(dialog().getByLabelText(new RegExp(`^${label}`)), {
		target: { value },
	});
}

/** 主机对话框里的钥匙下拉（自绘 listbox）：打开后点选项。 */
function pickKey(name: string) {
	fireEvent.click(dialog().getByRole("combobox", { name: /^钥匙/ }));
	fireEvent.click(screen.getByRole("option", { name }));
}

/** 主机列表（审计的主机筛选里也有主机名，查询限定在列表内）；等行加载出来再取。 */
const hostList = async () => {
	await screen.findAllByTestId("ssh-host-row");
	return within(screen.getByTestId("ssh-hosts"));
};

describe("SshPage", () => {
	it("列出全局主机（项目主机不在此）、跳板名与标签", async () => {
		const list = await hostList();
		const row = list.getByText("prod-web").closest("li");
		expect(row).toHaveTextContent("deploy@10.0.1.21:22");
		expect(row).toHaveTextContent("经 bastion");
		expect(row).toHaveTextContent("端口转发");
		expect(list.queryByText("staging-db")).toBeNull();
	});

	it("新建主机后出现在列表，并自动跑一次测试显示结果", async () => {
		await hostList();
		fireEvent.click(screen.getByRole("button", { name: "添加主机" }));
		fill("名称", "web2");
		fill("登录用户", "ops");
		fill("地址", "10.0.1.22");
		pickKey("deploy");
		fireEvent.click(dialog().getByRole("button", { name: "添加" }));
		const row = (await (await hostList()).findByText("web2")).closest("li");
		expect(row).toHaveTextContent("ops@10.0.1.22:22");
		await waitFor(() =>
			expect(
				within(row as HTMLElement).getByTestId("ssh-test-result"),
			).toHaveTextContent("连接成功"),
		);
		expect(screen.queryByRole("dialog")).toBeNull();
	});

	it("重名：服务端 409 的文案显示在对话框里", async () => {
		await hostList();
		fireEvent.click(screen.getByRole("button", { name: "添加主机" }));
		fill("名称", "bastion");
		fill("登录用户", "ops");
		fill("地址", "10.0.1.30");
		pickKey("deploy");
		fireEvent.click(dialog().getByRole("button", { name: "添加" }));
		expect(await dialog().findByRole("alert")).toHaveTextContent(
			"同名主机已存在",
		);
	});

	it("删除被当跳板的主机：确认后 409 文案显示在该行下", async () => {
		const list = await hostList();
		fireEvent.click(screen.getByRole("button", { name: "bastion 的操作" }));
		fireEvent.click(screen.getByRole("menuitem", { name: "删除" }));
		expect((await answerConfirm(true)).title).toBe("删除主机「bastion」？");
		const row = list.getByText("bastion").closest("li") as HTMLElement;
		expect(await within(row).findByRole("alert")).toHaveTextContent(
			"主机被其他主机用作跳板",
		);
	});

	it("生成钥匙后显示公钥；钥匙被主机引用时拒删", async () => {
		const keys = await screen.findByTestId("ssh-keys");
		expect(
			(await within(keys).findByText("deploy")).closest("li"),
		).toHaveTextContent("2 台主机在用");
		fireEvent.click(screen.getByRole("button", { name: "生成钥匙" }));
		fill("名称", "ci");
		fireEvent.click(dialog().getByRole("button", { name: "生成钥匙" }));
		const created = await dialog().findByTestId("ssh-key-created");
		expect(created).toHaveTextContent(/^.*ssh-ed25519 \S+ ci/);
		fireEvent.click(dialog().getByRole("button", { name: "完成" }));
		await within(keys).findByText("ci");

		fireEvent.click(screen.getByRole("button", { name: "deploy 的操作" }));
		fireEvent.click(screen.getByRole("menuitem", { name: "删除" }));
		await answerConfirm(true);
		const row = within(keys).getByText("deploy").closest("li") as HTMLElement;
		expect(await within(row).findByRole("alert")).toHaveTextContent(
			"钥匙正被 2 台主机使用",
		);
	});
});
