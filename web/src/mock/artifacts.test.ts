/** M14：mock 的页面帧与 orynd 一致——跟随最新的帧每次导航都显示当时的最新版本，钉住的帧停在那个版本（K10）。 */
import { afterEach, describe, expect, it, vi } from "vitest";
import { ARTIFACT_IDS } from "./fixtures";
import { MockServer } from "./server";

/** jsdom 没有 `URL.createObjectURL`：换成记下内容的假实现，返回按地址读 blob 文本的函数。 */
function stubBlobs(): (url: string) => Promise<string> {
	const blobs = new Map<string, Blob>();
	vi.stubGlobal(
		"URL",
		Object.assign(class extends URL {}, {
			createObjectURL: (b: Blob) => {
				const url = `blob:mock/${blobs.size}`;
				blobs.set(url, b);
				return url;
			},
			revokeObjectURL: () => {},
		}),
	);
	return (url) => {
		const b = blobs.get(url);
		if (!b) throw new Error(`没有 blob ${url}`);
		return b.text();
	};
}

async function openFrame(s: MockServer, body: unknown): Promise<string> {
	const res = await s.handleFetch(
		new Request(`http://x/api/v1/artifacts/${ARTIFACT_IDS.web}/frames`, {
			method: "POST",
			body: JSON.stringify(body),
		}),
	);
	if (!res) throw new Error("mock 未处理 frames");
	expect(res.status).toBe(201);
	const { url } = (await res.json()) as { url: string };
	return url;
}

afterEach(() => {
	vi.unstubAllGlobals();
});

describe("mock 页面帧", () => {
	it("跟随最新的帧在新版本之后重新导航显示新版本，钉住的帧停在原版本", async () => {
		const text = stubBlobs();
		const s = new MockServer();
		const follow = await openFrame(s, {});
		const pinned = await openFrame(s, { version: 1 });
		expect(follow).toMatch(/\/__mock_af\/[a-z2-7]+\/$/);
		const load = (url: string) => {
			const blob = s.artifacts.resolveFrameUrl(url);
			if (!blob) throw new Error(`帧地址没有解析：${url}`);
			return text(blob);
		};

		const before = await load(follow);
		expect(before).toContain('"runtime":true');
		expect(before).not.toContain("热更新之后");
		expect(await load(pinned)).toContain('"runtime":false');

		expect(
			s.artifacts.write(
				ARTIFACT_IDS.web,
				"<!doctype html><title>新版本</title><h1>热更新之后</h1>",
			),
		).toBe(true);
		expect(await load(follow)).toContain("热更新之后");
		expect(await load(pinned)).not.toContain("热更新之后");
	});
});
