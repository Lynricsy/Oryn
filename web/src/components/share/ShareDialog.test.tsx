/**
 * 分享对话框：无命中直接创建（新链接置顶并复制）、有命中 → 勾选确认 → 带 `acknowledgeFindings` 重发、撤销两步确认。
 */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import {
	afterEach,
	beforeEach,
	describe,
	expect,
	it,
	type Mock,
	vi,
} from "vitest";
import type { ShareScan, ThreadShare } from "../../api/share-queries";
import i18n from "../../i18n";
import { ShareDialog } from "./ShareDialog";

const TID = "T-0193a1b2-0000-7000-8000-0000000000d1";
const T = (k: string, o: Record<string, unknown> = {}) =>
	i18n.t(k as "common.copy", o);

function share(id: string, mode: ThreadShare["mode"]): ThreadShare {
	const slug = id.padEnd(32, "0");
	return {
		id,
		threadId: TID,
		threadTitle: "t",
		mode,
		slug,
		url: `https://oryn.example/s/${slug}`,
		createdAt: 1_000,
		findings: 0,
	};
}

const SCAN: ShareScan = {
	findings: [
		{
			rule: "api_key_sk",
			seq: 3,
			entryId: "e3",
			role: "toolResult",
			tool: "bash",
			preview: "sk-t…23",
			count: 2,
		},
	],
	scanned: 4,
	truncated: false,
};

let posts: Record<string, unknown>[];
let deletes: string[];
let writeText: Mock<(text: string) => Promise<void>>;

function setup({ findings }: { findings: boolean }) {
	posts = [];
	deletes = [];
	const existing = [share("aaaa", "live")];
	vi.stubGlobal(
		"fetch",
		vi.fn(async (input: Request) => {
			const u = new URL(input.url);
			if (
				u.pathname === `/api/v1/threads/${TID}/shares` &&
				input.method === "GET"
			)
				return Response.json({ shares: existing });
			if (u.pathname === `/api/v1/threads/${TID}/shares`) {
				const body = (await input.json()) as Record<string, unknown>;
				posts.push(body);
				if (findings && body.acknowledgeFindings !== true)
					return Response.json(
						{ code: "secrets_found", message: "x", scan: SCAN },
						{ status: 409 },
					);
				const created = share("bbbb", "snapshot");
				existing.unshift(created);
				return Response.json(created, { status: 201 });
			}
			if (
				u.pathname.startsWith("/api/v1/shares/") &&
				input.method === "DELETE"
			) {
				deletes.push(u.pathname);
				return new Response(null, { status: 204 });
			}
			return Response.json({ code: "not_found" }, { status: 404 });
		}),
	);
	render(
		<QueryClientProvider
			client={
				new QueryClient({ defaultOptions: { queries: { retry: false } } })
			}
		>
			<ShareDialog threadId={TID} onClose={() => {}} />
		</QueryClientProvider>,
	);
}

const createButton = () =>
	screen.getByRole("button", { name: T("share.dialog.create") });

beforeEach(() => {
	writeText = vi.fn(async (_text: string) => {});
	Object.defineProperty(navigator, "clipboard", {
		value: { writeText },
		configurable: true,
	});
});
afterEach(() => {
	vi.unstubAllGlobals();
});

describe("ShareDialog", () => {
	it("无命中：直接创建，新链接置顶并自动复制", async () => {
		setup({ findings: false });
		expect(
			await screen.findByTitle(share("aaaa", "live").url),
		).toBeInTheDocument();
		fireEvent.click(createButton());
		await waitFor(() => expect(posts).toHaveLength(1));
		expect(posts[0]).toEqual({ mode: "snapshot", acknowledgeFindings: false });
		const created = share("bbbb", "snapshot").url;
		await waitFor(() => expect(writeText).toHaveBeenCalledWith(created));
		const rows = screen
			.getAllByRole("listitem")
			.filter((li) => li.querySelector("[title^='https://']"));
		expect(rows[0]?.querySelector(`[title='${created}']`)).not.toBeNull();
		expect(
			screen.getByText(T("share.dialog.createdCopied")),
		).toBeInTheDocument();
	});

	it("选实时模式时按 live 创建", async () => {
		setup({ findings: false });
		fireEvent.click(screen.getByRole("radio", { checked: false }));
		fireEvent.click(createButton());
		await waitFor(() => expect(posts).toHaveLength(1));
		expect(posts[0]).toMatchObject({ mode: "live" });
	});

	it("有命中：列出命中项，勾选确认后带 acknowledgeFindings 重发", async () => {
		setup({ findings: true });
		fireEvent.click(createButton());
		expect(
			await screen.findByText(T("share.findings.title", { n: 2 })),
		).toBeInTheDocument();
		expect(screen.getByText(T("share.rule.api_key_sk"))).toBeInTheDocument();
		expect(screen.getByText("sk-t…23")).toBeInTheDocument();
		expect(screen.getByText("bash")).toBeInTheDocument();
		const anyway = screen.getByRole("button", {
			name: T("share.findings.createAnyway"),
		});
		expect(anyway).toBeDisabled();
		fireEvent.click(screen.getByRole("checkbox"));
		expect(anyway).toBeEnabled();
		fireEvent.click(anyway);
		await waitFor(() => expect(posts).toHaveLength(2));
		expect(posts[1]).toEqual({ mode: "snapshot", acknowledgeFindings: true });
		await waitFor(() =>
			expect(screen.queryByTestId("share-findings")).toBeNull(),
		);
		expect(writeText).toHaveBeenCalledWith(share("bbbb", "snapshot").url);
	});

	it("撤销要点两次", async () => {
		setup({ findings: false });
		const revoke = await screen.findByRole("button", {
			name: T("share.dialog.revoke"),
		});
		fireEvent.click(revoke);
		expect(deletes).toEqual([]);
		fireEvent.click(
			screen.getByRole("button", { name: T("share.dialog.revokeConfirm") }),
		);
		await waitFor(() => expect(deletes).toEqual(["/api/v1/shares/aaaa"]));
	});
});
