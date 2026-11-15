/**
 * 输入区斜杠命令：文本路径一律发 `prompt`（M1）、应答生成本地提示（M4）、长命令超时（M5）、
 * 附件随命令发出或留在输入区（M6）、/model 选择器、/plan-review 与入口类命令、不可用说明。
 */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, fireEvent, render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { beforeEach, describe, expect, it, vi } from "vitest";
import i18n from "../../i18n";
import type * as AttachmentsLib from "../../lib/attachments";
import { resetComposerDrafts } from "../../state/composer-drafts";
import { getThreadStore, resetThreadStores } from "../../state/stores";
import { uiStore } from "../../state/ui";
import { Composer } from "./Composer";

const { cmd, upload } = vi.hoisted(() => ({ cmd: vi.fn(), upload: vi.fn() }));
vi.mock("../../lib/attachments", async (orig) => ({
	...(await orig<typeof AttachmentsLib>()),
	uploadAttachment: upload,
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

const A = "T-0193a1b2-0000-7000-8000-0000000000f1";

type Frame = Record<string, unknown>;
type Reply = (f: Frame) => unknown;

function reply(handlers: Record<string, Reply> = {}) {
	cmd.mockImplementation(async (_tid: string, f: Frame) => {
		const h = handlers[f.type as string];
		if (h) return h(f);
		if (f.type === "get_state") return { isStreaming: false, isSettled: true };
		return {};
	});
}

function mount() {
	const client = new QueryClient({
		defaultOptions: { queries: { retry: false } },
	});
	return render(
		<QueryClientProvider client={client}>
			<MemoryRouter>
				<Composer threadId={A} />
			</MemoryRouter>
		</QueryClientProvider>,
	);
}

function box(): HTMLTextAreaElement {
	return screen.getByRole("textbox", {
		name: i18n.t("composer.label"),
	}) as HTMLTextAreaElement;
}

async function send(text: string) {
	fireEvent.change(box(), { target: { value: text } });
	// 菜单打开时 Enter 先补全高亮项：先 Esc 收起，再发送
	fireEvent.keyDown(box(), { key: "Escape" });
	await act(async () => {
		fireEvent.keyDown(box(), { key: "Enter" });
	});
}

function calls(): { frame: Frame; timeout: unknown }[] {
	return cmd.mock.calls.map((c) => ({ frame: c[1] as Frame, timeout: c[2] }));
}

function framesOf(t: string): Frame[] {
	return calls()
		.map((c) => c.frame)
		.filter((f) => f.type === t);
}

function notices(): string[] {
	return getThreadStore(A)
		.getState()
		.notices.map((n) => n.text);
}

async function attachFile() {
	upload.mockResolvedValue({
		id: "att_1",
		name: "spec.txt",
		size: 3,
		mime: "text/plain",
	});
	const input = screen.getByTestId("attach-file-input");
	await act(async () => {
		fireEvent.change(input, {
			target: {
				files: [new File(["abc"], "spec.txt", { type: "text/plain" })],
			},
		});
	});
	await vi.waitFor(() =>
		expect(screen.getByText("spec.txt")).toBeInTheDocument(),
	);
}

describe("Composer 斜杠命令", () => {
	beforeEach(() => {
		cmd.mockReset();
		upload.mockReset();
		resetThreadStores();
		resetComposerDrafts();
		uiStore.setState({ dialog: null, planReviewNonce: 0 });
	});

	it("M1：agent 忙时非客户端命令仍发 prompt（带 steer 行为），不发 steer / follow_up", async () => {
		reply();
		getThreadStore(A).getState().patchLocal({ running: true });
		mount();
		await send("/effort high");
		expect(framesOf("steer")).toHaveLength(0);
		expect(framesOf("prompt")).toEqual([
			{ type: "prompt", message: "/effort high", streamingBehavior: "steer" },
		]);
		expect(box().value).toBe("");
	});

	it("M1：Ctrl+Enter（排队）时 prompt 的 streamingBehavior 为 followUp", async () => {
		reply();
		getThreadStore(A).getState().patchLocal({ running: true });
		mount();
		fireEvent.change(box(), { target: { value: "/jobs kill bg_1" } });
		fireEvent.keyDown(box(), { key: "Escape" });
		await act(async () => {
			fireEvent.keyDown(box(), { key: "Enter", ctrlKey: true });
		});
		expect(framesOf("follow_up")).toHaveLength(0);
		expect(framesOf("prompt")).toEqual([
			{
				type: "prompt",
				message: "/jobs kill bg_1",
				streamingBehavior: "followUp",
			},
		]);
	});

	it("M4：应答生成本地提示（没有可重试的回合、已派发 tan、已移除…）", async () => {
		reply({
			retry: () => ({ retried: false }),
			tan: () => ({ jobId: "bg_7" }),
			shake_context: () => ({
				removed: "Shook 3 tool results (~1200 tokens freed).",
			}),
			handoff: () => null,
			clear_context: () => ({ cleared: true, droppedCount: 12 }),
		});
		mount();
		await send("/retry");
		await send("/tan 修 lint");
		await send("/shake");
		await send("/handoff");
		await send("/clear");
		expect(notices()).toEqual([
			i18n.t("slash.result.nothingToRetry"),
			i18n.t("slash.result.tan", { id: "bg_7" }),
			i18n.t("slash.result.shake", {
				summary: "Shook 3 tool results (~1200 tokens freed).",
			}),
			i18n.t("slash.result.handoffCancelled"),
			i18n.t("slash.result.cleared", { count: 12 }),
		]);
	});

	it("M5：compact / handoff / omfg / guided-goal 与文本命令传 10 分钟超时", async () => {
		reply({ omfg: () => ({ state: "aborted" }) });
		mount();
		await send("/compact");
		await send("/handoff 交给下一位");
		await send("/omfg 别改测试");
		await send("/guided-goal");
		await send("/effort high");
		const timeoutOf = (t: string) =>
			calls().find((c) => c.frame.type === t)?.timeout;
		for (const t of ["compact", "handoff", "omfg", "guided_goal", "prompt"])
			expect(timeoutOf(t)).toBe(600_000);
		// 普通命令仍用默认超时
		await send("/fresh");
		expect(timeoutOf("fresh")).toBeUndefined();
	});

	it("M12.2：omfg 应答的 data.oryn → 已同步 / 只在 orb（原因填进 {{why}}）", async () => {
		const answers = [
			{
				state: "saved",
				savedPath: "~/.oryn/project-config/.omp/rules/x.md",
				oryn: { synced: true, scope: "project", path: "rules/x.md" },
			},
			{
				state: "saved",
				savedPath: "~/.omp/agent/rules/x.md",
				oryn: { synced: false, reason: "scope_unconfirmed", scope: "global" },
			},
		];
		reply({ omfg: () => answers.shift() });
		mount();
		await send("/omfg 别改测试");
		await send("/omfg 别改测试");
		expect(notices()).toEqual([
			"规则已保存到本项目的 Agent 配置（rules/x.md）。当前线程立即生效，其它线程在新建 orb 或下次唤醒时生效。",
			"规则只保存在当前 orb（~/.omp/agent/rules/x.md），没有写入 Oryn 中央配置：没有收到你对「全局」的选择。可以重新执行 /omfg，或到 Agent 配置里手动添加。",
		]);
	});

	it("M6：不带附件的命令执行后附件留在输入区并提示", async () => {
		reply({ tan: () => ({ jobId: "bg_2" }) });
		mount();
		await attachFile();
		await send("/tan 顺便修 lint");
		expect(framesOf("tan")).toEqual([{ type: "tan", work: "顺便修 lint" }]);
		expect(box().value).toBe("");
		expect(screen.getByText("spec.txt")).toBeInTheDocument();
		expect(screen.getByRole("status")).toHaveTextContent(
			i18n.t("slash.attachmentsKept", { name: "/tan" }),
		);
	});

	it("M6：/plan <prompt> 进入计划模式后把 prompt 连同附件发出，附件随之清空", async () => {
		reply({ set_plan_mode: (f) => ({ enabled: f.enabled }) });
		mount();
		await attachFile();
		await send("/plan 重构登录");
		const order = calls().map((c) => c.frame.type);
		expect(order.indexOf("set_plan_mode")).toBeLessThan(
			order.indexOf("prompt"),
		);
		expect(framesOf("prompt")).toEqual([
			{
				type: "prompt",
				message: "重构登录",
				streamingBehavior: "steer",
				orynAttachments: ["att_1"],
			},
		]);
		await vi.waitFor(() => expect(screen.queryByText("spec.txt")).toBeNull());
	});

	it("/queue 以 follow_up 发出并带附件", async () => {
		reply();
		mount();
		await attachFile();
		await send("/queue 之后看看");
		expect(framesOf("follow_up")).toEqual([
			{ type: "follow_up", message: "之后看看", orynAttachments: ["att_1"] },
		]);
	});

	it("/model <选择器>：对上唯一模型直接切换；对不上打开模型对话框并预填搜索", async () => {
		reply({
			get_available_models: () => ({
				models: [
					{
						provider: "anthropic",
						id: "claude-sonnet-4-5",
						name: "Claude Sonnet 4.5",
					},
					{ provider: "openai", id: "gpt-5", name: "GPT-5" },
				],
			}),
		});
		mount();
		await send("/model sonnet");
		expect(framesOf("oryn_select_model")).toEqual([
			{
				type: "oryn_select_model",
				provider: "anthropic",
				modelId: "claude-sonnet-4-5",
			},
		]);
		expect(notices()).toContain(
			i18n.t("slash.result.model", { model: "Claude Sonnet 4.5" }),
		);
		await send("/model llama");
		expect(uiStore.getState().dialog).toEqual({
			kind: "model",
			query: "llama",
		});
	});

	describe("应答晚于切线程：旧线程的命令不动新线程的对话框", () => {
		const B = "T-0193a1b2-0000-7000-8000-0000000000f2";

		/**
		 * 挂上 A。`switchToB`：与 ThreadPage 一样按线程给输入区 key，切线程即卸载 A 的输入区；
		 * `remountA`：跨 compact 断点时 A 的输入区换一棵子树重新挂载，仍在 A。
		 */
		function mountSwitchable() {
			const client = new QueryClient({
				defaultOptions: { queries: { retry: false } },
			});
			const tree = (tid: string, key = tid) => (
				<QueryClientProvider client={client}>
					<MemoryRouter>
						<Composer key={key} threadId={tid} />
					</MemoryRouter>
				</QueryClientProvider>
			);
			const r = render(tree(A));
			return {
				switchToB: () => r.rerender(tree(B)),
				remountA: () => r.rerender(tree(A, `${A}-compact`)),
			};
		}

		/** 指定类型的命令挂起，直到测试手动放行。 */
		function holdReply(type: string, value: unknown) {
			let release: () => void = () => {};
			const gate = new Promise<void>((res) => {
				release = res;
			});
			reply({ [type]: () => gate.then(() => value) });
			return () => act(async () => release());
		}

		it("/btw <问题>：留在 A 时打开 btw；切到 B 后才应答则不打开、B 的对话框不变", async () => {
			reply();
			const { switchToB } = mountSwitchable();
			await send("/btw 第一个问题");
			expect(uiStore.getState().dialog).toEqual({ kind: "btw" });
			uiStore.getState().close();

			const release = holdReply("btw", {});
			await send("/btw 第二个问题");
			expect(framesOf("btw")).toHaveLength(2);
			switchToB();
			uiStore.getState().open({ kind: "goal" });
			await release();
			expect(uiStore.getState().dialog).toEqual({ kind: "goal" });
		});

		it("/btw <问题>：应答到达前输入区跨断点重新挂载，仍在 A，照常打开 btw", async () => {
			const release = holdReply("btw", {});
			const { remountA } = mountSwitchable();
			await send("/btw 问题");
			remountA();
			await release();
			expect(uiStore.getState().dialog).toEqual({ kind: "btw" });
		});

		it("/model <选择器>：对不上的应答在切到 B 后才到，不在 B 打开模型对话框", async () => {
			const release = holdReply("get_available_models", {
				models: [{ provider: "openai", id: "gpt-5", name: "GPT-5" }],
			});
			const { switchToB } = mountSwitchable();
			await send("/model llama");
			switchToB();
			uiStore.getState().open({ kind: "goal" });
			await release();
			expect(uiStore.getState().dialog).toEqual({ kind: "goal" });
		});
	});

	it("/plan-review：没有提案时说明原因，有提案时请求重新打开", async () => {
		reply();
		mount();
		await send("/plan-review");
		expect(screen.getByRole("alert")).toHaveTextContent(i18n.t("slash.noPlan"));
		expect(uiStore.getState().planReviewNonce).toBe(0);
		getThreadStore(A)
			.getState()
			.patchLocal({
				planProposal: {
					type: "plan_proposal",
					title: "P",
					planContent: "x",
					planFilePath: "/p.md",
					suggestedFileName: "p.md",
				} as never,
			});
		await send("/plan-review");
		expect(uiStore.getState().planReviewNonce).toBe(1);
	});

	it("/git 打开右栏改动面板，/agents 打开信息面板并说明还没有子 agent", async () => {
		reply();
		mount();
		await send("/git");
		expect(uiStore.getState()).toMatchObject({
			panelOpen: true,
			panelTab: "changes",
		});
		await send("/agents");
		expect(uiStore.getState().panelTab).toBe("info");
		expect(notices()).toEqual([i18n.t("slash.result.noSubagents")]);
	});

	it("/move 被拦下，说明原因，命令不发出", async () => {
		reply();
		mount();
		await send("/move ../other");
		expect(cmd).not.toHaveBeenCalled();
		expect(screen.getByRole("alert")).toHaveTextContent(
			i18n.t("slash.unavailable.workspace", { name: "move" }),
		);
		expect(box().value).toBe("/move ../other");
	});

	it("/session delete 先行拦下：命令不发出，提示指向线程菜单的删除 / 归档", async () => {
		reply();
		mount();
		await send("/session delete");
		expect(cmd).not.toHaveBeenCalled();
		expect(screen.getByRole("alert")).toHaveTextContent(
			i18n.t("slash.unavailable.sessionDelete"),
		);
		expect(box().value).toBe("/session delete");
	});

	it("/fast foo 报用法，不切换", async () => {
		reply();
		mount();
		await send("/fast foo");
		expect(cmd).not.toHaveBeenCalled();
		expect(screen.getByRole("alert")).toHaveTextContent(
			i18n.t("slash.badArgs", {
				name: "fast",
				arg: "foo",
				usage: "/fast [on|ultra|off|status]",
			}),
		);
	});
});
