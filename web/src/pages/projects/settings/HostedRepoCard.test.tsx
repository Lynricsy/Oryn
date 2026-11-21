/**
 * 设置「概览」的「托管仓库」卡（M24 阶段 ③）：托管项目显示克隆地址与推送后 Webhook；没有仓库的项目可以创建托管仓库
 * （确认后 POST `repo/hosted`，已归档时禁用）；外部仓库项目不渲染（那里是「仓库镜像」卡）。fetch 桩见 projects-test-kit。
 */
import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Project } from "../../../api/client";
import { answerConfirm } from "../../../test/confirm";
import {
	CORE,
	kit,
	LEGACY,
	mount,
	ORYN,
	resetKit,
	writesOnly,
} from "../projects-test-kit";

const HOOK_URL = "https://ci.example.com/hooks/oryn";

const HOSTED: Project = {
	...CORE,
	name: "notes",
	hosted: true,
	cloneUrl: `https://oryn.test/git/${CORE.id}.git`,
	baseBranch: "main",
	webhook: {
		url: HOOK_URL,
		hasSecret: true,
		lastDeliveryAt: Date.now() - 25 * 60_000,
		lastStatus: 200,
	},
};

const overview = (id: string) => `/projects/${id}/settings/overview`;

let width: number;
beforeEach(() => {
	resetKit();
	width = window.innerWidth;
	window.innerWidth = 1280;
});

afterEach(() => {
	window.innerWidth = width;
	vi.unstubAllGlobals();
	vi.restoreAllMocks();
});

