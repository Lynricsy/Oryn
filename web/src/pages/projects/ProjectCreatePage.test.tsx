/** 新建项目页（M24）：精简表单，创建后进新项目，取消回项目总览；没有环境、Agent 配置与推送门禁。 */
import { fireEvent, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import i18n from "../../i18n";
import { kit, mount, resetKit, where, writesOnly } from "./projects-test-kit";

beforeEach(resetKit);

afterEach(() => {
	vi.unstubAllGlobals();
	vi.restoreAllMocks();
});

describe("ProjectCreatePage", () => {
	it("创建成功后跳到新项目：POST 只带填了的字段", async () => {
		mount("/projects/new");
		expect(screen.queryByText("环境配置")).toBeNull();
		expect(screen.queryByLabelText(/^setup/)).toBeNull();
		fireEvent.change(screen.getByLabelText("名称"), {
			target: { value: "fresh" },
		});
		fireEvent.click(screen.getByRole("button", { name: "创建" }));
		await waitFor(() => expect(where()).toHaveTextContent("/projects/prj_new"));
		expect(writesOnly()).toEqual([
			["POST", "/api/v1/projects", { name: "fresh", shallowClone: false }],
		]);
		expect(
			await screen.findByRole("heading", { level: 1, name: "fresh" }),
		).toBeInTheDocument();
	});

	it("填了描述与仓库：一起带上", async () => {
		mount("/projects/new");
		fireEvent.change(screen.getByLabelText("名称"), {
			target: { value: "w" },
		});
		fireEvent.change(screen.getByLabelText(/^描述/), {
			target: { value: "一行说明" },
		});
		fireEvent.change(screen.getByLabelText("仓库地址"), {
			target: { value: "https://github.com/a/w" },
		});
		fireEvent.click(screen.getByRole("button", { name: "创建" }));
		await waitFor(() => expect(kit.writes).toHaveLength(1));
		expect(kit.writes[0]?.body).toEqual({
			name: "w",
			shallowClone: false,
			description: "一行说明",
			repoUrl: "https://github.com/a/w",
		});
	});

	it("新建时取消：回项目总览", async () => {
		mount("/projects/new");
		fireEvent.click(screen.getByRole("button", { name: "取消" }));
		await waitFor(() => expect(where()).toHaveTextContent(/^\/projects$/));
		expect(kit.writes).toEqual([]);
	});

	it("新建项目页没有 Agent 配置卡片与推送门禁", () => {
		mount("/projects/new");
		expect(
			screen.queryByRole("heading", {
				name: i18n.t("projectsPage.agentConfig.title"),
			}),
		).toBeNull();
		expect(screen.queryByTestId("project-gates")).toBeNull();
	});
});
