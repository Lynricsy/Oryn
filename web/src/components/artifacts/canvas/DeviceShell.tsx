/**
 * 设计画布的设备外壳（M29 R1）：给原型预览里的画框套上手机、平板或浏览器边框。
 * 颜色与圆角全部使用应用设计令牌（见 `docs/design/web-guidelines.md`），不写死颜色字面量；
 * `none` 直接透传 children，不加额外包装。
 */
import type { ReactNode } from "react";
import type { Device } from "../../../lib/artifacts/canvas-doc";

/** 各种设备外壳相对内容区多出来的宽高（边框 + 顶底栏 / 左右边框，单位 CSS px）。 */
export interface ShellChrome {
	extraW: number;
	extraH: number;
}

export const SHELL_CHROME: Record<Device, ShellChrome> = {
	none: { extraW: 0, extraH: 0 },
	// 2px*2 边框 + 8px*2 左右边距 = 20；2px*2 边框 + 28px 顶栏 + 20px 底栏 = 52
	phone: { extraW: 20, extraH: 52 },
	// 2px*2 边框 + 12px*2 左右边距 = 28；2px*2 边框 + 24px 顶栏 + 16px 底栏 = 44
	tablet: { extraW: 28, extraH: 44 },
	// 1px*2 边框 = 2；1px*2 边框 + 36px 标题栏 = 38
	browser: { extraW: 2, extraH: 38 },
};

/** 给定内容区宽高，算出套上设备外壳后的整体外框尺寸（供 `fitScale` 与 `ScaledPage` 用）。 */
export function deviceShellSize(
	device: Device,
	width: number,
	height: number,
): { width: number; height: number } {
	const chrome = SHELL_CHROME[device];
	return { width: width + chrome.extraW, height: height + chrome.extraH };
}

export interface DeviceShellProps {
	device?: Device;
	/** 浏览器外壳地址条显示的当前帧名。 */
	title?: string;
	/** 固定内容区宽高（px）；省略时随父容器自适应，不裁剪内容。 */
	width?: number;
	height?: number;
	children: ReactNode;
}

export function DeviceShell({
	device = "none",
	title,
	width,
	height,
	children,
}: DeviceShellProps): ReactNode {
	if (device === "none") return <>{children}</>;

	const fixed = width !== undefined && height !== undefined;
	const outerStyle = fixed ? deviceShellSize(device, width, height) : undefined;
	const screenStyle = fixed ? { width, height } : undefined;

	const outerClass =
		device === "phone"
			? "box-border inline-flex max-w-full flex-col rounded-xl border-2 border-border-strong bg-panel px-2 shadow-lg"
			: device === "tablet"
				? "box-border inline-flex max-w-full flex-col rounded-xl border-2 border-border-strong bg-panel px-3 shadow-lg"
				: "box-border inline-flex max-w-full flex-col overflow-hidden rounded-lg border border-border-strong bg-panel shadow-lg";

	const screenSizeClass = fixed ? "shrink-0" : "min-h-0 min-w-0 flex-1";
	const screenClass =
		device === "phone"
			? `${screenSizeClass} overflow-hidden rounded-lg bg-inset ring-1 ring-border`
			: device === "tablet"
				? `${screenSizeClass} overflow-hidden rounded-md bg-inset ring-1 ring-border`
				: `${screenSizeClass} bg-inset`;

	return (
		<div
			data-testid="device-shell"
			data-device={device}
			className={outerClass}
			style={outerStyle}
		>
			{device === "phone" ? (
				<div
					aria-hidden="true"
					data-testid="device-shell-status"
					className="flex h-7 shrink-0 items-center justify-center"
				>
					<span className="h-1.5 w-14 rounded-full bg-border-strong" />
				</div>
			) : device === "tablet" ? (
				<div
					aria-hidden="true"
					data-testid="device-shell-camera"
					className="flex h-6 shrink-0 items-center justify-center"
				>
					<span className="h-2 w-2 rounded-full bg-border-strong" />
				</div>
			) : (
				<div
					data-testid="device-shell-chrome"
					className="flex h-9 shrink-0 items-center gap-2 border-border border-b bg-raised px-3"
				>
					<div
						aria-hidden="true"
						className="flex shrink-0 items-center gap-1.5"
					>
						<span className="h-2.5 w-2.5 rounded-full bg-border-strong" />
						<span className="h-2.5 w-2.5 rounded-full bg-border-strong" />
						<span className="h-2.5 w-2.5 rounded-full bg-border-strong" />
					</div>
					<div
						data-testid="device-shell-address"
						className="flex h-6 min-w-0 flex-1 items-center justify-center rounded-md border border-border bg-inset px-2.5 text-muted text-xs"
					>
						<span className="truncate">{title ?? ""}</span>
					</div>
					<span aria-hidden="true" className="w-6 shrink-0" />
				</div>
			)}

			<div
				data-testid="device-shell-screen"
				className={screenClass}
				style={screenStyle}
			>
				{children}
			</div>

			{device === "phone" ? (
				<div
					aria-hidden="true"
					data-testid="device-shell-home"
					className="flex h-5 shrink-0 items-center justify-center"
				>
					<span className="h-1 w-28 max-w-[40%] rounded-full bg-border-strong" />
				</div>
			) : device === "tablet" ? (
				<div aria-hidden="true" className="h-4 shrink-0" />
			) : null}
		</div>
	);
}
