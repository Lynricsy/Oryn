/** 同意对话框：一次一个、允许与不允许、Esc 等于不允许、别处先回答或不再观看时收走、回答失败时留着。 */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
	act,
	fireEvent,
	render,
	screen,
	waitFor,
} from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import i18n from "../../i18n";
import type { ArtifactConsentEvent } from "../../realtime/socket";
import { ConsentDialog } from "./ConsentDialog";

// 测试按字符串键取文案（含插值），绕开 i18next 按键推导的参数类型
const T = i18n.t.bind(i18n) as unknown as (
	k: string,
	o?: Record<string, string>,
) => string;

const ART = "art_01JCZ8M3W4X5Y6Z7A8B9C0D1E2";
const OTHER = "art_01JCZ8M3W4X5Y6Z7A8B9C0D1E3";

function asked(
	requestId: string,
	p: Partial<ArtifactConsentEvent> = {},
): ArtifactConsentEvent {
	return {
		type: "artifact.consent",
		kind: "asked",
		requestId,
		artifactId: ART,
		title: "仓库浏览",
		project: "oryn",
		capability: "files",
		...p,
	};
}

/** 假的实时客户端：测试里手动推送 artifact.consent 与观看变化。 */
function fakeClient() {
	let onConsent: ((m: ArtifactConsentEvent) => void) | null = null;
	let onWatch: (() => void) | null = null;
	const watching = new Set([ART, OTHER]);
	return {
		client: {
			onArtifactConsent: (cb: (m: ArtifactConsentEvent) => void) => {
				onConsent = cb;
				return () => {
					onConsent = null;
				};
			},
			isWatchingArtifact: (id: string) => watching.has(id),
			onArtifactWatchesChanged: (cb: () => void) => {
				onWatch = cb;
				return () => {
					onWatch = null;
				};
			},
		},
		push: (m: ArtifactConsentEvent) => act(() => onConsent?.(m)),
		unwatch: (id: string) =>
			act(() => {
				watching.delete(id);
				onWatch?.();
			}),
	};
}

let answers: { key: string; allow: boolean }[];

function setup(status = 204) {
	answers = [];
	vi.stubGlobal(
		"fetch",
		vi.fn(async (input: Request) => {
			const key = new URL(input.url).pathname.split("/").pop() ?? "";
			answers.push({ key, allow: (await input.json()).allow });
			return status === 204
				? new Response(null, { status })
				: Response.json({ code: "x", message: "失败了" }, { status });
		}),
	);
	const f = fakeClient();
	render(
		<QueryClientProvider client={new QueryClient()}>
			<ConsentDialog client={f.client} />
		</QueryClientProvider>,
	);
	return f;
}

afterEach(() => {
	vi.unstubAllGlobals();
});

describe("ConsentDialog", () => {
	it("files：标题带项目名，允许后回答 allow=true 并收走", async () => {
		const f = setup();
		expect(screen.queryByRole("dialog")).toBeNull();
		f.push(asked("acr_1"));
		const dialog = screen.getByRole("dialog");
		expect(dialog).toHaveTextContent(
			T("artifacts.consent.filesTitle", { title: "仓库浏览", project: "oryn" }),
		);
		expect(dialog).toHaveTextContent(T("artifacts.consent.filesBody"));
		fireEvent.click(
			screen.getByRole("button", { name: T("artifacts.consent.allow") }),
		);
		await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
		expect(answers).toEqual([{ key: "acr_1", allow: true }]);
	});

	it("一次一个：回答第一个后显示第二个；Esc 等于不允许", async () => {
		const f = setup();
		f.push(asked("acr_1"));
		f.push(asked("acr_2", { capability: "sample", title: "问答" }));
		f.push(asked("acr_1")); // 重复的 asked（新连接加入时重发）不重复排队
		fireEvent.click(
			screen.getByRole("button", { name: T("artifacts.consent.deny") }),
		);
		await waitFor(() =>
			expect(screen.getByRole("dialog")).toHaveTextContent(
				T("artifacts.consent.sampleTitle", { title: "问答" }),
			),
		);
		expect(screen.getByRole("dialog")).toHaveTextContent(
			T("artifacts.consent.sampleBody"),
		);
		fireEvent.keyDown(document, { key: "Escape" });
		await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
		expect(answers).toEqual([
			{ key: "acr_1", allow: false },
			{ key: "acr_2", allow: false },
		]);
	});

	it("别处先回答（settled）或不再观看时收走，不发回答；没在看的 artifact 的请求不显示", () => {
		const f = setup();
		f.push(asked("acr_x", { artifactId: "art_01JCZ8M3W4X5Y6Z7A8B9C0D1E9" }));
		expect(screen.queryByRole("dialog")).toBeNull();

		f.push(asked("acr_1"));
		expect(screen.getByRole("dialog")).toBeInTheDocument();
		f.push({
			type: "artifact.consent",
			kind: "settled",
			requestId: "acr_1",
			artifactId: ART,
			allowed: true,
			reload: false,
		});
		expect(screen.queryByRole("dialog")).toBeNull();

		f.push(asked("acr_2", { artifactId: OTHER }));
		expect(screen.getByRole("dialog")).toBeInTheDocument();
		f.unwatch(OTHER);
		expect(screen.queryByRole("dialog")).toBeNull();
		expect(answers).toEqual([]);
	});

	it("没有项目时用不带项目名的标题", () => {
		const f = setup();
		f.push(asked("acr_1", { project: undefined }));
		expect(screen.getByRole("dialog")).toHaveTextContent(
			T("artifacts.consent.filesTitleNoProject", { title: "仓库浏览" }),
		);
	});

	it("请求已结束（404）直接收走；其它失败留着并显示错误", async () => {
		const gone = setup(404);
		gone.push(asked("acr_1"));
		fireEvent.click(
			screen.getByRole("button", { name: T("artifacts.consent.allow") }),
		);
		await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
	});

	it("服务端出错时对话框留着并显示错误，可以再试", async () => {
		const f = setup(500);
		f.push(asked("acr_1"));
		fireEvent.click(
			screen.getByRole("button", { name: T("artifacts.consent.allow") }),
		);
		expect(await screen.findByRole("alert")).toBeInTheDocument();
		expect(screen.getByRole("dialog")).toBeInTheDocument();
		expect(
			screen.getByRole("button", { name: T("artifacts.consent.allow") }),
		).toBeEnabled();
	});
});
