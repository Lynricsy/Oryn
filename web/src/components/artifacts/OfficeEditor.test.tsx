/**
 * OfficeEditor：建会话后把令牌以表单 POST 到引擎帧；只接受引擎源且发自编辑帧的消息；`Frame_Ready` 后回
 * `Host_PostmessageReady`；「完成」先发 `Action_Save` 再关闭会话（回执超时也关闭）；每 60 秒心跳；卸载时保存并关闭；
 * StrictMode 的模拟卸载不关闭正在用的会话；打不开时显示错误并可返回。
 */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
	act,
	fireEvent,
	render,
	screen,
	waitFor,
} from "@testing-library/react";
import { StrictMode } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import i18n from "../../i18n";
import { dismissToast, toastsStore } from "../../state/toasts";
import {
	OFFICE_BEAT_MS,
	OFFICE_SAVE_WAIT_MS,
	OfficeEditor,
	parseEngineMessage,
} from "./OfficeEditor";

const ID = "art_01JDNAWEB0000000000000000A";
const SID = "aos_01JDNAWEB0000000000000000A";
const ENGINE = "https://office.example.test";
const ACTION = `${ENGINE}/browser/abc/cool.html?WOPISrc=https%3A%2F%2Foryn.example.test%2Fwopi%2Ffiles%2F${ID}&lang=zh-CN`;
const TOKEN = "k".repeat(43);
const TTL = 1_900_000_000_000;

/** 请求与发往引擎的消息按发生顺序记在一起。 */
let log: string[];
let openFails: boolean;
let submits: HTMLFormElement[];
// 建会话的次数：第一次回 SID，之后回 `${SID}<n>`
let opens: number;

beforeEach(() => {
	vi.useFakeTimers({ shouldAdvanceTime: true });
	log = [];
	openFails = false;
	submits = [];
	opens = 0;
	vi.spyOn(HTMLFormElement.prototype, "submit").mockImplementation(function (
		this: HTMLFormElement,
	) {
		submits.push(this);
	});
	vi.stubGlobal(
		"fetch",
		vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
			const req =
				input instanceof Request
					? input
					: new Request(new URL(String(input), location.origin), init);
			const path = new URL(req.url).pathname;
			log.push(`${req.method} ${path}`);
			if (path === `/api/v1/artifacts/${ID}/office-sessions`) {
				if (openFails)
					return Response.json(
						{ code: "invalid", message: "这个类型不能编辑" },
						{ status: 400 },
					);
				opens += 1;
				return Response.json(
					{
						sessionId: opens === 1 ? SID : `${SID}${opens}`,
						actionUrl: ACTION,
						accessToken: TOKEN,
						accessTokenTtl: TTL,
					},
					{ status: 201 },
				);
			}
			return new Response(null, { status: 204 });
		}),
	);
});

afterEach(() => {
	for (const x of toastsStore.getState().toasts) dismissToast(x.id);
	vi.useRealTimers();
	vi.unstubAllGlobals();
	vi.restoreAllMocks();
});

async function mount(onClose = vi.fn()) {
	const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
	const view = render(
		<QueryClientProvider client={qc}>
			<OfficeEditor artifactId={ID} title="季度报告" onClose={onClose} />
		</QueryClientProvider>,
	);
	const frame = (await screen.findByTestId(
		"office-editor-frame",
	)) as HTMLIFrameElement;
	await act(async () => {});
	const win = frame.contentWindow;
	if (!win) throw new Error("iframe 没有窗口");
	const sent: { msg: Record<string, unknown>; origin: string }[] = [];
	vi.spyOn(win, "postMessage").mockImplementation(((
		msg: unknown,
		origin: string,
	) => {
		const parsed = JSON.parse(String(msg)) as Record<string, unknown>;
		sent.push({ msg: parsed, origin });
		log.push(`post ${String(parsed.MessageId)}`);
	}) as Window["postMessage"]);
	return { view, frame, sent, onClose };
}

