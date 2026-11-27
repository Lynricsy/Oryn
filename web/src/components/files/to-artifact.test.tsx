import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { renderHook } from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import i18n from "../../i18n";
import { MAX_OFFICE_FILE_BYTES } from "../../lib/artifacts/office/formats";
import { formatBytes } from "../../lib/format";
import { useFileToArtifact } from "./to-artifact";

function hook() {
	const qc = new QueryClient();
	const wrapper = ({ children }: { children: ReactNode }) => (
		<QueryClientProvider client={qc}>{children}</QueryClientProvider>
	);
	return renderHook(() => useFileToArtifact(), { wrapper }).result.current;
}

afterEach(() => {
	vi.unstubAllGlobals();
});

describe("转为 artifact", () => {
	it("超过 256 MiB 不下载，直接报错", async () => {
		const fetch = vi.fn();
		vi.stubGlobal("fetch", fetch);
		await expect(
			hook()({
				url: "/f/big.pptx",
				name: "big.pptx",
				size: MAX_OFFICE_FILE_BYTES + 1,
			}),
		).rejects.toThrow(
			i18n.t("files.toArtifactTooLarge", {
				max: formatBytes(MAX_OFFICE_FILE_BYTES),
			}),
		);
		expect(fetch).not.toHaveBeenCalled();
	});

	it("下载后以原文件名为字段名导入，带上线程与项目", async () => {
		const seen: { url: URL; body?: string }[] = [];
		vi.stubGlobal(
			"fetch",
			vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
				const req =
					input instanceof Request
						? input
						: new Request(new URL(String(input), location.origin), init);
				const url = new URL(req.url);
				if (url.pathname === "/f/report.xlsx") {
					seen.push({ url });
					return new Response("PK");
				}
				seen.push({ url, body: await req.text() });
				return Response.json({ id: "art_X", title: "report" }, { status: 201 });
			}),
		);
		const a = await hook()({
			url: "/f/report.xlsx",
			name: "report.xlsx",
			size: 2,
			threadId: "T-1",
			projectId: "P-1",
		});
		expect(a.id).toBe("art_X");
		const imp = seen[1];
		expect(imp?.url.pathname).toBe("/api/v1/artifacts/import");
		expect(imp?.url.searchParams.get("threadId")).toBe("T-1");
		expect(imp?.url.searchParams.get("projectId")).toBe("P-1");
		// jsdom 与 Node 的 Blob 不同源，序列化时丢掉文件名与内容；这里只核对字段名
		expect(imp?.body).toContain('form-data; name="report.xlsx"');
	});
});
