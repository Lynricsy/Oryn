/**
 * 推送 / 开 PR 门禁卡（M15，M24 起在项目设置「GitHub」分组）：只对绑定了 GitHub 的项目出现；回填已有门禁；
 * 没改过时不能保存，改过才发 `githubGates`（命令去空白、空命令省略、标题去 `##` 与空行）。
 */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { Project } from "../../api/client";
import i18n from "../../i18n";
import { normalizeGates, ProjectGatesCard } from "./ProjectGates";

const T = (k: string) => i18n.t(k as "common.copy");

const PROJECT: Project = {
	id: "prj_1",
	name: "widgets",
	repoUrl: "https://github.com/acme/widgets",
	shallowClone: false,
	snapshots: true,
	warmOrbs: 0,
	createdAt: 1,
	updatedAt: 1,
	github: {
		repoId: 42,
		fullName: "acme/widgets",
		bound: true,
		listen: { reviews: true, comments: true, ci: true },
	},
};

let patches: Record<string, unknown>[];

function renderCard(project: Project) {
	patches = [];
	vi.stubGlobal(
		"fetch",
		vi.fn(async (input: Request) => {
			const body = (await input.json()) as Record<string, unknown>;
			patches.push(body);
			return Response.json({ ...project, ...body });
		}),
	);
	render(
		<QueryClientProvider
			client={
				new QueryClient({ defaultOptions: { queries: { retry: false } } })
			}
		>
			<ProjectGatesCard project={project} onCancel={() => undefined} />
		</QueryClientProvider>,
	);
}

const save = () => screen.getByRole("button", { name: T("common.save") });
const toggle = (k: string) => screen.getByRole("switch", { name: T(k) });
const area = (k: string) =>
	screen.getByRole("textbox", { name: (n) => n.startsWith(T(k)) });

afterEach(() => {
	vi.unstubAllGlobals();
});

describe("ProjectGates", () => {
	it("GitHub 项目：门禁区出现，默认全关；没改过时保存不可用", async () => {
		renderCard(PROJECT);
		expect(await screen.findByTestId("project-gates")).toBeInTheDocument();
		expect(toggle("github.gates.push")).not.toBeChecked();
		expect(toggle("github.gates.pr")).not.toBeChecked();
		expect(toggle("github.gates.requireClosingRef")).not.toBeChecked();
		expect(save()).toBeDisabled();
		fireEvent.click(toggle("github.gates.push"));
		expect(save()).toBeEnabled();
	});

	it("改门禁：PATCH 带整理后的 githubGates", async () => {
		renderCard(PROJECT);
		await screen.findByTestId("project-gates");
		fireEvent.click(toggle("github.gates.push"));
		fireEvent.click(toggle("github.gates.pr"));
		fireEvent.change(area("github.gates.checkCmd"), {
			target: { value: "  make check  " },
		});
		fireEvent.change(area("github.gates.testCmd"), {
			target: { value: "make test" },
		});
		fireEvent.change(area("github.gates.prHeadings"), {
			target: { value: "## Repro\n\nCause\n  Fix  " },
		});
		fireEvent.click(toggle("github.gates.requireClosingRef"));
		fireEvent.click(save());
		await waitFor(() => expect(patches).toHaveLength(1));
		expect(patches[0]?.githubGates).toEqual({
			push: true,
			pr: true,
			checkCmd: "make check",
			testCmd: "make test",
			prHeadings: ["Repro", "Cause", "Fix"],
			requireClosingRef: true,
		});
	});

	it("回填已有门禁；清空命令后保存时省略该命令", async () => {
		renderCard({
			...PROJECT,
			githubGates: {
				push: true,
				pr: false,
				fixCmd: "bun run format",
				checkCmd: "bun run check",
				prHeadings: ["Summary"],
				requireClosingRef: true,
			},
		});
		await screen.findByTestId("project-gates");
		await waitFor(() => expect(toggle("github.gates.push")).toBeChecked());
		expect(area("github.gates.fixCmd")).toHaveValue("bun run format");
		expect(area("github.gates.prHeadings")).toHaveValue("Summary");
		fireEvent.change(area("github.gates.fixCmd"), { target: { value: "" } });
		fireEvent.click(save());
		await waitFor(() => expect(patches).toHaveLength(1));
		expect(patches[0]?.githubGates).toEqual({
			push: true,
			pr: false,
			checkCmd: "bun run check",
			prHeadings: ["Summary"],
			requireClosingRef: true,
		});
	});

	it("非 GitHub 项目：没有门禁区", () => {
		const { github: _g, ...plain } = PROJECT;
		renderCard({ ...plain, repoUrl: "https://gitlab.com/acme/widgets" });
		expect(screen.queryByTestId("project-gates")).toBeNull();
	});
});

describe("normalizeGates", () => {
	it("命令去空白、空命令省略；标题去掉 ## 前缀与空行", () => {
		expect(
			normalizeGates({
				push: false,
				pr: true,
				fixCmd: "   ",
				testCmd: " t ",
				prHeadings: ["### Why", "", " ## How "],
			}),
		).toEqual({
			push: false,
			pr: true,
			testCmd: "t",
			prHeadings: ["Why", "How"],
			requireClosingRef: false,
		});
	});
});
