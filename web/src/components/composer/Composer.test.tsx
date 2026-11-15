/**
 * 输入区回归：等待应答期间的新草稿不被清空（W4）、同一草稿不重复发送、斜杠模式命令对账（W12）、
 * 切换线程时草稿各自保留、不串（W5）；跨 compact 断点重新挂载时草稿、错误行、光标保留（M12.1 问题 4）；
 * 触屏上工具行有拍照键，拍到的照片作为附件上传到本线程。
 */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, fireEvent, render, screen } from "@testing-library/react";
import type { ReactNode } from "react";
import { MemoryRouter, Route, Routes, useNavigate } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import i18n from "../../i18n";
import type * as AttachmentsLib from "../../lib/attachments";
import type * as ImagesLib from "../../lib/images";
import { ThreadPage } from "../../pages/ThreadPage";
import { resetComposerDrafts } from "../../state/composer-drafts";
import { getThreadStore, resetThreadStores } from "../../state/stores";
import { Composer } from "./Composer";

const { cmd, upload } = vi.hoisted(() => ({ cmd: vi.fn(), upload: vi.fn() }));
vi.mock("../../lib/attachments", async (orig) => ({
	...(await orig<typeof AttachmentsLib>()),
	uploadAttachment: upload,
}));
vi.mock("../../lib/images", async (orig) => ({
	...(await orig<typeof ImagesLib>()),
	// 不解码：图片只作为文件附件
	prepareImage: () => Promise.reject(new Error("no decode")),
}));
vi.mock("../../realtime/socket", () => ({
	realtime: {
		cmd: (...args: unknown[]) => cmd(...args),
		uiResponse: () => true,
		subscribe: () => () => undefined,
		focus: () => undefined,
	},
	OmpCommandError: class extends Error {},
}));

/** 手动控制的应答（tsconfig 的 lib 不含 Promise.withResolvers）。 */
function deferred<T>() {
	let resolve: (v: T) => void = () => undefined;
	const promise = new Promise<T>((r) => {
		resolve = r;
	});
	return { promise, resolve };
}

const A = "T-0193a1b2-0000-7000-8000-0000000000e1";
const B = "T-0193a1b2-0000-7000-8000-0000000000e2";

function wrap(children: ReactNode, path = "/") {
	const client = new QueryClient({
		defaultOptions: { queries: { retry: false } },
	});
	return render(
		<QueryClientProvider client={client}>
			<MemoryRouter initialEntries={[path]}>{children}</MemoryRouter>
		</QueryClientProvider>,
	);
}

function box(): HTMLTextAreaElement {
	return screen.getByRole("textbox", {
		name: i18n.t("composer.label"),
	}) as HTMLTextAreaElement;
}

function type(value: string) {
	fireEvent.change(box(), { target: { value } });
}

function enter() {
	fireEvent.keyDown(box(), { key: "Enter" });
}

function framesOf(t: string): Record<string, unknown>[] {
	return cmd.mock.calls
		.map((c) => c[1] as Record<string, unknown>)
		.filter((f) => f.type === t);
}

let go: (to: string) => void = () => undefined;
function Nav() {
	go = useNavigate();
	return null;
}

