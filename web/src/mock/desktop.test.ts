/** mock 建线程的 Profile 预检（M8）：只对新 orb 解析项目默认并检查占用；挂到已有 orb 不解析、不冲突。 */
import { describe, expect, it } from "vitest";
import { projects } from "./fixtures";
import { MockServer } from "./server";

async function createThread(s: MockServer, body: unknown) {
	const res = await s.handleFetch(
		new Request("http://x/api/v1/threads", {
			method: "POST",
			body: JSON.stringify(body),
		}),
	);
	if (!res) throw new Error("mock 未处理 POST threads");
	return { status: res.status, json: await res.json() };
}

const ORYN = projects.find((p) => p.defaultBrowserProfileId);

describe("mock 建线程的 Profile 预检", () => {
	it("夹具前提：项目默认 Profile 已被主 orb 独占，新 orb 跟随默认时 409", async () => {
		expect(ORYN).toBeDefined();
		const r = await createThread(new MockServer(), {
			prompt: "x",
			projectId: ORYN?.id,
			executor: { kind: "new_orb" },
		});
		expect(r.status).toBe(409);
		expect(r.json.code).toBe("profile_in_use");
	});

	it("挂到已有 orb：不解析项目默认 Profile，不报冲突", async () => {
		const r = await createThread(new MockServer(), {
			prompt: "x",
			projectId: ORYN?.id,
			executor: { kind: "orb", orbId: "orb-01jcz8m3w4x5y6z7a8b9c0d1e2" },
		});
		expect(r.status).toBe(201);
		expect(r.json.thread.id).toMatch(/^T-mock-/);
	});
});
