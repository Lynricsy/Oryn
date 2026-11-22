/**
 * desktop_record 的转录卡片（M25 3.3）：start 一行、stop 内嵌录像（地址取宿主的 orbFileUrl）、未正常收尾的提示、
 * 没有宿主能力（分享页）或 orb 已删除时只给路径、视频加载失败、出错的调用。
 */
import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import i18n from "../../i18n";
import type zhCN from "../../i18n/locales/zh-CN.json";
import { resolveToolRenderer } from "../registry";
import { ToolView } from "../ToolView";
import type { ToolResultLike } from "../types";
import { desktopRecordRenderer, formatClock } from "./desktop-record";

const PATH = "/home/user/recordings/20261122-101500.mp4";

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
	content: [{ type: "text", text: `录制已结束：${PATH}` }],
	details,
});

type RecordKey = keyof (typeof zhCN)["toolRender"]["desktopRecord"];
const tr = (key: RecordKey): string =>
	i18n.t(`toolRender.desktopRecord.${key}`);

describe("desktop_record 渲染器", () => {
	it("登记到注册表", () => {
		expect(resolveToolRenderer("desktop_record")).toBe(desktopRecordRenderer);
	});

	it("时长按 m:ss", () => {
		expect(formatClock(83_400)).toBe("1:23");
		expect(formatClock(600_000)).toBe("10:00");
		expect(formatClock(400)).toBe("0:00");
	});

	it("start：一行「开始录制桌面」，展开没有视频", () => {
		render(
			<ToolView
				name="desktop_record"
				args={{ action: "start" }}
				result={{
					content: [
						{ type: "text", text: `已开始录制桌面，视频写到 ${PATH}。` },
					],
				}}
				defaultOpen
				host={{ orbFileUrl: () => "/x" }}
			/>,
		);
		expect(document.body).toHaveTextContent(tr("start"));
		expect(screen.queryByTestId("record-video")).toBeNull();
	});

	it("stop：标题有时长与大小，内嵌 video 的 src 取 host.orbFileUrl", () => {
		const url = "/api/v1/orbs/orb-1/fs/file?thread=T-1&path=x.mp4";
		const orbFileUrl = vi.fn(() => url);
		render(
			<ToolView
				name="desktop_record"
				args={{ action: "stop" }}
				result={stopResult(recording())}
				defaultOpen
				host={{ orbFileUrl }}
			/>,
		);
		expect(orbFileUrl).toHaveBeenCalledWith(PATH);
		const video = screen.getByTestId("record-video");
		expect(video.tagName).toBe("VIDEO");
		expect(video).toHaveAttribute("src", url);
		// 取视频会唤醒冻结的 orb：只在用户点播放时才请求
		expect(video).toHaveAttribute("preload", "none");
		expect(video).toHaveAttribute("controls");
		expect(document.body).toHaveTextContent("1:23");
		expect(document.body).toHaveTextContent("3.0 MB");
		expect(document.body).toHaveTextContent("1280×800");
		expect(document.body).not.toHaveTextContent(tr("truncated"));
		expect(document.body).not.toHaveTextContent(tr("storedInOrb"));
	});

	it("truncated 与自动停止时给提示", () => {
		const { unmount } = render(
			<ToolView
				name="desktop_record"
				args={{ action: "stop" }}
				result={stopResult(recording({ truncated: true, stoppedBy: "limit" }))}
				defaultOpen
				host={{ orbFileUrl: () => "/v.mp4" }}
			/>,
		);
		expect(document.body).toHaveTextContent(tr("truncated"));
		expect(document.body).toHaveTextContent(tr("incomplete"));
		expect(document.body).toHaveTextContent(tr("stoppedByLimit"));
		unmount();
		const { unmount: unmount2 } = render(
			<ToolView
				name="desktop_record"
				args={{ action: "stop" }}
				result={stopResult(recording({ stoppedBy: "error" }))}
				defaultOpen
			/>,
		);
		expect(document.body).toHaveTextContent(tr("stoppedByError"));
		expect(document.body).not.toHaveTextContent(tr("truncated"));
		unmount2();
		render(
			<ToolView
				name="desktop_record"
				args={{ action: "stop" }}
				result={stopResult(recording({ stoppedBy: "control" }))}
				defaultOpen
			/>,
		);
		expect(document.body).toHaveTextContent(tr("stoppedByControl"));
		expect(document.body).not.toHaveTextContent(tr("stoppedByError"));
	});

	it("没有宿主能力（分享页）或返回 null（orb 已删除）时只显示路径与说明", () => {
		for (const host of [undefined, { orbFileUrl: () => null }]) {
			const { unmount } = render(
				<ToolView
					name="desktop_record"
					args={{ action: "stop" }}
					result={stopResult(recording())}
					defaultOpen
					host={host}
				/>,
			);
			expect(screen.queryByTestId("record-video")).toBeNull();
			expect(document.body).toHaveTextContent(tr("storedInOrb"));
			expect(document.body).toHaveTextContent("20261122-101500.mp4");
			unmount();
		}
	});

	it("视频加载失败时显示错误；有 openOrbFile 时给「在文件中打开」", () => {
		const openOrbFile = vi.fn();
		const { unmount } = render(
			<ToolView
				name="desktop_record"
				args={{ action: "stop" }}
				result={stopResult(recording())}
				defaultOpen
				host={{ orbFileUrl: () => "/v.mp4", openOrbFile }}
			/>,
		);
		fireEvent.error(screen.getByTestId("record-video"));
		expect(screen.queryByTestId("record-video")).toBeNull();
		expect(document.body).toHaveTextContent(tr("loadFailed"));
		fireEvent.click(screen.getByRole("button", { name: tr("openInFiles") }));
		expect(openOrbFile).toHaveBeenCalledWith(PATH);
		unmount();

		render(
			<ToolView
				name="desktop_record"
				args={{ action: "stop" }}
				result={stopResult(recording())}
				defaultOpen
				host={{ orbFileUrl: () => "/v.mp4" }}
			/>,
		);
		fireEvent.error(screen.getByTestId("record-video"));
		expect(document.body).toHaveTextContent(tr("loadFailed"));
		expect(
			screen.queryByRole("button", { name: tr("openInFiles") }),
		).toBeNull();
	});

	it("出错的调用显示 details.error.message，没有就用结果文本，不渲染视频", () => {
		const { unmount } = render(
			<ToolView
				name="desktop_record"
				args={{ action: "stop" }}
				result={{
					content: [{ type: "text", text: "现在没有在录制" }],
					isError: true,
					details: {
						tool: "desktop_record",
						error: { code: "not_recording", message: "no recording" },
					},
				}}
				defaultOpen
				host={{ orbFileUrl: () => "/v.mp4" }}
			/>,
		);
		expect(document.querySelector(".tv-card--error")).not.toBeNull();
		expect(document.body).toHaveTextContent("no recording");
		expect(screen.queryByTestId("record-video")).toBeNull();
		unmount();
		// start 被人工操作挡住（control_user）：同样按出错卡片显示
		const { unmount: unmountStart } = render(
			<ToolView
				name="desktop_record"
				args={{ action: "start" }}
				result={{
					content: [{ type: "text", text: "用户正在操作桌面" }],
					isError: true,
					details: {
						tool: "desktop_record",
						error: { code: "control_user", message: "user has control" },
					},
				}}
				defaultOpen
			/>,
		);
		expect(document.querySelector(".tv-card--error")).not.toBeNull();
		expect(document.body).toHaveTextContent(tr("start"));
		expect(document.body).toHaveTextContent("user has control");
		unmountStart();
		render(
			<ToolView
				name="desktop_record"
				args={{ action: "start" }}
				result={{
					content: [{ type: "text", text: "unavailable: dial failed" }],
					isError: true,
				}}
				defaultOpen
			/>,
		);
		expect(document.body).toHaveTextContent("unavailable: dial failed");
	});
});