/** 以 `source` 的身份、`origin` 为来源向父页面发一条引擎消息。 */
function fromEngine(
	source: Window | null,
	id: string,
	values: Record<string, unknown> = {},
	origin = ENGINE,
) {
	act(() => {
		window.dispatchEvent(
			new MessageEvent("message", {
				data: JSON.stringify({ MessageId: id, SendTime: 1, Values: values }),
				origin,
				source,
			}),
		);
	});
}

function loadDocument(frame: HTMLIFrameElement) {
	fromEngine(frame.contentWindow, "App_LoadingStatus", {
		Status: "Document_Loaded",
	});
}

describe("parseEngineMessage", () => {
	it("只认 JSON 字符串里带字符串 MessageId 的对象", () => {
		expect(
			parseEngineMessage(
				JSON.stringify({
					MessageId: "Action_Save_Resp",
					Values: { success: true },
				}),
			),
		).toEqual({ id: "Action_Save_Resp", values: { success: true } });
		expect(
			parseEngineMessage(JSON.stringify({ MessageId: "UI_Close" })),
		).toEqual({ id: "UI_Close", values: {} });
		expect(parseEngineMessage({ MessageId: "UI_Close" })).toBeNull();
		expect(parseEngineMessage("not json")).toBeNull();
		expect(parseEngineMessage(JSON.stringify({ MessageId: 1 }))).toBeNull();
		expect(parseEngineMessage(JSON.stringify(["x"]))).toBeNull();
	});
});

