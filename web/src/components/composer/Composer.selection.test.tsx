/**
 * 输入区的选中元素条（M14 3.5）：有选中时引用条显示「标题 › 锚点」，发出的正文是用户原文 + `selectionPrompt` 的数据块；
 * 移除后发原文；斜杠命令与 `!` 命令不带也不清掉它；发送失败保留；与引用回复同时存在时引用块在最前；
 * 空输入框里按退格先移除引用、再移除选中。
 */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, fireEvent, render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { beforeEach, describe, expect, it, vi } from "vitest";
import i18n from "../../i18n";
import {
	type ArtifactSelectionRef,
	MARK_PREFIX,
} from "../../lib/artifacts/prompt";
import {
	getComposerDraft,
	patchComposerDraft,
	resetComposerDrafts,
} from "../../state/composer-drafts";
import { resetThreadStores } from "../../state/stores";
import { Composer } from "./Composer";

const { cmd } = vi.hoisted(() => ({ cmd: vi.fn() }));
vi.mock("../../realtime/socket", () => ({
	realtime: {
		cmd: (...args: unknown[]) => cmd(...args),
		uiResponse: () => true,
		subscribe: () => () => undefined,
		focus: () => undefined,
	},
	OmpCommandError: class extends Error {},
}));

const A = "T-0193a1b2-0000-7000-8000-0000000000c5";

const SEL: ArtifactSelectionRef = {
	artifactId: "art_01JAAAAAAAAAAAAAAAAAAAAAAA",
	title: "周报",
	version: 3,
	anchor: {
		version: 3,
		path: "index.html",
		page: null,
		offset: 120,
		tag: "h2",
		text: "本周进展",
		missing: false,
	},
};

type Frame = Record<string, unknown>;

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

function framesOf(t: string): Frame[] {
	return cmd.mock.calls.map((c) => c[1] as Frame).filter((f) => f.type === t);
}

function lastMessage(): string {
	return framesOf("prompt").at(-1)?.message as string;
}

/** 正文里的边界行（恰好一对、内容相同）。 */
function marks(message: string): string[] {
	return message.split("\n").filter((l) => l.startsWith(MARK_PREFIX));
}

const chip = () => screen.queryByTestId("composer-artifact-selection");

describe("输入区的选中元素条（3.5）", () => {
	beforeEach(() => {
		cmd.mockReset();
		cmd.mockResolvedValue({});
		resetThreadStores();
		resetComposerDrafts();
	});

	it("引用条显示「标题 › 锚点」；发出的正文以原文开头、带一对边界，成功后清掉选中", async () => {
		patchComposerDraft(A, { artifactSelection: SEL });
		mount();
		expect(chip()?.textContent).toContain("周报 › <h2>“本周进展”");
		await send("把这个标题改短");
		const msg = lastMessage();
		expect(msg.startsWith("把这个标题改短\n\n")).toBe(true);
		const m = marks(msg);
		expect(m).toHaveLength(2);
		expect(m[0]).toBe(m[1]);
		// 页面来的文本只在两行边界之间
		const inside = msg.slice(msg.indexOf(m[0] as string));
		expect(inside).toContain('artifact: "周报"');
		expect(inside).toContain('index.html, <h2> at offset 120: "本周进展"');
		expect(msg.slice(0, msg.indexOf(m[0] as string))).not.toContain("本周进展");
		expect(chip()).toBeNull();
		expect(getComposerDraft(A).artifactSelection).toBeNull();
		expect(box().value).toBe("");
	});

	it("移除引用条后发出的就是原文", async () => {
		patchComposerDraft(A, { artifactSelection: SEL });
		mount();
		fireEvent.click(
			screen.getByRole("button", {
				name: i18n.t("artifacts.selection.remove"),
			}),
		);
		expect(chip()).toBeNull();
		await send("只是聊聊");
		expect(lastMessage()).toBe("只是聊聊");
	});

	it("斜杠命令与 ! 命令不带选中，也不清掉它", async () => {
		patchComposerDraft(A, { artifactSelection: SEL });
		mount();
		await send("/compact");
		expect(cmd).toHaveBeenCalled();
		expect(JSON.stringify(cmd.mock.calls)).not.toContain(MARK_PREFIX);
		await send("!ls");
		expect(framesOf("bash").at(-1)).toMatchObject({ command: "ls" });
		expect(JSON.stringify(cmd.mock.calls)).not.toContain(MARK_PREFIX);
		expect(getComposerDraft(A).artifactSelection).toBe(SEL);
		expect(chip()).not.toBeNull();
	});

	it("发送失败保留选中与原文", async () => {
		cmd.mockRejectedValue(new Error("boom"));
		patchComposerDraft(A, { artifactSelection: SEL });
		mount();
		await send("改一下");
		expect(marks(lastMessage())).toHaveLength(2);
		expect(getComposerDraft(A).artifactSelection).toBe(SEL);
		expect(chip()).not.toBeNull();
		expect(box().value).toBe("改一下");
		expect(screen.getByRole("alert").textContent).toContain("boom");
	});

	it("同时有引用回复：引用块在最前，然后是原文，最后是选中的数据块", async () => {
		patchComposerDraft(A, {
			quote: { text: "agent 说的话" },
			artifactSelection: SEL,
		});
		mount();
		await send("回复");
		const msg = lastMessage();
		expect(msg.startsWith("> agent 说的话\n\n回复\n\n")).toBe(true);
		expect(marks(msg)).toHaveLength(2);
		expect(getComposerDraft(A).quote).toBeNull();
		expect(getComposerDraft(A).artifactSelection).toBeNull();
	});

	it("空输入框里按退格：先移除引用，再移除选中", () => {
		patchComposerDraft(A, {
			quote: { text: "q" },
			artifactSelection: SEL,
		});
		mount();
		fireEvent.keyDown(box(), { key: "Backspace" });
		expect(getComposerDraft(A).quote).toBeNull();
		expect(chip()).not.toBeNull();
		fireEvent.keyDown(box(), { key: "Backspace" });
		expect(chip()).toBeNull();
	});
});
