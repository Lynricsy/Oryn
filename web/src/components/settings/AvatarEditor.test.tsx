/**
 * 头像编辑器的解码代次：只有最新一次选图、且编辑器仍挂载时完成的解码才进入裁剪对话框；过期 / 卸载后完成的源图被释放，
 * 过期的失败不显示错误；替换仍开着的源时取景框按新图重新初始化。解码用手动 resolve / reject 的 Promise 控制先后。
 */
import { act, fireEvent, render, screen, within } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import {
	type AvatarSource,
	loadAvatarSource,
	renderAvatar,
} from "../../lib/avatar-image";
import { AvatarEditor } from "./AvatarEditor";

vi.mock("../../lib/avatar-image", async (orig) => ({
	...(await orig<object>()),
	loadAvatarSource: vi.fn(),
	renderAvatar: vi.fn(),
}));

interface Decode {
	resolve(source: AvatarSource): void;
	reject(err: unknown): void;
}

let decodes: Decode[];

function source(width: number, height: number): AvatarSource {
	return {
		image: {} as CanvasImageSource,
		width,
		height,
		url: `blob:${width}x${height}`,
		release: vi.fn(),
	};
}

beforeEach(() => {
	decodes = [];
	// 手动控制的解码（tsconfig 的 lib 不含 Promise.withResolvers）
	vi.mocked(loadAvatarSource)
		.mockReset()
		.mockImplementation(
			() =>
				new Promise<AvatarSource>((resolve, reject) => {
					decodes.push({ resolve, reject });
				}),
		);
	vi.mocked(renderAvatar)
		.mockReset()
		.mockReturnValue("data:image/webp;base64,OUT");
});

function mount() {
	const onChange = vi.fn(async () => true);
	const view = render(
		<AvatarEditor
			value={undefined}
			busy={false}
			resetLabel="恢复"
			onChange={onChange}
		/>,
	);
	return { onChange, view };
}

function pick(name: string) {
	const input = document.querySelector<HTMLInputElement>('input[type="file"]');
	if (!input) throw new Error("没有上传入口");
	fireEvent.change(input, {
		target: { files: [new File(["x"], name, { type: "image/png" })] },
	});
}

/** 在 act 里完成第 i 次解码（含后续微任务）。 */
const settle = (i: number, fn: (d: Decode) => void) =>
	act(async () => {
		const d = decodes[i];
		if (!d) throw new Error(`没有第 ${i} 次解码`);
		fn(d);
	});

const dialog = () => screen.getByRole("dialog", { name: "裁剪头像" });
const cropOf = () =>
	within(dialog()).getByTestId("crop-frame").getAttribute("data-crop");

describe("AvatarEditor 解码代次", () => {
	it("先选 A 再选 B，B 先完成、A 后完成：对话框与 renderAvatar 始终用 B，A 被释放", async () => {
		const { onChange } = mount();
		pick("a.png");
		pick("b.png");
		const a = source(100, 100);
		const b = source(400, 200);
		await settle(1, (d) => d.resolve(b));
		expect(cropOf()).toBe("100,0,200");
		await settle(0, (d) => d.resolve(a));
		expect(a.release).toHaveBeenCalled();
		expect(cropOf()).toBe("100,0,200");
		fireEvent.click(within(dialog()).getByRole("button", { name: "使用" }));
		expect(vi.mocked(renderAvatar).mock.calls).toEqual([
			[b, { x: 100, y: 0, size: 200 }],
		]);
		expect(onChange).toHaveBeenCalledWith({
			avatarImage: "data:image/webp;base64,OUT",
		});
	});

	it("旧请求晚到的失败不显示错误，也不关掉新图的对话框", async () => {
		mount();
		pick("a.png");
		pick("b.png");
		await settle(1, (d) => d.resolve(source(400, 200)));
		await settle(0, (d) => d.reject(new Error("bad")));
		expect(screen.queryByRole("alert")).toBeNull();
		expect(dialog()).toBeInTheDocument();
	});

	it("卸载后才完成的解码：源图被释放", async () => {
		const { view } = mount();
		pick("a.png");
		view.unmount();
		const a = source(400, 200);
		await settle(0, (d) => d.resolve(a));
		expect(a.release).toHaveBeenCalled();
	});

	it("对话框开着时换源：旧源释放，取景框按新图重新初始化", async () => {
		mount();
		pick("a.png");
		const a = source(400, 200);
		await settle(0, (d) => d.resolve(a));
		fireEvent.keyDown(within(dialog()).getByTestId("crop-frame"), {
			key: "ArrowRight",
		});
		expect(cropOf()).toBe("102,0,200");
		pick("b.png");
		const b = source(100, 100);
		await settle(1, (d) => d.resolve(b));
		expect(a.release).toHaveBeenCalled();
		expect(cropOf()).toBe("0,0,100");
		fireEvent.click(within(dialog()).getByRole("button", { name: "使用" }));
		expect(vi.mocked(renderAvatar).mock.calls).toEqual([
			[b, { x: 0, y: 0, size: 100 }],
		]);
	});
});