describe("托管仓库卡", () => {
	it("托管项目：只读的克隆地址、复制按钮与 oryn clone 提示；没有仓库镜像卡", async () => {
		kit.projects = [ORYN, HOSTED];
		mount(overview(CORE.id));
		const card = await screen.findByTestId("hosted-repo-card");
		const input = within(card).getByLabelText("克隆地址");
		expect(input).toHaveValue(HOSTED.cloneUrl);
		expect(input).toHaveAttribute("readonly");
		expect(
			within(card).getByRole("button", { name: "复制克隆地址" }),
		).toBeInTheDocument();
		expect(card).toHaveTextContent("oryn clone notes");
		expect(screen.queryByTestId("project-repo-mirror")).toBeNull();
		// 托管项目的基本信息里没有仓库地址
		expect(screen.queryByLabelText("仓库地址")).toBeNull();
	});

	it("最近一次投递成功：写时间与状态码；已有密钥时密钥框提示留空不改", async () => {
		kit.projects = [HOSTED];
		mount(overview(CORE.id));
		const line = await screen.findByTestId("hosted-webhook-delivery");
		expect(line).toHaveTextContent(/^最近一次投递：25分钟前 · HTTP 200$/);
		expect(
			within(screen.getByTestId("hosted-webhook")).getByLabelText(/^签名密钥/),
		).toHaveAttribute("placeholder", "已设置；留空则不改");
	});

	it("投递失败：写原因", async () => {
		kit.projects = [
			{
				...HOSTED,
				webhook: {
					url: HOOK_URL,
					hasSecret: false,
					lastDeliveryAt: Date.now() - 60_000,
					lastStatus: 500,
					lastError: "HTTP 500",
				},
			},
		];
		mount(overview(CORE.id));
		expect(
			await screen.findByTestId("hosted-webhook-delivery"),
		).toHaveTextContent("最近一次投递失败：1分钟前 · HTTP 500");
	});

	it("没有 Webhook：没有投递行与移除按钮；保存要先填地址", async () => {
		kit.projects = [{ ...HOSTED, webhook: undefined }];
		mount(overview(CORE.id));
		const sec = await screen.findByTestId("hosted-webhook");
		expect(screen.queryByTestId("hosted-webhook-delivery")).toBeNull();
		expect(
			within(sec).queryByRole("button", { name: "移除 Webhook" }),
		).toBeNull();
		const save = within(sec).getByRole("button", { name: "保存 Webhook" });
		expect(save).toBeDisabled();
		fireEvent.change(within(sec).getByLabelText("地址"), {
			target: { value: HOOK_URL },
		});
		fireEvent.change(within(sec).getByLabelText(/^签名密钥/), {
			target: { value: "s3cret" },
		});
		fireEvent.click(save);
		await waitFor(() =>
			expect(writesOnly()).toEqual([
				[
					"PUT",
					`/api/v1/projects/${CORE.id}/webhook`,
					{ url: HOOK_URL, secret: "s3cret" },
				],
			]),
		);
	});

	it("只改地址：PUT 不带 secret", async () => {
		kit.projects = [HOSTED];
		mount(overview(CORE.id));
		const sec = await screen.findByTestId("hosted-webhook");
		fireEvent.change(within(sec).getByLabelText("地址"), {
			target: { value: "https://ci.example.com/hooks/v2" },
		});
		fireEvent.click(within(sec).getByRole("button", { name: "保存 Webhook" }));
		await waitFor(() =>
			expect(writesOnly()).toEqual([
				[
					"PUT",
					`/api/v1/projects/${CORE.id}/webhook`,
					{ url: "https://ci.example.com/hooks/v2" },
				],
			]),
		);
	});

	it("清除密钥再保存：secret 为空串", async () => {
		kit.projects = [HOSTED];
		mount(overview(CORE.id));
		const sec = await screen.findByTestId("hosted-webhook");
		fireEvent.click(within(sec).getByRole("button", { name: "清除密钥" }));
		fireEvent.click(within(sec).getByRole("button", { name: "保存 Webhook" }));
		await waitFor(() =>
			expect(writesOnly()).toEqual([
				[
					"PUT",
					`/api/v1/projects/${CORE.id}/webhook`,
					{ url: HOOK_URL, secret: "" },
				],
			]),
		);
		// 保存后服务端没有密钥了：不再有「清除密钥」
		await waitFor(() =>
			expect(
				within(screen.getByTestId("hosted-webhook")).queryByRole("button", {
					name: "清除密钥",
				}),
			).toBeNull(),
		);
	});

	it("移除 Webhook 要确认；取消不发请求，确认后 DELETE", async () => {
		kit.projects = [HOSTED];
		mount(overview(CORE.id));
		const sec = await screen.findByTestId("hosted-webhook");
		const remove = within(sec).getByRole("button", { name: "移除 Webhook" });
		fireEvent.click(remove);
		const req = await answerConfirm(false);
		expect(req.title).toBe("移除 notes 的推送 Webhook？");
		expect(req.danger).toBe(true);
		expect(writesOnly()).toEqual([]);
		fireEvent.click(remove);
		await answerConfirm(true);
		await waitFor(() =>
			expect(writesOnly()).toEqual([
				["DELETE", `/api/v1/projects/${CORE.id}/webhook`, undefined],
			]),
		);
		await waitFor(() =>
			expect(screen.queryByTestId("hosted-webhook-delivery")).toBeNull(),
		);
	});

	it("没有仓库的项目：确认后创建托管仓库", async () => {
		mount(overview(CORE.id));
		const card = await screen.findByTestId("hosted-repo-card");
		fireEvent.click(within(card).getByRole("button", { name: "创建托管仓库" }));
		const req = await answerConfirm(true);
		expect(req.title).toBe("为 core 创建托管仓库？");
		expect(req.body).toContain("main");
		await waitFor(() =>
			expect(writesOnly()).toEqual([
				["POST", `/api/v1/projects/${CORE.id}/repo/hosted`, undefined],
			]),
		);
		expect(
			await within(
				await screen.findByTestId("hosted-repo-card"),
			).findByLabelText("克隆地址"),
		).toHaveValue(`https://oryn.test/git/${CORE.id}.git`);
	});

	it("取消确认：不发请求", async () => {
		mount(overview(CORE.id));
		fireEvent.click(
			await screen.findByRole("button", { name: "创建托管仓库" }),
		);
		await answerConfirm(false);
		expect(writesOnly()).toEqual([]);
	});

	it("已归档且没有仓库：按钮禁用并说明", async () => {
		kit.archived = [LEGACY];
		mount(overview(LEGACY.id));
		const card = await screen.findByTestId("hosted-repo-card");
		expect(
			within(card).getByRole("button", { name: "创建托管仓库" }),
		).toBeDisabled();
		expect(card).toHaveTextContent("先取消归档");
	});

	it("外部仓库项目：不渲染", async () => {
		mount(overview(ORYN.id));
		await screen.findByTestId("project-repo-mirror");
		expect(screen.queryByTestId("hosted-repo-card")).toBeNull();
	});
});
