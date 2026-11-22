/**
 * computer_record 的转录卡片（M25 3.4）：登记为助手自己的宿主工具、start 一行、存进文件库时内嵌录像
 * （地址取宿主的 libraryFileUrl）、没存进文件库或没有宿主能力时只给电脑上的路径、为人工操作提前结束的提示、出错的调用。
 */
import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import i18n from "../../i18n";
import type zhCN from "../../i18n/locales/zh-CN.json";
import { isComputerTool, resolveToolRenderer } from "../registry";
import { ToolView } from "../ToolView";
import type { ToolResultLike } from "../types";
import { computerRecordRenderer } from "./computer-record";

const PATH = "/home/user/recordings/20261122-101500.mp4";
const FILE_ID = "af_01";

function recording(extra: Record<string, unknown> = {}) {
	return {
		path: PATH,
		bytes: 3 * 1024 * 1024,
		durationMs: 83_400,
		width: 1280,
		height: 800,
		truncated: false,
		stoppedBy: "agent",
		...extra,
	};
}

const stopResult = (details: unknown): ToolResultLike => ({
	content: [{ type: "text", text: `录制已结束，存进了文件库：${PATH}` }],
	details,
});

type DesktopKey = keyof (typeof zhCN)["toolRender"]["desktopRecord"];
type ComputerKey = keyof (typeof zhCN)["toolRender"]["computerRecord"];
const trD = (key: DesktopKey): string =>
	i18n.t(`toolRender.desktopRecord.${key}`);
const trC = (key: ComputerKey): string =>
	i18n.t(`toolRender.computerRecord.${key}`);

describe("computer_record 渲染器", () => {
	it("登记到注册表，按自己的名字显示（不是遥控工具）", () => {
		expect(resolveToolRenderer("computer_record")).toBe(computerRecordRenderer);
		expect(isComputerTool("computer_record")).toBe(false);
	});

	it("start：一行「开始录制桌面」，展开没有视频", () => {
		const libraryFileUrl = vi.fn(() => "/x");
		render(
			<ToolView
				name="computer_record"
				args={{ action: "start" }}
				result={{
					content: [{ type: "text", text: `已开始录制，视频写到 ${PATH}。` }],
					details: { path: PATH },
				}}
				defaultOpen
				host={{ libraryFileUrl }}
			/>,
		);
		expect(document.body).toHaveTextContent(trD("start"));
		expect(screen.queryByTestId("record-video")).toBeNull();
		expect(document.body).not.toHaveTextContent(trC("onComputer"));
		expect(libraryFileUrl).not.toHaveBeenCalled();
	});

	it("存进文件库：内嵌 video 的 src 取 host.libraryFileUrl(id)，preload 为 none", () => {
		const url = `/api/v1/assistant/files/${FILE_ID}/content`;
		const libraryFileUrl = vi.fn(() => url);
		render(
			<ToolView
				name="computer_record"
				args={{ action: "stop" }}
				result={stopResult({ recording: recording(), libraryFileId: FILE_ID })}
				defaultOpen
				host={{ libraryFileUrl }}
			/>,
		);
		expect(libraryFileUrl).toHaveBeenCalledWith(FILE_ID);
		const video = screen.getByTestId("record-video");
		expect(video.tagName).toBe("VIDEO");
		expect(video).toHaveAttribute("src", url);
		expect(video).toHaveAttribute("preload", "none");
		expect(video).toHaveAttribute("controls");
		expect(document.body).toHaveTextContent("1:23");
		expect(document.body).toHaveTextContent("3.0 MB");
		expect(document.body).toHaveTextContent("1280×800");
		expect(document.body).not.toHaveTextContent(trC("onComputer"));
	});

	it("视频加载失败时显示错误文字", () => {
		render(
			<ToolView
				name="computer_record"
				args={{ action: "stop" }}
				result={stopResult({ recording: recording(), libraryFileId: FILE_ID })}
				defaultOpen
				host={{ libraryFileUrl: () => "/v.mp4" }}
			/>,
		);
		fireEvent.error(screen.getByTestId("record-video"));
		expect(screen.queryByTestId("record-video")).toBeNull();
		expect(document.body).toHaveTextContent(trC("loadFailed"));
	});

	it("没有 libraryFileId（超过上限没存）或没有宿主能力（分享页）时只显示电脑上的路径", () => {
		const cases = [
			{
				details: { recording: recording() },
				host: { libraryFileUrl: vi.fn(() => "/x") },
			},
			{
				details: { recording: recording(), libraryFileId: FILE_ID },
				host: undefined,
			},
		];
		for (const { details, host } of cases) {
			const { unmount } = render(
				<ToolView
					name="computer_record"
					args={{ action: "stop" }}
					result={stopResult(details)}
					defaultOpen
					host={host}
				/>,
			);
			expect(screen.queryByTestId("record-video")).toBeNull();
			expect(document.body).toHaveTextContent(trC("onComputer"));
			expect(document.body).toHaveTextContent("20261122-101500.mp4");
			if (host) expect(host.libraryFileUrl).not.toHaveBeenCalled();
			unmount();
		}
	});

	it("stoppedBy=control 时提示录制为人工操作提前结束", () => {
		render(
			<ToolView
				name="computer_record"
				args={{ action: "stop" }}
				result={stopResult({
					recording: recording({ stoppedBy: "control", truncated: true }),
					libraryFileId: FILE_ID,
				})}
				defaultOpen
				host={{ libraryFileUrl: () => "/v.mp4" }}
			/>,
		);
		expect(document.body).toHaveTextContent(trD("stoppedByControl"));
		expect(document.body).toHaveTextContent(trD("truncated"));
		expect(document.body).not.toHaveTextContent(trD("stoppedByLimit"));
	});

	it("出错的调用显示 details.error.message，没有就用结果文本，不渲染视频", () => {
		const { unmount } = render(
			<ToolView
				name="computer_record"
				args={{ action: "stop" }}
				result={{
					content: [{ type: "text", text: "现在没有在录制" }],
					isError: true,
					details: {
						tool: "computer_record",
						error: { code: "not_recording", message: "no recording" },
					},
				}}
				defaultOpen
				host={{ libraryFileUrl: () => "/v.mp4" }}
			/>,
		);
		expect(document.querySelector(".tv-card--error")).not.toBeNull();
		expect(document.body).toHaveTextContent("no recording");
		expect(screen.queryByTestId("record-video")).toBeNull();
		unmount();
		render(
			<ToolView
				name="computer_record"
				args={{ action: "start" }}
				result={{
					content: [{ type: "text", text: "用户正在操作桌面" }],
					isError: true,
				}}
				defaultOpen
			/>,
		);
		expect(document.querySelector(".tv-card--error")).not.toBeNull();
		expect(document.body).toHaveTextContent(trD("start"));
		expect(document.body).toHaveTextContent("用户正在操作桌面");
	});
});
