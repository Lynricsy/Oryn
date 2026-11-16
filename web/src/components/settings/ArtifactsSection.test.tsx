/** 设置 › Artifacts：已授权列表（项目名、能力、两步收回）与加载中、失败（重试）、空三种占位。 */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { ArtifactConsent } from "../../api/artifact-queries";
import i18n from "../../i18n";
import { ArtifactsSection } from "./ArtifactsSection";

const T = (k: string) => i18n.t(k as "common.copy");

const GRANT: ArtifactConsent = {
	artifactId: "art_01JCZ8M3W4X5Y6Z7A8B9C0D1E2",
	title: "仓库浏览",
	projectId: "prj_01JCZ8M3W4X5Y6Z7A8B9C0D1E2",
	capabilities: ["files", "sample"],
	grantedAt: Date.now() - 60_000,
};
const LOOSE: ArtifactConsent = {
	artifactId: "art_01JCZ8M3W4X5Y6Z7A8B9C0D1E3",
	title: "问答",
	capabilities: ["sample"],
	grantedAt: Date.now() - 120_000,
};

let deletes: string[];

/** `consents` 为数字时，列表接口以该状态码失败。 */
function renderWith(consents: ArtifactConsent[] | number) {
	deletes = [];
	let list = consents;
	vi.stubGlobal(
		"fetch",
		vi.fn(async (input: Request) => {
			const u = new URL(input.url);
			if (input.method === "DELETE") {
				deletes.push(u.pathname);
				if (Array.isArray(list))
					list = list.filter((c) => !u.pathname.endsWith(c.artifactId));
				return new Response(null, { status: 204 });
			}
			if (u.pathname === "/api/v1/projects")
				return Response.json({
					projects: [{ id: GRANT.projectId, name: "Oryn" }],
				});
			if (typeof list === "number")
				return Response.json(
					{ code: "internal", message: "坏了" },
					{ status: list },
				);
			return Response.json({ consents: list });
		}),
	);
	render(
		<QueryClientProvider
			client={
				new QueryClient({ defaultOptions: { queries: { retry: false } } })
			}
		>
			<MemoryRouter>
				<ArtifactsSection />
			</MemoryRouter>
		</QueryClientProvider>,
	);
	return {
		heal: (next: ArtifactConsent[]) => {
			list = next;
		},
	};
}

afterEach(() => {
	vi.unstubAllGlobals();
});

describe("ArtifactsSection", () => {
	it("没有授权：只显示空态", async () => {
		renderWith([]);
		expect(
			await screen.findByText(T("artifacts.settings.empty")),
		).toBeInTheDocument();
		expect(screen.queryByText(T("artifacts.settings.granted"))).toBeNull();
	});

	it("列出授权：标题链到查看器，项目名与能力，两步收回整个 artifact", async () => {
		renderWith([GRANT, LOOSE]);
		const link = await screen.findByRole("link", { name: "仓库浏览" });
		expect(link).toHaveAttribute("href", `/artifacts/${GRANT.artifactId}`);
		expect(await screen.findByText("Oryn")).toBeInTheDocument();
		expect(
			screen.getByText(T("artifacts.settings.noProject")),
		).toBeInTheDocument();
		expect(
			screen.getByText(T("artifacts.settings.caps.files")),
		).toBeInTheDocument();
		expect(
			screen.getAllByText(T("artifacts.settings.caps.sample")),
		).toHaveLength(2);

		const [first] = screen.getAllByRole("button", {
			name: T("artifacts.settings.revoke"),
		});
		fireEvent.click(first as HTMLElement);
		expect(deletes).toEqual([]);
		fireEvent.click(
			screen.getByRole("button", {
				name: T("artifacts.settings.revokeConfirm"),
			}),
		);
		await waitFor(() =>
			expect(deletes).toEqual([
				`/api/v1/artifact-consents/${GRANT.artifactId}`,
			]),
		);
		await waitFor(() =>
			expect(screen.queryByRole("link", { name: "仓库浏览" })).toBeNull(),
		);
	});

	it("加载失败：显示失败占位，重试后恢复", async () => {
		const r = renderWith(500);
		expect(
			await screen.findByText(T("artifacts.settings.failed")),
		).toBeInTheDocument();
		r.heal([LOOSE]);
		fireEvent.click(
			screen.getByRole("button", { name: T("artifacts.settings.retry") }),
		);
		expect(
			await screen.findByRole("link", { name: "问答" }),
		).toBeInTheDocument();
	});
});
