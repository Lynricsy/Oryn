import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { afterEach, describe, expect, it, vi } from "vitest";
import i18n from "../i18n";
import {
	fullPage,
	goAfterLogin,
	SetupPage,
	setupTokenFromHash,
} from "./AuthPages";

describe("初始化令牌走 URL 片段（L3）", () => {
	afterEach(() => window.history.replaceState(null, "", "/"));

	it("从 #token= 读取（URL 转义的值也能还原）", () => {
		expect(setupTokenFromHash("#token=ost_abc-_9")).toBe("ost_abc-_9");
		expect(setupTokenFromHash("#token=ost_a%2Bb")).toBe("ost_a+b");
		expect(setupTokenFromHash("")).toBeUndefined();
		expect(setupTokenFromHash("#other=1")).toBeUndefined();
	});

	it("SetupPage 读到令牌后从地址栏抹掉片段，查询串里的 token 不再被采用", () => {
		window.history.replaceState(null, "", "/setup?x=1#token=ost_secret");
		render(
			<QueryClientProvider client={new QueryClient()}>
				<MemoryRouter initialEntries={["/setup"]}>
					<SetupPage />
				</MemoryRouter>
			</QueryClientProvider>,
		);
		expect(
			screen.getByRole("heading", { name: i18n.t("auth.setupTitle") }),
		).toBeInTheDocument();
		expect(window.location.hash).toBe("");
		expect(window.location.search).toBe("?x=1");
	});
});

describe("登录后的去处（M4 4.3）", () => {
	afterEach(() => vi.restoreAllMocks());

	it("next 是 portal 授权端点：整页跳转到服务端（location.replace），不走路由", () => {
		const replace = vi.spyOn(fullPage, "replace").mockImplementation(() => {});
		const navigate = vi.fn();
		const next = "/portal/auth?t=3f9a1c0b7e2d&p=5173&next=%2Fa%3Fb%3D1";
		goAfterLogin(next, navigate);
		expect(replace).toHaveBeenCalledWith(next);
		expect(navigate).not.toHaveBeenCalled();
	});

	it("站内路径走路由内跳转，非站内地址回首页", () => {
		const replace = vi.spyOn(fullPage, "replace").mockImplementation(() => {});
		const navigate = vi.fn();
		goAfterLogin("/t/T-1", navigate);
		goAfterLogin("https://evil.example/portal/auth?t=1", navigate);
		// 只认 `/portal/auth?` 前缀：别的 portal 开头的站内路径照常走路由
		goAfterLogin("/portal/authx", navigate);
		expect(navigate.mock.calls).toEqual([
			["/t/T-1", { replace: true }],
			["/", { replace: true }],
			["/portal/authx", { replace: true }],
		]);
		expect(replace).not.toHaveBeenCalled();
	});
});
