import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { afterEach, describe, expect, it } from "vitest";
import i18n from "../i18n";
import { SetupPage, setupTokenFromHash } from "./AuthPages";

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
