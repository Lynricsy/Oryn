/**
 * 扫码期间的返回键（M7 验收 A22）：全屏扫码时按返回键只取消扫码、App 不退出；
 * 扫码结束（成功 / 取消 / 出错）后监听都要解除，返回键恢复原行为。
 */
import {
	afterEach,
	beforeEach,
	describe,
	expect,
	it,
	type Mock,
	vi,
} from "vitest";
import { scanQr } from "./bridge";

type Handler = (payload: { canGoBack: boolean }) => void;

let backHandler: Handler | null;
let unregister: Mock;
let invoke: Mock;
/** 进行中的 scan 调用的 reject：cancel 时以「cancelled」拒绝，模拟插件行为。 */
let rejectScan: ((e: unknown) => void) | null;

function install(scan: () => Promise<unknown>) {
	backHandler = null;
	rejectScan = null;
	unregister = vi.fn(async () => {
		backHandler = null;
	});
	invoke = vi.fn(async (cmd: string) => {
		switch (cmd) {
			case "plugin:barcode-scanner|check_permissions":
				return { camera: "granted" };
			case "plugin:barcode-scanner|scan":
				return scan();
			case "plugin:barcode-scanner|cancel":
				rejectScan?.("cancelled");
				return null;
			default:
				throw new Error(`unexpected ${cmd}`);
		}
	});
	Object.assign(window, {
		__ORYN_HOST__: { kind: "tauri", platform: "android", bridge: 3 },
		__TAURI__: {
			core: { invoke },
			app: {
				onBackButtonPress: vi.fn(async (h: Handler) => {
					backHandler = h;
					return { unregister };
				}),
			},
		},
	});
}

const waitForScanCall = () =>
	vi.waitFor(() =>
		expect(invoke).toHaveBeenCalledWith(
			"plugin:barcode-scanner|scan",
			expect.anything(),
		),
	);

beforeEach(() => {
	backHandler = null;
});

afterEach(() => {
	Reflect.deleteProperty(window, "__TAURI__");
	Reflect.deleteProperty(window, "__ORYN_HOST__");
});

describe("scanQr 与返回键", () => {
	it("扫码中按返回键：取消扫码，结果为 cancelled，随后解除监听", async () => {
		// tsconfig 的 lib 低于 es2024，没有 Promise.withResolvers
		install(
			() =>
				new Promise((_resolve, reject) => {
					rejectScan = reject;
				}),
		);
		const result = scanQr();
		await waitForScanCall();
		expect(backHandler).not.toBeNull();
		backHandler?.({ canGoBack: false });
		await expect(result).resolves.toEqual({ kind: "cancelled" });
		expect(invoke).toHaveBeenCalledWith(
			"plugin:barcode-scanner|cancel",
			undefined,
		);
		expect(unregister).toHaveBeenCalledTimes(1);
		expect(backHandler).toBeNull();
	});

	it("扫码成功后解除监听", async () => {
		install(async () => ({ content: "oryn://pair?server=x" }));
		await expect(scanQr()).resolves.toEqual({
			kind: "ok",
			content: "oryn://pair?server=x",
		});
		expect(unregister).toHaveBeenCalledTimes(1);
		expect(backHandler).toBeNull();
	});

	it("扫码出错也解除监听", async () => {
		install(async () => {
			throw new Error("camera unavailable");
		});
		const r = await scanQr();
		expect(r.kind).toBe("failed");
		expect(unregister).toHaveBeenCalledTimes(1);
		expect(backHandler).toBeNull();
	});

	it("没有返回键接口（旧壳）时照常扫码", async () => {
		install(async () => ({ content: "https://oryn.example.com" }));
		Object.assign(window, { __TAURI__: { core: { invoke } } });
		await expect(scanQr()).resolves.toEqual({
			kind: "ok",
			content: "https://oryn.example.com",
		});
	});
});
