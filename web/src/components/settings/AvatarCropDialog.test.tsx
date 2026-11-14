import { fireEvent, render, screen } from "@testing-library/react";
import {
	afterEach,
	beforeEach,
	describe,
	expect,
	it,
	type Mock,
	vi,
} from "vitest";
import {
	type AvatarSource,
	AvatarTooLargeError,
	renderAvatar,
} from "../../lib/avatar-image";
import { AvatarCropDialog } from "./AvatarCropDialog";

vi.mock("../../lib/avatar-image", async (orig) => ({
	...(await orig<object>()),
	renderAvatar: vi.fn(() => "data:image/webp;base64,OUT"),
}));

/** 400×200 的横图：初始取景框是中间的 200×200。 */
function source(): AvatarSource {
	return {
		image: {} as CanvasImageSource,
		width: 400,
		height: 200,
		url: "blob:avatar",
		release: vi.fn(),
	};
}

let src: AvatarSource;
let onCancel: Mock<() => void>;
let onConfirm: Mock<(url: string) => void>;

beforeEach(() => {
	src = source();
	onCancel = vi.fn<() => void>();
	onConfirm = vi.fn<(url: string) => void>();
	vi.mocked(renderAvatar)
		.mockReset()
		.mockReturnValue("data:image/webp;base64,OUT");
});

afterEach(() => {
	vi.restoreAllMocks();
});

function mount() {
	render(
		<AvatarCropDialog source={src} onCancel={onCancel} onConfirm={onConfirm} />,
	);
	return screen.getByTestId("crop-frame");
}

const cropOf = () => screen.getByTestId("crop-frame").getAttribute("data-crop");