describe("Composer", () => {
	beforeEach(() => {
		cmd.mockReset();
		resetThreadStores();
		resetComposerDrafts();
	});
	afterEach(() => {
		vi.restoreAllMocks();
	});

	it("应答回来前编辑的新草稿保留（W4）", async () => {
		const { promise, resolve } = deferred<unknown>();
		cmd.mockImplementation(() => promise);
		wrap(<Composer threadId={A} />);
		type("first");
		enter();
		expect(framesOf("prompt")).toMatchObject([{ message: "first" }]);
		type("new draft while waiting");
		await act(async () => {
			resolve({});
			await promise;
		});
		expect(box().value).toBe("new draft while waiting");
	});

	it("草稿未改动时应答后清空；同一草稿在途时不重复发送", async () => {
		const { promise, resolve } = deferred<unknown>();
		cmd.mockImplementation(() => promise);
		wrap(<Composer threadId={A} />);
		type("once");
		enter();
		enter();
		expect(framesOf("prompt")).toHaveLength(1);
		await act(async () => {
			resolve({});
			await promise;
		});
		expect(box().value).toBe("");
	});

	it("/vibe 斜杠命令按应答更新开关，第二次关闭（W12）", async () => {
		cmd.mockImplementation(async (_tid: string, f: Record<string, unknown>) => {
			if (f.type === "set_vibe_mode") return { enabled: f.enabled };
			if (f.type === "get_state")
				return { isStreaming: false, isSettled: true };
			return {};
		});
		wrap(<Composer threadId={A} />);
		type("/vibe");
		await act(async () => enter());
		await vi.waitFor(() => expect(box().value).toBe(""));
		type("/vibe");
		await act(async () => enter());
		await vi.waitFor(() =>
			expect(framesOf("set_vibe_mode").map((f) => f.enabled)).toEqual([
				true,
				false,
			]),
		);
	});

	it("切换线程后草稿不带到另一个线程，切回来仍在（W5）", () => {
		wrap(
			<>
				<Nav />
				<Routes>
					<Route path="/t/:threadId" element={<ThreadPage />} />
				</Routes>
			</>,
			`/t/${A}`,
		);
		type("draft for A");
		act(() => go(`/t/${B}`));
		expect(box().value).toBe("");
		type("draft for B");
		act(() => go(`/t/${A}`));
		expect(box().value).toBe("draft for A");
		act(() => go(`/t/${B}`));
		expect(box().value).toBe("draft for B");
	});

	describe("跨 compact 断点重新挂载（M12.1 问题 4）", () => {
		/** 与 ThreadPage 跨断点一样：同一线程的输入区换一棵子树重新挂载。 */
		function mountRemountable() {
			const client = new QueryClient({
				defaultOptions: { queries: { retry: false } },
			});
			const tree = (key: string) => (
				<QueryClientProvider client={client}>
					<MemoryRouter>
						<Composer key={key} threadId={A} />
					</MemoryRouter>
				</QueryClientProvider>
			);
			const r = render(tree("wide"));
			let n = 0;
			return () => {
				n += 1;
				r.rerender(tree(`compact-${n}`));
			};
		}

		it("文字、光标与焦点恢复", () => {
			const remount = mountRemountable();
			type("hello world");
			const before = box();
			before.focus();
			before.setSelectionRange(2, 5);
			remount();
			const after = box();
			expect(after).not.toBe(before);
			expect(after.value).toBe("hello world");
			expect(document.activeElement).toBe(after);
			expect([after.selectionStart, after.selectionEnd]).toEqual([2, 5]);
		});

		it("没在输入框里时不抢焦点", () => {
			const remount = mountRemountable();
			type("/");
			remount();
			expect(box().value).toBe("/");
			expect(document.activeElement).not.toBe(box());
		});

		it("错误行保留，下一次提交时清掉", async () => {
			cmd.mockRejectedValueOnce(new Error("orb 正忙"));
			const remount = mountRemountable();
			type("hi");
			await act(async () => enter());
			expect(screen.getByRole("alert")).toHaveTextContent("orb 正忙");
			remount();
			expect(screen.getByRole("alert")).toHaveTextContent("orb 正忙");
			expect(box().value).toBe("hi");
			cmd.mockResolvedValueOnce({});
			await act(async () => enter());
			expect(screen.queryByRole("alert")).toBeNull();
			expect(box().value).toBe("");
		});

		it("发送在途时重新挂载：应答成功后照样清空草稿", async () => {
			const { promise, resolve } = deferred<unknown>();
			cmd.mockImplementation(() => promise);
			const remount = mountRemountable();
			type("in flight");
			enter();
			remount();
			expect(box().value).toBe("in flight");
			await act(async () => {
				resolve({});
				await promise;
			});
			expect(box().value).toBe("");
		});

		it("发送未应答 → 重新挂载 → 再按 Enter：同一份草稿只发送一次；应答后可再发", async () => {
			const { promise, resolve } = deferred<unknown>();
			cmd.mockImplementation(() => promise);
			const remount = mountRemountable();
			type("only once");
			enter();
			remount();
			enter();
			expect(framesOf("prompt")).toHaveLength(1);
			await act(async () => {
				resolve({});
				await promise;
			});
			// 在途标记随应答清掉：同样的文字再发是新的一条
			cmd.mockResolvedValue({});
			type("only once");
			await act(async () => enter());
			expect(framesOf("prompt")).toHaveLength(2);
		});

		it("发送在途时重新挂载后又改了草稿：应答回来不清掉新草稿", async () => {
			const { promise, resolve } = deferred<unknown>();
			cmd.mockImplementation(() => promise);
			const remount = mountRemountable();
			type("in flight");
			enter();
			remount();
			type("typed after remount");
			await act(async () => {
				resolve({});
				await promise;
			});
			expect(box().value).toBe("typed after remount");
		});

		it("set_editor_text 只并入一次：重新挂载不再前置一遍", () => {
			const remount = mountRemountable();
			type("tail");
			act(() =>
				getThreadStore(A)
					.getState()
					.patchLocal({
						editorText: { text: "head ", prepend: true, nonce: 1 },
					}),
			);
			expect(box().value).toBe("head tail");
			remount();
			expect(box().value).toBe("head tail");
		});
	});
});

describe("Composer 附件入口", () => {
	afterEach(() => {
		vi.unstubAllGlobals();
		upload.mockReset();
	});

	it("触屏：工具行有拍照键，拍到的照片上传到本线程", async () => {
		vi.stubGlobal("matchMedia", (q: string) => ({
			matches: q === "(pointer: coarse)",
			media: q,
			addEventListener: () => undefined,
			removeEventListener: () => undefined,
		}));
		upload.mockReturnValue(new Promise(() => undefined));
		wrap(<Composer threadId={A} />);
		expect(screen.getByTestId("attach-file-input")).toBeInTheDocument();
		const cam = screen.getByTestId("attach-camera-input");
		expect(cam).toHaveAttribute("capture", "environment");
		const photo = new File(["x"], "photo.jpg", { type: "image/jpeg" });
		await act(async () => {
			fireEvent.change(cam, { target: { files: [photo] } });
		});
		expect(upload).toHaveBeenCalledWith(
			photo,
			expect.objectContaining({ threadId: A }),
		);
		expect(screen.getByText("photo.jpg")).toBeInTheDocument();
	});

	it("非触屏：没有拍照键", () => {
		wrap(<Composer threadId={A} />);
		expect(screen.getByTestId("attach-file-input")).toBeInTheDocument();
		expect(screen.queryByTestId("attach-camera")).toBeNull();
	});
});
