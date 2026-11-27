import { afterEach, describe, expect, it, vi } from "vitest";
import {
	installWorkerShim,
	moduleBootstrap,
	workerScriptURL,
} from "./worker-shim";

const blobs: Blob[] = [];

afterEach(() => {
	blobs.length = 0;
});

function stubObjectURL(): void {
	Object.defineProperty(URL, "createObjectURL", {
		configurable: true,
		value: (b: Blob) => {
			blobs.push(b);
			return `blob:null/${blobs.length}`;
		},
	});
}

const PAGE = "http://a-x.localhost:7600/_oryn/v/1/";

describe("workerScriptURL", () => {
	it("classic 的 blob 地址原样返回", () => {
		expect(workerScriptURL("blob:null/abc", undefined, PAGE)).toBe(
			"blob:null/abc",
		);
	});

	it("module worker 一律换成动态 import 的 classic 引导脚本（Chromium 不让不透明源起 module worker）", async () => {
		stubObjectURL();
		expect(
			workerScriptURL(
				"/_oryn/viewer/pdfjs/pdf.worker.min.mjs",
				{ type: "module" },
				PAGE,
			),
		).toBe("blob:null/1");
		expect(await blobs[0]?.text()).toBe(
			moduleBootstrap(
				"http://a-x.localhost:7600/_oryn/viewer/pdfjs/pdf.worker.min.mjs",
			),
		);
		expect(workerScriptURL("blob:null/lib", { type: "module" }, PAGE)).toBe(
			"blob:null/2",
		);
		expect(await blobs[1]?.text()).toBe(moduleBootstrap("blob:null/lib"));
	});

	it("同站的 classic worker 用 importScripts", async () => {
		stubObjectURL();
		workerScriptURL(new URL("w.js", PAGE), undefined, PAGE);
		expect(await blobs[0]?.text()).toBe(
			'importScripts("http://a-x.localhost:7600/_oryn/v/1/w.js");',
		);
	});

	it("data: 脚本搬进 blob（帧策略的 worker-src 不放行 data:）", async () => {
		stubObjectURL();
		expect(
			workerScriptURL(
				`data:text/javascript;base64,${btoa("self.y=2;")}`,
				undefined,
				PAGE,
			),
		).toBe("blob:null/1");
		expect(await blobs[0]?.text()).toBe("self.y=2;");
		expect(blobs[0]?.type).toBe("text/javascript");
		workerScriptURL(
			`data:text/javascript;charset=utf-8,${encodeURIComponent("self.x=1;")}`,
			{ type: "module" },
			PAGE,
		);
		expect(await blobs[1]?.text()).toBe("self.x=1;");
		expect(await blobs[2]?.text()).toBe(moduleBootstrap("blob:null/2"));
	});
});

describe("moduleBootstrap", () => {
	it("模块求值完之前收到的消息按原顺序重发", async () => {
		// 引导脚本里的 self 换成一个 EventTarget，import() 换成替身：「模块」求值时在 self 上注册自己的监听
		// （vitest 的 vm 里 new Function 不能动态 import）
		const target = new EventTarget();
		const seen: unknown[] = [];
		const imported: string[] = [];
		const fakeImport = async (url: string) => {
			imported.push(url);
			target.addEventListener("message", (e) =>
				seen.push((e as MessageEvent).data),
			);
		};
		const src = moduleBootstrap("blob:null/lib").replace("import(", "load(");
		new Function("self", "load", src)(target, fakeImport);
		target.dispatchEvent(new MessageEvent("message", { data: 1 }));
		target.dispatchEvent(new MessageEvent("message", { data: 2 }));
		expect(seen).toEqual([]);
		await vi.waitFor(() => expect(seen).toEqual([1, 2]));
		expect(imported).toEqual(["blob:null/lib"]);
		target.dispatchEvent(new MessageEvent("message", { data: 3 }));
		expect(seen).toEqual([1, 2, 3]);
	});
});

describe("installWorkerShim", () => {
	it("把 Worker 换成改写地址、以 classic 启动的子类，只装一次", () => {
		stubObjectURL();
		const seen: [string | URL, WorkerOptions | undefined][] = [];
		class FakeWorker {
			constructor(url: string | URL, options?: WorkerOptions) {
				seen.push([url, options]);
			}
		}
		const win = {
			Worker: FakeWorker as unknown as typeof Worker,
			location: { href: PAGE } as Location,
		};
		installWorkerShim(win);
		const shimmed = win.Worker;
		installWorkerShim(win);
		expect(win.Worker).toBe(shimmed);
		new win.Worker("w.js", { type: "module", name: "parse" });
		new win.Worker("blob:null/keep");
		expect(seen).toEqual([
			["blob:null/1", { type: "classic", name: "parse" }],
			["blob:null/keep", { type: "classic" }],
		]);
	});
});