describe("OfficeEditor", () => {
	it("建会话后把令牌以隐藏表单 POST 到同名的引擎帧", async () => {
		const { frame } = await mount();
		expect(log[0]).toBe(`POST /api/v1/artifacts/${ID}/office-sessions`);
		expect(submits).toHaveLength(1);
		const form = submits[0] as HTMLFormElement;
		expect(form.getAttribute("action")).toBe(ACTION);
		expect(form.getAttribute("method")).toBe("post");
		expect(form.target).toBe(frame.name);
		expect(frame.name).not.toBe("");
		const fields = Object.fromEntries(new FormData(form).entries());
		expect(fields).toEqual({
			access_token: TOKEN,
			access_token_ttl: String(TTL),
		});
		expect(frame.getAttribute("allow")).toBe("clipboard-read; clipboard-write");
		expect(frame.getAttribute("src")).toBeNull();
		expect(screen.getByTestId("office-editor-loading")).toBeInTheDocument();
	});

	it("只接受引擎源且发自编辑帧的消息；Frame_Ready 后回 Host_PostmessageReady", async () => {
		const { frame, sent } = await mount();
		// 别的来源、别的窗口发来的都不理
		fromEngine(
			frame.contentWindow,
			"App_LoadingStatus",
			{ Status: "Frame_Ready" },
			"https://evil.example.test",
		);
		fromEngine(window, "App_LoadingStatus", { Status: "Frame_Ready" });
		fromEngine(
			frame.contentWindow,
			"App_LoadingStatus",
			{ Status: "Document_Loaded" },
			location.origin,
		);
		expect(sent).toEqual([]);
		expect(screen.getByTestId("office-editor-loading")).toBeInTheDocument();

		fromEngine(frame.contentWindow, "App_LoadingStatus", {
			Status: "Frame_Ready",
		});
		expect(sent).toHaveLength(1);
		expect(sent[0]?.origin).toBe(ENGINE);
		expect(sent[0]?.msg).toMatchObject({ MessageId: "Host_PostmessageReady" });

		loadDocument(frame);
		expect(screen.queryByTestId("office-editor-loading")).toBeNull();
	});

	it("完成：先发 Action_Save，收到回执后再关闭会话并返回", async () => {
		const onClose = vi.fn();
		const { frame, sent } = await mount(onClose);
		loadDocument(frame);
		log = [];
		fireEvent.click(screen.getByTestId("office-editor-done"));
		expect(screen.getByTestId("office-editor-done")).toHaveTextContent(
			i18n.t("artifacts.office.saving"),
		);
		expect(sent.at(-1)?.msg).toMatchObject({
			MessageId: "Action_Save",
			Values: {
				DontTerminateEdit: true,
				DontSaveIfUnmodified: true,
				Notify: true,
			},
		});
		await act(async () => {});
		expect(log).toEqual(["post Action_Save"]);
		expect(onClose).not.toHaveBeenCalled();

		fromEngine(frame.contentWindow, "Action_Save_Resp", { success: true });
		await waitFor(() => expect(onClose).toHaveBeenCalledTimes(1));
		expect(log).toEqual([
			"post Action_Save",
			`DELETE /api/v1/artifact-office-sessions/${SID}`,
		]);
	});

	it("引擎回保存失败：不关闭会话、编辑器留着并提示原因；再点完成且成功才关闭", async () => {
		const onClose = vi.fn();
		const { frame } = await mount(onClose);
		loadDocument(frame);
		log = [];
		fireEvent.click(screen.getByTestId("office-editor-done"));
		fromEngine(frame.contentWindow, "Action_Save_Resp", {
			success: false,
			errorMsg: "磁盘已满",
		});
		const alert = await screen.findByTestId("office-editor-save-failed");
		expect(alert).toHaveTextContent(
			i18n.t("artifacts.office.saveFailed", { reason: "磁盘已满" }),
		);
		await act(() => vi.advanceTimersByTimeAsync(OFFICE_SAVE_WAIT_MS * 2));
		expect(log).toEqual(["post Action_Save"]);
		expect(onClose).not.toHaveBeenCalled();
		expect(screen.getByTestId("office-editor-frame")).toBeInTheDocument();
		expect(screen.getByTestId("office-editor-done")).toBeEnabled();

		// 缺 Values 也算失败，提示通用原因
		fireEvent.click(screen.getByTestId("office-editor-done"));
		expect(screen.queryByTestId("office-editor-save-failed")).toBeNull();
		act(() => {
			window.dispatchEvent(
				new MessageEvent("message", {
					data: JSON.stringify({ MessageId: "Action_Save_Resp" }),
					origin: ENGINE,
					source: frame.contentWindow,
				}),
			);
		});
		expect(
			await screen.findByTestId("office-editor-save-failed"),
		).toHaveTextContent(
			i18n.t("artifacts.office.saveFailed", {
				reason: i18n.t("artifacts.office.saveFailedGeneric"),
			}),
		);
		expect(log).toEqual(["post Action_Save", "post Action_Save"]);

		fireEvent.click(screen.getByTestId("office-editor-done"));
		fromEngine(frame.contentWindow, "Action_Save_Resp", { success: true });
		await waitFor(() => expect(onClose).toHaveBeenCalledTimes(1));
		expect(log).toEqual([
			"post Action_Save",
			"post Action_Save",
			"post Action_Save",
			`DELETE /api/v1/artifact-office-sessions/${SID}`,
		]);
	});

	it("等不到 Action_Save_Resp：10 秒后照常关闭会话并提示保存未确认", async () => {
		const onClose = vi.fn();
		const { frame } = await mount(onClose);
		loadDocument(frame);
		log = [];
		fireEvent.click(screen.getByTestId("office-editor-done"));
		await act(() => vi.advanceTimersByTimeAsync(OFFICE_SAVE_WAIT_MS - 100));
		expect(log).toEqual(["post Action_Save"]);
		await act(() => vi.advanceTimersByTimeAsync(200));
		await waitFor(() => expect(onClose).toHaveBeenCalledTimes(1));
		expect(log).toEqual([
			"post Action_Save",
			`DELETE /api/v1/artifact-office-sessions/${SID}`,
		]);
		expect(toastsStore.getState().toasts.map((x) => x.text)).toContain(
			i18n.t("artifacts.office.saveUnconfirmed"),
		);
	});

	it("文档还没加载完就点完成：不等保存回执，直接关闭会话", async () => {
		const onClose = vi.fn();
		await mount(onClose);
		log = [];
		fireEvent.click(screen.getByTestId("office-editor-done"));
		await waitFor(() => expect(onClose).toHaveBeenCalledTimes(1));
		expect(log).toEqual([`DELETE /api/v1/artifact-office-sessions/${SID}`]);
	});

	it("每 60 秒发一次心跳", async () => {
		await mount();
		const beats = () =>
			log.filter(
				(l) => l === `POST /api/v1/artifact-office-sessions/${SID}/beat`,
			).length;
		expect(beats()).toBe(0);
		await act(() => vi.advanceTimersByTimeAsync(OFFICE_BEAT_MS));
		expect(beats()).toBe(1);
		await act(() => vi.advanceTimersByTimeAsync(OFFICE_BEAT_MS));
		expect(beats()).toBe(2);
	});

	it("卸载时先发 Action_Save 再关闭会话，心跳停止", async () => {
		const { frame, view } = await mount();
		loadDocument(frame);
		log = [];
		view.unmount();
		await waitFor(() =>
			expect(log).toEqual([
				"post Action_Save",
				`DELETE /api/v1/artifact-office-sessions/${SID}`,
			]),
		);
		await act(() => vi.advanceTimersByTimeAsync(OFFICE_BEAT_MS * 2));
		expect(log).toHaveLength(2);
	});

	it("StrictMode：模拟卸载不关闭正在用的会话，表单只提交一次，完成照常关闭", async () => {
		const onClose = vi.fn();
		const qc = new QueryClient();
		render(
			<StrictMode>
				<QueryClientProvider client={qc}>
					<OfficeEditor artifactId={ID} title="季度报告" onClose={onClose} />
				</QueryClientProvider>
			</StrictMode>,
		);
		const frame = (await screen.findByTestId(
			"office-editor-frame",
		)) as HTMLIFrameElement;
		await act(() => vi.advanceTimersByTimeAsync(50));
		// 建会话的 effect 跑了两次：第一次的会话作废关闭，编辑器用第二次的
		const live = `${SID}2`;
		const deletes = (sid: string) =>
			log.filter((l) => l === `DELETE /api/v1/artifact-office-sessions/${sid}`)
				.length;
		expect(deletes(SID)).toBe(1);
		expect(deletes(live)).toBe(0);
		expect(submits).toHaveLength(1);

		const win = frame.contentWindow as Window;
		vi.spyOn(win, "postMessage").mockImplementation(() => undefined);
		loadDocument(frame);
		await act(() => vi.advanceTimersByTimeAsync(OFFICE_BEAT_MS));
		expect(log).toContain(`POST /api/v1/artifact-office-sessions/${live}/beat`);
		expect(deletes(live)).toBe(0);

		fireEvent.click(screen.getByTestId("office-editor-done"));
		fromEngine(frame.contentWindow, "Action_Save_Resp", { success: true });
		await waitFor(() => expect(onClose).toHaveBeenCalledTimes(1));
		expect(deletes(live)).toBe(1);
	});

	it("UI_Close 等同完成", async () => {
		const onClose = vi.fn();
		const { frame } = await mount(onClose);
		fromEngine(frame.contentWindow, "UI_Close");
		await waitFor(() => expect(onClose).toHaveBeenCalledTimes(1));
		expect(log).toContain(`DELETE /api/v1/artifact-office-sessions/${SID}`);
	});

	it("建会话失败：显示原因，可返回也可重试", async () => {
		openFails = true;
		const onClose = vi.fn();
		const qc = new QueryClient();
		render(
			<QueryClientProvider client={qc}>
				<OfficeEditor artifactId={ID} title="季度报告" onClose={onClose} />
			</QueryClientProvider>,
		);
		const error = await screen.findByTestId("office-editor-error");
		expect(error).toHaveTextContent(i18n.t("artifacts.office.openFailed"));
		fireEvent.click(screen.getByText(i18n.t("artifacts.office.back")));
		expect(onClose).toHaveBeenCalledTimes(1);

		openFails = false;
		fireEvent.click(screen.getByText(i18n.t("artifacts.viewer.retry")));
		expect(
			await screen.findByTestId("office-editor-frame"),
		).toBeInTheDocument();
		// findBy 在提交后就返回，提交表单的被动 effect 可能还没跑
		await waitFor(() => expect(submits).toHaveLength(1));
	});
});
