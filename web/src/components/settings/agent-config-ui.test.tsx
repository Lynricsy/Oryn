/**
 * TextFileDialog：开始编辑时冻结基线；打开后文件在别处被改过时，保存冲突且再点保存仍不覆盖，
 * 只有明确「载入最新内容」后才能在新内容上保存。
 */
import { fireEvent, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import {
	agentFetch,
	home,
	renderPage,
	resetHome,
} from "../../pages/settings/agent/agent-config-mock";
import { TextFileDialog } from "./agent-config-ui";

vi.mock("../editor/CodeEditor", () => ({
	default: ({
		value,
		path,
		onChange,
	}: {
		value: string;
		path: string;
		onChange?(v: string): void;
	}) => (
		<textarea
			aria-label={`editor:${path}`}
			value={value}
			onChange={(e) => onChange?.(e.target.value)}
		/>
	),
}));

beforeEach(() => {
	resetHome({ "AGENTS.md": "原文\n" });
	vi.stubGlobal("fetch", agentFetch);
});

afterEach(() => {
	vi.unstubAllGlobals();
});

it("外部修改后：首次保存冲突，再次保存仍不覆盖；载入最新内容后才写入", async () => {
	renderPage(
		<TextFileDialog path="AGENTS.md" title="AGENTS.md" onClose={() => {}} />,
	);
	const editor = await screen.findByLabelText("editor:AGENTS.md");
	await waitFor(() => expect(editor).toHaveValue("原文\n"));
	fireEvent.change(editor, { target: { value: "我的旧稿\n" } });
	home.files["AGENTS.md"] = "别处改的\n";

	const save = () =>
		fireEvent.click(screen.getByRole("button", { name: "保存" }));
	save();
	expect(await screen.findByText(/打开后文件已在别处修改/)).toBeInTheDocument();
	expect(editor).toHaveValue("我的旧稿\n");
	save();
	await waitFor(() =>
		expect(screen.getByRole("button", { name: "保存" })).toBeEnabled(),
	);
	expect(home.puts).toHaveLength(0);

	fireEvent.click(
		screen.getByRole("button", { name: "载入最新内容（放弃我的改动）" }),
	);
	await waitFor(() => expect(editor).toHaveValue("别处改的\n"));
	fireEvent.change(editor, { target: { value: "合并后\n" } });
	save();
	await waitFor(() =>
		expect(home.puts).toEqual([{ path: "AGENTS.md", body: "合并后\n" }]),
	);
});
