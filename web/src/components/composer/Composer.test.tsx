/**
 * 输入区回归：等待应答期间的新草稿不被清空（W4）、同一草稿不重复发送、斜杠模式命令对账（W12）、
 * 切换线程时草稿各自保留、不串（W5）；跨 compact 断点重新挂载时草稿、错误行、光标保留（M12.1 问题 4）；
 * 触屏上工具行有拍照键，拍到的照片作为附件上传到本线程；全局助手对话的附件传进助手文件库、以 `orynFiles` 发送（M13）；
 * agent 未就绪时发出的消息立即以「发送中」气泡进转录、输入区清空，失败时草稿放回。
 */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, fireEvent, render, screen } from "@testing-library/react";
import type { ReactNode } from "react";
import { MemoryRouter, Route, Routes, useNavigate } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Orb, Thread } from "../../api/client";
import { identityKeys } from "../../api/identity-queries";
import { qk } from "../../api/queries";
import i18n from "../../i18n";
import type * as AttachmentsLib from "../../lib/attachments";
import type { Attachment } from "../../lib/attachments";
import type * as ImagesLib from "../../lib/images";
import { ThreadPage } from "../../pages/ThreadPage";
import { resetComposerDrafts } from "../../state/composer-drafts";
import {
	getThreadStore,
	indexStore,
	resetThreadStores,
} from "../../state/stores";
import type { TranscriptItem } from "../../state/transcript";
import { TranscriptContext } from "../transcript/context";
import { pendingPromptStore } from "../transcript/pending-prompt";
import { Transcript } from "../transcript/Transcript";
import { Composer } from "./Composer";

