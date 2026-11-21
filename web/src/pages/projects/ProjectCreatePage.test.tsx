/** 新建项目页（M24）：保存后进新项目，取消回项目总览；只有设置表单（没有 Agent 配置与推送门禁）。 */
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
	it("新建成功后跳到新项目；只有设置卡", async () => {
		mount("/projects/new");
		expect(screen.queryByText("环境配置")).toBeNull();
		fireEvent.change(screen.getByLabelText("名称"), {
			target: { value: "fresh" },
		});
		fireEvent.click(screen.getByRole("button", { name: "保存" }));
		await waitFor(() => expect(where()).toHaveTextContent("/projects/prj_new"));
		expect(writesOnly()).toEqual([
			[
				"POST",
				"/api/v1/projects",
				{ name: "fresh", shallowClone: false, snapshots: true, warmOrbs: 0 },
			],
		]);
		expect(
			await screen.findByRole("heading", { level: 1, name: "fresh" }),
		).toBeInTheDocument();
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
