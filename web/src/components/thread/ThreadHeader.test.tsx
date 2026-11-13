/**
 * 页头标题前的图标：窄页头（手机）下项目段整段隐藏，标题前仍要有图标——
 * 有标题卡图标用它，没有时退到项目徽标，两者都没有就不放。
 * jsdom 不算容器查询，这里只核对渲染了哪种图标、是否带窄屏才显示的类。
 */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { beforeEach, describe, expect, it } from "vitest";
import type { Orb, Thread } from "../../api/client";
import { qk } from "../../api/queries";
import i18n from "../../i18n";
import { getThreadStore } from "../../state/stores";
import { uiStore } from "../../state/ui";
import { ThreadHeader } from "./ThreadHeader";

const PROJECT = { id: "P-hdr", name: "oryn", icon: "" };

function renderHeader(id: string, thread: Partial<Thread>) {
	getThreadStore(id)
		.getState()
		.patchLocal({ thread: { id, ...thread } as Thread });
	const client = new QueryClient({
		defaultOptions: { queries: { retry: false, staleTime: Infinity } },
	});
	client.setQueryData(qk.projects, { projects: [PROJECT] });
	render(
		<QueryClientProvider client={client}>
			<MemoryRouter>
				<ThreadHeader threadId={id} sidebarToggle />
			</MemoryRouter>
		</QueryClientProvider>,
	);
	return screen.getByRole("button", { name: /重连|无图标|补全/ });
}

beforeEach(() => {
	i18n.changeLanguage("zh-CN");
});

describe("ThreadHeader 标题图标", () => {
	it("有标题卡图标时，标题前渲染它（不受窄页头隐藏）", () => {
		const title = renderHeader("T-hdr1", {
			title: "🐳 DOCKER: 重连失败",
			projectId: PROJECT.id,
		});
		const glyph = title.querySelector("[data-title-glyph]");
		expect(glyph).toHaveAttribute("data-title-glyph", "card");
		expect(glyph).toHaveAttribute("aria-hidden", "true");
		expect(glyph).toHaveTextContent("🐳");
		expect(glyph?.className).not.toMatch(/(^|\s)hidden(\s|$)/);
		// 图标不进无障碍名，标题按钮名只是标题文字
		expect(title).toHaveAccessibleName("重连失败");
	});

	it("没有标题卡图标时，窄页头在标题前退到项目徽标", () => {
		const title = renderHeader("T-hdr2", {
			title: "补全 CLI 脚本",
			projectId: PROJECT.id,
		});
		const glyph = title.querySelector("[data-title-glyph]");
		expect(glyph).toHaveAttribute("data-title-glyph", "project");
		// 宽页头已有项目段，这里只在窄页头显示
		expect(glyph?.className).toContain("@md/header:hidden");
		expect(glyph).toHaveTextContent("O");
	});

	it("既无标题卡图标也无项目时不放图标", () => {
		const title = renderHeader("T-hdr3", { title: "无图标的线程" });
		expect(title.querySelector("[data-title-glyph]")).toBeNull();
	});
});

describe("ThreadHeader orb 状态胶囊", () => {
	const FAILED_SETUP = { ok: false, phase: "setup" };

	function renderWithOrb(id: string, orb: Partial<Orb>) {
		getThreadStore(id)
			.getState()
			.patchLocal({
				orb: {
					id: `orb-${id}`,
					stateChangedAt: "2026-01-01T00:00:00Z",
					...orb,
				} as Orb,
			});
		renderHeader(id, { title: "无图标的线程", orbId: `orb-${id}` });
		return screen.getByRole("button", { name: /^状态：/ });
	}

	it("orb 就绪而最近 setup 失败时，胶囊报环境失败", () => {
		const chip = renderWithOrb("T-orb1", {
			state: "ready",
			lastSetup: FAILED_SETUP as Orb["lastSetup"],
		});
		expect(chip).toHaveAccessibleName("状态：环境 setup 失败");
	});

	it.each([
		["frozen", "已冻结"],
		["stopped", "已停机"],
		["waking", "唤醒中"],
		["resuming", "恢复中"],
		["failed", "失败"],
	] as const)("orb %s 时历史 setup 失败不覆盖生命周期状态", (state, label) => {
		const chip = renderWithOrb(`T-orb-${state}`, {
			state,
			lastSetup: FAILED_SETUP as Orb["lastSetup"],
		});
		expect(chip).toHaveAccessibleName(`状态：${label}`);
	});
});

describe("ThreadHeader 公开徽标", () => {
	it("有未撤销的公开链接时显示「已公开」，点击打开分享对话框", () => {
		uiStore.getState().close();
		renderHeader("T-hdr-pub", { title: "重连", publicShares: 2 });
		const badge = screen.getByRole("button", {
			name: i18n.t("share.header.badge"),
		});
		expect(badge).toHaveAttribute(
			"title",
			i18n.t("share.header.badgeTitle", { n: 2 }),
		);
		fireEvent.click(badge);
		expect(uiStore.getState().dialog).toEqual({ kind: "share" });
	});

	it("没有公开链接时不显示", () => {
		renderHeader("T-hdr-nopub", { title: "重连" });
		expect(
			screen.queryByRole("button", { name: i18n.t("share.header.badge") }),
		).toBeNull();
	});
});