describe("AvatarCropDialog", () => {
	it("取景框可聚焦、带组名与说明；初始为居中的最大正方形", () => {
		const frame = mount();
		expect(frame).toHaveAttribute("role", "group");
		expect(frame).toHaveAttribute("tabindex", "0");
		expect(frame).toHaveAccessibleName("取景框");
		expect(frame).toHaveAccessibleDescription(/方向键移动/);
		expect(cropOf()).toBe("100,0,200");
		expect(screen.getByRole("dialog", { name: "裁剪头像" })).toBeVisible();
		expect(screen.getByTestId("crop-preview-tile")).toHaveTextContent(
			"方块显示",
		);
		expect(screen.getByTestId("crop-preview-circle")).toHaveTextContent(
			"圆形显示",
		);
	});

	it("方向键移动 1%，Shift 时 10%；+ / - 以中心缩放；0 复位", () => {
		const frame = mount();
		fireEvent.keyDown(frame, { key: "ArrowLeft" });
		expect(cropOf()).toBe("98,0,200");
		fireEvent.keyDown(frame, { key: "ArrowLeft", shiftKey: true });
		expect(cropOf()).toBe("78,0,200");
		fireEvent.keyDown(frame, { key: "ArrowRight" });
		expect(cropOf()).toBe("80,0,200");
		fireEvent.keyDown(frame, { key: "0" });
		expect(cropOf()).toBe("100,0,200");
		fireEvent.keyDown(frame, { key: "+" });
		expect(cropOf()).toBe("110,10,180");
		fireEvent.keyDown(frame, { key: "ArrowDown" });
		expect(cropOf()).toBe("110,12,180");
		fireEvent.keyDown(frame, { key: "-" });
		expect(cropOf()).toBe("100,0,200");
		fireEvent.keyDown(frame, { key: "=" });
		fireEvent.keyDown(frame, { key: "_" });
		expect(cropOf()).toBe("100,0,200");
	});

	it("到边界后继续按键，取景框不再变", () => {
		const frame = mount();
		for (let i = 0; i < 12; i++)
			fireEvent.keyDown(frame, { key: "ArrowRight", shiftKey: true });
		expect(cropOf()).toBe("200,0,200");
		fireEvent.keyDown(frame, { key: "ArrowRight" });
		fireEvent.keyDown(frame, { key: "ArrowUp" });
		fireEvent.keyDown(frame, { key: "-" });
		expect(cropOf()).toBe("200,0,200");
		for (let i = 0; i < 40; i++) fireEvent.keyDown(frame, { key: "+" });
		const smallest = cropOf();
		expect(smallest?.split(",")[2]).toBe("32");
		fireEvent.keyDown(frame, { key: "+" });
		expect(cropOf()).toBe(smallest);
	});

	it("滑杆按倍数缩放（以取景框中心为不动点）", () => {
		mount();
		const slider = screen.getByRole("slider", { name: "缩放" });
		expect(slider).toHaveValue("1");
		fireEvent.change(slider, { target: { value: "2" } });
		expect(cropOf()).toBe("150,50,100");
		expect(slider).toHaveValue("2");
	});

	it("指针拖动取景框、滚轮以指针处为不动点缩放", () => {
		mount();
		const stage = screen.getByTestId("crop-stage");
		// 舞台 320×160 显示 400×200 的图：1 css px = 1.25 源像素
		vi.spyOn(stage, "getBoundingClientRect").mockReturnValue(
			DOMRect.fromRect({ x: 0, y: 0, width: 320, height: 160 }),
		);
		const frame = screen.getByTestId("crop-frame");
		fireEvent.pointerDown(frame, { pointerId: 1, clientX: 100, clientY: 50 });
		fireEvent.pointerMove(stage, { pointerId: 1, clientX: 140, clientY: 50 });
		expect(cropOf()).toBe("150,0,200");
		fireEvent.pointerUp(stage, { pointerId: 1, clientX: 140, clientY: 50 });
		// 松开后再移动不再拖
		fireEvent.pointerMove(stage, { pointerId: 1, clientX: 0, clientY: 50 });
		expect(cropOf()).toBe("150,0,200");
		// 指针在源图 (250, 100)：向上滚一格边长 ×0.9，指针处相对位置不变
		fireEvent.wheel(stage, { deltaY: -100, clientX: 200, clientY: 80 });
		expect(cropOf()).toBe("160,10,180");
	});

	it("确认：renderAvatar 收到当前取景框，onConfirm 收到其返回值，源图被释放", () => {
		const frame = mount();
		fireEvent.keyDown(frame, { key: "ArrowLeft", shiftKey: true });
		fireEvent.click(screen.getByRole("button", { name: "使用" }));
		expect(renderAvatar).toHaveBeenCalledWith(src, {
			x: 80,
			y: 0,
			size: 200,
		});
		expect(onConfirm).toHaveBeenCalledWith("data:image/webp;base64,OUT");
		expect(src.release).toHaveBeenCalled();
		expect(onCancel).not.toHaveBeenCalled();
	});

	it("取消不调 onConfirm，并释放源图", () => {
		mount();
		fireEvent.click(screen.getByRole("button", { name: "取消" }));
		expect(onCancel).toHaveBeenCalledTimes(1);
		expect(onConfirm).not.toHaveBeenCalled();
		expect(renderAvatar).not.toHaveBeenCalled();
		expect(src.release).toHaveBeenCalled();
	});

	it("编码后仍超限：对话框里显示错误、不关闭、不调 onConfirm", () => {
		vi.mocked(renderAvatar).mockImplementationOnce(() => {
			throw new AvatarTooLargeError();
		});
		mount();
		fireEvent.click(screen.getByRole("button", { name: "使用" }));
		expect(screen.getByRole("alert")).toHaveTextContent(
			"图片压缩后仍超过 128 KiB",
		);
		expect(screen.getByRole("dialog")).toBeVisible();
		expect(onConfirm).not.toHaveBeenCalled();
		expect(src.release).not.toHaveBeenCalled();
	});

	it("重置按钮回到初始取景框", () => {
		const frame = mount();
		fireEvent.keyDown(frame, { key: "+" });
		fireEvent.click(screen.getByRole("button", { name: "重置" }));
		expect(cropOf()).toBe("100,0,200");
	});
});
