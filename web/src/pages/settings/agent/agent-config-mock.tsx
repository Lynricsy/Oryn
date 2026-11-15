/**
 * Agent 设置子页测试共用：内存里的 omp-home（`/api/v1/agent-config/files…` 的 GET / PUT / DELETE）与渲染包装。
 * 各测试文件仍需自己 `vi.mock` CodeEditor（mock 按文件提升）。
 */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render } from "@testing-library/react";
import type { ReactNode } from "react";
import { MemoryRouter } from "react-router";
import { vi } from "vitest";
import { readConfigDoc } from "../../../lib/omp-config";

export const home = {
	files: {} as Record<string, string>,
	puts: [] as { path: string; body: string }[],
	deletes: [] as string[],
};

export function resetHome(files: Record<string, string>): void {
	home.files = { ...files };
	home.puts = [];
	home.deletes = [];
}

const PREFIX = "/api/v1/agent-config/files/";

function meta(path: string, body: string) {
	return {
		path,
		size: body.length,
		mode: 420,
		sha256: "0".repeat(64),
		updatedAt: 1,
	};
}

export const agentFetch = vi.fn(
	async (input: RequestInfo | URL, init?: RequestInit) => {
		// rawFetch 传相对路径与 init；Node 的 Request 只收绝对地址
		const req =
			input instanceof Request
				? input
				: new Request(new URL(String(input), "http://x"), init);
		const url = new URL(req.url, "http://x");
		if (url.pathname === "/api/v1/agent-config/files")
			return Response.json({
				files: Object.entries(home.files).map(([p, c]) => meta(p, c)),
			});
		if (!url.pathname.startsWith(PREFIX))
			return Response.json({ code: "not_found", message: "" }, { status: 404 });
		const path = decodeURIComponent(url.pathname.slice(PREFIX.length));
		if (req.method === "PUT") {
			const body = await req.text();
			home.puts.push({ path, body });
			home.files[path] = body;
			return Response.json({
				file: meta(path, body),
				extractedSecrets: [],
				warnings: [],
			});
		}
		if (req.method === "DELETE") {
			home.deletes.push(path);
			delete home.files[path];
			return new Response(null, { status: 204 });
		}
		const c = home.files[path];
		return c === undefined
			? Response.json({ code: "not_found", message: path }, { status: 404 })
			: new Response(c);
	},
);

export function renderPage(ui: ReactNode) {
	const client = new QueryClient({
		defaultOptions: { queries: { retry: false } },
	});
	return render(
		<QueryClientProvider client={client}>
			<MemoryRouter>{ui}</MemoryRouter>
		</QueryClientProvider>,
	);
}

/** 最后一次写入 config.yml 的 `disabledExtensions`。 */
export function lastDisabled(): unknown {
	const put = home.puts.filter((p) => p.path === "config.yml").at(-1);
	return put ? readConfigDoc(put.body).toJS()?.disabledExtensions : undefined;
}