const { cmd, upload, uploadLib } = vi.hoisted(() => ({
	cmd: vi.fn(),
	upload: vi.fn(),
	uploadLib: vi.fn(),
}));
vi.mock("../../lib/attachments", async (orig) => ({
	...(await orig<typeof AttachmentsLib>()),
	uploadAttachment: upload,
	uploadAssistantFile: uploadLib,
}));
vi.mock("../../lib/images", async (orig) => ({
	...(await orig<typeof ImagesLib>()),
	// 不解码：图片只作为文件附件
	prepareImage: () => Promise.reject(new Error("no decode")),
}));
vi.mock("../../realtime/socket", () => ({
	realtime: {
		// agent 就绪时模式条会查询开关状态、角色轨会预取角色数据：挂起不应答，不占用各用例给 `cmd` 设的应答
		cmd: (tid: string, frame: { type: string }, ...rest: unknown[]) =>
			[
				"get_vibe_mode",
				"get_loop_mode",
				"get_settings",
				"get_model_roles",
			].includes(frame.type)
				? new Promise(() => undefined)
				: cmd(tid, frame, ...rest),
		uiResponse: () => true,
		subscribe: () => () => undefined,
		focus: () => undefined,
		onBrowserRelayState: () => () => undefined,
		onBrowserRelayClients: () => () => undefined,
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
		// 默认 agent 已就绪：应答后清空的原有行为（未就绪的气泡路径见文末）
		getThreadStore(A).setState({ agentStatus: "ready" });
		getThreadStore(B).setState({ agentStatus: "ready" });
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

describe("全局助手对话的附件（M13 4.2）", () => {
	const K = "T-0193a1b2-0000-7000-8000-0000000000e3";
	const S = "T-0193a1b2-0000-7000-8000-0000000000e4";
	const th = (id: string, kind: Thread["kind"]): Thread => ({
		id,
		title: id,
		titleSource: "auto",
		state: "idle",
		stateChangedAt: 1,
		pinned: false,
		labels: [],
		unread: false,
		createdAt: 1,
		updatedAt: 1,
		kind,
	});

	beforeEach(() => {
		cmd.mockReset();
		cmd.mockResolvedValue({});
		upload.mockReset();
		uploadLib.mockReset();
		resetThreadStores();
		resetComposerDrafts();
		indexStore.getState().upsertThread(th(K, "assistant"));
		indexStore.getState().upsertThread(th(S, "space"));
	});

	it("有附件按钮；文件传进助手文件库，发送带 orynFiles、不带 orynAttachments", async () => {
		uploadLib.mockResolvedValue({
			id: "afl_1",
			name: "notes.txt",
			mime: "text/plain",
			size: 1,
			sha256: "0".repeat(64),
			scope: "conversation",
			source: "upload",
			createdAt: 1,
		});
		wrap(<Composer threadId={K} coordinator />);
		const file = new File(["x"], "notes.txt", { type: "text/plain" });
		await act(async () => {
			fireEvent.change(screen.getByTestId("attach-file-input"), {
				target: { files: [file] },
			});
		});
		expect(uploadLib).toHaveBeenCalledWith(
			file,
			expect.objectContaining({ threadId: K }),
		);
		expect(upload).not.toHaveBeenCalled();
		type("看看这个");
		await act(async () => {
			enter();
		});
		const [frame] = framesOf("prompt");
		expect(frame).toMatchObject({
			message: "看看这个",
			orynFiles: ["afl_1"],
		});
		expect(frame).not.toHaveProperty("orynAttachments");
	});

	it("Space 聊天（协调对话）仍然没有附件入口", () => {
		wrap(<Composer threadId={S} coordinator />);
		expect(screen.queryByTestId("attach-file-input")).toBeNull();
	});
});

describe("agent 未就绪时发送：转录先显示「发送中」气泡", () => {
	const ctx = {
		threadId: A,
		toolResults: new Map(),
		liveTools: {},
		subagents: {},
		openSubagent: () => {},
		forkFrom: () => {},
		rewindTo: () => {},
		labelEntry: () => {},
	};
	const userRow = (seq: number, text: string): TranscriptItem => ({
		kind: "message",
		key: `e${seq}`,
		seq,
		entryId: `E${seq}`,
		message: { role: "user", content: [{ type: "text", text }] },
		live: false,
	});
	const old = userRow(1, "早先的消息");

	beforeEach(() => {
		cmd.mockReset();
		upload.mockReset();
		resetThreadStores();
		resetComposerDrafts();
		pendingPromptStore.setState({ prompts: {} });
		// 已有一条 user 消息（seq 1），线程条目到 seq 2；agent 在启动
		getThreadStore(A).setState({ agentStatus: "starting", lastSeq: 2 });
		// jsdom 没有布局：给虚拟列表一个足够高的视口，所有行都挂载
		vi.spyOn(HTMLElement.prototype, "clientHeight", "get").mockReturnValue(
			4000,
		);
		vi.spyOn(HTMLElement.prototype, "clientWidth", "get").mockReturnValue(800);
	});
	afterEach(() => {
		vi.restoreAllMocks();
	});

	/** 转录与输入区同挂；返回用新条目重渲染转录的函数。 */
	function mount(items: TranscriptItem[] = [old]) {
		const client = new QueryClient({
			defaultOptions: {
				queries: { retry: false, staleTime: Number.POSITIVE_INFINITY },
			},
		});
		client.setQueryData(qk.agentIdentity, {});
		client.setQueryData(qk.userIdentity, {});
		client.setQueryData(qk.gitIdentity, {});
		client.setQueryData(identityKeys.roles, []);
		const tree = (rows: TranscriptItem[]) => (
			<QueryClientProvider client={client}>
				<MemoryRouter>
					<TranscriptContext.Provider value={ctx}>
						<Transcript items={rows} />
					</TranscriptContext.Provider>
					<Composer threadId={A} />
				</MemoryRouter>
			</QueryClientProvider>
		);
		const r = render(tree(items));
		return (rows: TranscriptItem[]) => r.rerender(tree(rows));
	}

	const sending = () => screen.queryAllByText(i18n.t("transcript.sending"));

	it("发送即显示气泡、输入区清空；对应的 user 条目到达后气泡消失，文字只出现一次", async () => {
		const { promise, resolve } = deferred<unknown>();
		cmd.mockImplementation(() => promise);
		const setItems = mount();
		type("你好");
		act(() => enter());
		expect(framesOf("prompt")).toMatchObject([{ message: "你好" }]);
		expect(box().value).toBe("");
		expect(sending()).toHaveLength(1);
		expect(screen.getAllByText("你好")).toHaveLength(1);
		// 应答先到、条目未到：气泡仍在，不空一下
		await act(async () => {
			resolve({});
			await promise;
		});
		expect(sending()).toHaveLength(1);
		act(() => setItems([old, userRow(3, "你好")]));
		expect(sending()).toHaveLength(0);
		expect(screen.getAllByText("你好")).toHaveLength(1);
		expect(pendingPromptStore.getState().prompts[A]).toBeUndefined();
	});

	it("连续发出的几条按顺序显示，user 条目逐条认领", () => {
		cmd.mockImplementation(() => new Promise(() => undefined));
		const setItems = mount();
		type("第一条");
		act(() => enter());
		type("第二条");
		act(() => enter());
		expect(framesOf("prompt")).toHaveLength(2);
		expect(sending()).toHaveLength(2);
		const first = screen.getByText("第一条");
		const second = screen.getByText("第二条");
		expect(
			first.compareDocumentPosition(second) & Node.DOCUMENT_POSITION_FOLLOWING,
		).toBeTruthy();
		act(() => setItems([old, userRow(3, "第一条")]));
		expect(sending()).toHaveLength(1);
		expect(screen.getAllByText("第一条")).toHaveLength(1);
		expect(screen.getAllByText("第二条")).toHaveLength(1);
		act(() => setItems([old, userRow(3, "第一条"), userRow(4, "第二条")]));
		expect(sending()).toHaveLength(0);
		expect(screen.getAllByText("第二条")).toHaveLength(1);
		expect(pendingPromptStore.getState().prompts[A]).toBeUndefined();
	});

	it("同样的文字在途时再发一次也是新的一条（草稿已离开输入区）", () => {
		cmd.mockImplementation(() => new Promise(() => undefined));
		mount();
		type("继续");
		act(() => enter());
		type("继续");
		act(() => enter());
		expect(framesOf("prompt")).toHaveLength(2);
		expect(sending()).toHaveLength(2);
	});

	it("发送失败：气泡消失，草稿与附件放回输入区，显示错误", async () => {
		const att: Attachment = {
			id: "att_1",
			name: "a.txt",
			mime: "text/plain",
			size: 1,
			sha256: "0".repeat(64),
			createdAt: 1,
		};
		upload.mockResolvedValue(att);
		// tsconfig 的 lib 不含 Promise.withResolvers
		let reject: (e: unknown) => void = () => undefined;
		cmd.mockImplementation(
			() =>
				new Promise((_, r) => {
					reject = r;
				}),
		);
		mount();
		await act(async () => {
			fireEvent.change(screen.getByTestId("attach-file-input"), {
				target: { files: [new File(["x"], "a.txt", { type: "text/plain" })] },
			});
		});
		type("会失败的消息");
		act(() => enter());
		expect(framesOf("prompt")).toMatchObject([
			{ message: "会失败的消息", orynAttachments: ["att_1"] },
		]);
		expect(box().value).toBe("");
		expect(screen.queryByText("a.txt")).toBeNull();
		expect(sending()).toHaveLength(1);
		await act(async () => reject(new Error("agent 启动失败")));
		expect(sending()).toHaveLength(0);
		expect(box().value).toBe("会失败的消息");
		expect(screen.getByText("a.txt")).toBeInTheDocument();
		expect(screen.getByRole("alert")).toHaveTextContent("agent 启动失败");
	});

	it("agent 已就绪：不显示气泡，草稿等应答后才清空", async () => {
		getThreadStore(A).setState({ agentStatus: "ready" });
		const { promise, resolve } = deferred<unknown>();
		cmd.mockImplementation(() => promise);
		mount();
		type("立刻回答");
		act(() => enter());
		expect(sending()).toHaveLength(0);
		expect(box().value).toBe("立刻回答");
		await act(async () => {
			resolve({});
			await promise;
		});
		expect(box().value).toBe("");
	});

	it("orb 冻结（agent 仍是 ready）也按未就绪显示气泡", () => {
		getThreadStore(A).setState({
			agentStatus: "ready",
			orb: { state: "frozen" } as Orb,
		});
		cmd.mockImplementation(() => new Promise(() => undefined));
		mount();
		type("醒醒");
		act(() => enter());
		expect(sending()).toHaveLength(1);
		expect(box().value).toBe("");
	});

	it("bash 与斜杠命令不走气泡", () => {
		cmd.mockImplementation(() => new Promise(() => undefined));
		mount();
		type("!ls");
		act(() => enter());
		type("/unknown-cmd arg");
		act(() => enter());
		expect(sending()).toHaveLength(0);
	});
});
