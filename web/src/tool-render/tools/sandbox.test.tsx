/**
 * `sandbox_*` 卡片（M30）：exec 的摘要是命令首行、读数是退出码与耗时，非零退出码按错误呈现；
 * detach 的读数是作业 id；cp 的摘要是 `source → dest`、读数是文件数与字节数；create 的摘要是 `后端 · 网络`；
 * 8 个工具都走这张卡片，图标同 bash。
 */
import { render } from "@testing-library/react";
import { beforeEach, describe, expect, it } from "vitest";
import i18n from "../../i18n";
import { resolveToolRenderer } from "../registry";
import { ToolView } from "../ToolView";
import type { ToolResultLike } from "../types";
import { SANDBOX_TOOLS, sandboxRenderer } from "./sandbox";

const SBX = "sbx_01JD9K3M7Q2W8E5R4T6Y1U0P3A";

function res(
	text: string,
	details: Record<string, unknown>,
	isError = false,
): ToolResultLike {
	return { content: [{ type: "text", text }], details, isError };
}

const q = (c: HTMLElement, sel: string) => c.querySelector(sel);

beforeEach(() => {
	i18n.changeLanguage("zh-CN");
});

describe("sandbox 卡片", () => {
	it("8 个工具都注册到沙箱渲染器", () => {
		expect(SANDBOX_TOOLS).toHaveLength(8);
		for (const name of SANDBOX_TOOLS)
			expect(resolveToolRenderer(name)).toBe(sandboxRenderer);
	});

	it("exec 成功：摘要是命令首行，读数是 exit 0 与耗时", () => {
		const { container } = render(
			<ToolView
				name="sandbox_exec"
				args={{ sandbox: SBX, command: "\ncd ~/repo\nbun run test" }}
				result={res("ok\nexit 0", {
					tool: "sandbox_exec",
					sandbox: SBX,
					exit: 0,
					durationMs: 18_420,
				})}
			/>,
		);
		const head = q(container, ".tv-head") ?? container;
		expect(head).toHaveTextContent("cd ~/repo");
		expect(head).not.toHaveTextContent("bun run test");
		expect(q(container, ".tv-meta .tv-meta-ok")).toHaveTextContent("exit 0");
		expect(q(container, ".tv-meta")).toHaveTextContent("18s");
	});

	it("exec 非零退出：读数为红色退出码，输出按错误样式", () => {
		const { container } = render(
			<ToolView
				name="sandbox_exec"
				defaultOpen
				args={{ sandbox: SBX, command: "go test ./..." }}
				result={res(
					"--- FAIL: TestX\nexit 2",
					{ tool: "sandbox_exec", sandbox: SBX, exit: 2, durationMs: 7_310 },
					true,
				)}
			/>,
		);
		expect(q(container, ".tv-meta .tv-meta-err")).toHaveTextContent("exit 2");
		expect(container).toHaveTextContent("--- FAIL: TestX");
		expect(q(container, ".tv-cmd")).toHaveTextContent("go test ./...");
	});

	it("exec detach：带 detach 标记，读数是作业 id", () => {
		const { container } = render(
			<ToolView
				name="sandbox_exec"
				args={{ sandbox: SBX, command: "./soak.sh", detach: true }}
				result={res(`Started job j1 in sandbox ${SBX}.`, {
					tool: "sandbox_exec",
					sandbox: SBX,
					jobId: "j1",
				})}
			/>,
		);
		expect(container).toHaveTextContent("detach");
		expect(q(container, ".tv-meta")).toHaveTextContent("作业 j1");
	});

	it("cp：摘要是 source → dest，读数是文件数与字节数", () => {
		const { container } = render(
			<ToolView
				name="sandbox_cp"
				args={{
					sandbox: SBX,
					direction: "to_sandbox",
					source: "/srv/repo",
					dest: "repo",
				}}
				result={res("Copied 214 files (3481190 bytes).", {
					tool: "sandbox_cp",
					files: 214,
					bytes: 3_481_190,
					skipped: 2,
				})}
			/>,
		);
		expect(container).toHaveTextContent("/srv/repo → repo");
		const meta = q(container, ".tv-meta");
		expect(meta).toHaveTextContent("214 个文件");
		expect(meta).toHaveTextContent("3.3 MB");
		expect(meta).toHaveTextContent("跳过 2 项");
	});

	it("create：摘要是后端 · 网络；其余工具是沙箱 id 末 6 位", () => {
		const { container } = render(
			<ToolView
				name="sandbox_create"
				args={{ network: "none" }}
				result={res("ready", {
					tool: "sandbox_create",
					sandbox: { id: SBX, backend: "kvm", network: "none" },
				})}
			/>,
		);
		expect(container).toHaveTextContent("kvm · 断网");
		const other = render(
			<ToolView
				name="sandbox_destroy"
				args={{ sandbox: SBX }}
				result={res(`Destroyed sandbox ${SBX}.`, {})}
			/>,
		);
		expect(other.container).toHaveTextContent("1U0P3A");
		expect(other.container).not.toHaveTextContent("sbx_01JD");
	});
});
