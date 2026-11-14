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
	AVATAR_MAX_BYTES,
	type AvatarSource,
	AvatarTooLargeError,
	loadAvatarSource,
	renderAvatar,
} from "./avatar-image";

interface FakeCanvas {
	width: number;
	height: number;
	ctx: { drawImage: Mock; imageSmoothingQuality: string };
	getContext(kind: string): FakeCanvas["ctx"];
	toDataURL: Mock;
}

let canvas: FakeCanvas;

function fakeCanvas(toDataURL: (type: string, q: number) => string) {
	const ctx = { drawImage: vi.fn(), imageSmoothingQuality: "low" };
	canvas = {
		width: 0,
		height: 0,
		ctx,
		getContext: () => ctx,
		toDataURL: vi.fn(toDataURL),
	};
	const real = document.createElement.bind(document);
	vi.spyOn(document, "createElement").mockImplementation(
		(tag: string, opts?: ElementCreationOptions) =>
			tag === "canvas"
				? (canvas as unknown as HTMLCanvasElement)
				: real(tag, opts),
	);
}

const source = (): AvatarSource => ({
	image: { width: 400, height: 200 } as unknown as CanvasImageSource,
	width: 400,
	height: 200,
	url: "blob:x",
	release: vi.fn(),
});

afterEach(() => {
	vi.restoreAllMocks();
	vi.unstubAllGlobals();
});

describe("renderAvatar", () => {
	it("128×128 画布、drawImage 的源矩形等于取景框", () => {
		fakeCanvas((type) => `data:${type};base64,AAAA`);
		const src = source();
		const url = renderAvatar(src, { x: 200, y: 0, size: 200 });
		expect(url).toBe("data:image/webp;base64,AAAA");
		expect([canvas.width, canvas.height]).toEqual([128, 128]);
		expect(canvas.ctx.drawImage).toHaveBeenCalledWith(
			src.image,
			200,
			0,
			200,
			200,
			0,
			0,
			128,
			128,
		);
	});

	it("浏览器不支持 webp 编码时改用 jpeg", () => {
		fakeCanvas((type) =>
			type === "image/webp"
				? "data:image/png;base64,AAAA"
				: "data:image/jpeg;base64,BBBB",
		);
		expect(renderAvatar(source(), { x: 0, y: 0, size: 200 })).toBe(
			"data:image/jpeg;base64,BBBB",
		);
	});

	it("逐级降质量直到不超过上限", () => {
		fakeCanvas((type, q) =>
			q > 0.6
				? `data:${type};base64,${"A".repeat(AVATAR_MAX_BYTES)}`
				: `data:${type};base64,small`,
		);
		expect(renderAvatar(source(), { x: 0, y: 0, size: 200 })).toBe(
			"data:image/webp;base64,small",
		);
		expect(canvas.toDataURL.mock.calls.map((c) => c[1])).toEqual([
			0.9, 0.8, 0.65, 0.5,
		]);
	});

	it("质量阶梯全部超限时抛 AvatarTooLargeError", () => {
		fakeCanvas((type) => `data:${type};base64,${"A".repeat(AVATAR_MAX_BYTES)}`);
		expect(() => renderAvatar(source(), { x: 0, y: 0, size: 200 })).toThrow(
			AvatarTooLargeError,
		);
	});
});

describe("loadAvatarSource", () => {
	beforeEach(() => {
		vi.stubGlobal(
			"URL",
			Object.assign(Object.create(URL), {
				createObjectURL: vi.fn(() => "blob:avatar"),
				revokeObjectURL: vi.fn(),
			}),
		);
	});

	it("解码成功：带尺寸与预览地址；release 关闭位图并撤销地址，只做一次", async () => {
		const close = vi.fn();
		vi.stubGlobal(
			"createImageBitmap",
			vi.fn(async () => ({ width: 640, height: 480, close })),
		);
		const src = await loadAvatarSource(new Blob(["x"]));
		expect([src.width, src.height, src.url]).toEqual([640, 480, "blob:avatar"]);
		expect(URL.revokeObjectURL).not.toHaveBeenCalled();
		src.release();
		src.release();
		expect(close).toHaveBeenCalledTimes(1);
		expect(URL.revokeObjectURL).toHaveBeenCalledTimes(1);
	});

	it("解码失败：撤销地址并抛出", async () => {
		vi.stubGlobal(
			"createImageBitmap",
			vi.fn(async () => {
				throw new Error("bad image");
			}),
		);
		await expect(loadAvatarSource(new Blob(["x"]))).rejects.toThrow(
			"bad image",
		);
		expect(URL.revokeObjectURL).toHaveBeenCalledWith("blob:avatar");
	});
});
