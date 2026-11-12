/** mock 服务端的设置接口：Git 身份读写与项目身份覆盖（两项同给或同清）。 */
import { describe, expect, it } from "vitest";
import { MockServer } from "./server";

async function call(
	s: MockServer,
	method: string,
	path: string,
	body?: unknown,
) {
	const res = await s.handleFetch(
		new Request(`http://x/api/v1/${path}`, {
			method,
			body: body === undefined ? undefined : JSON.stringify(body),
		}),
	);
	if (!res) throw new Error(`mock 未处理 ${method} ${path}`);
	return {
		status: res.status,
		json: res.status === 204 ? null : await res.json(),
	};
}

describe("mock 设置接口", () => {
	it("Git 身份：PUT 保存、DELETE 清除、非法邮箱 400", async () => {
		const s = new MockServer();
		expect(
			(
				await call(s, "PUT", "settings/git-identity", {
					name: "A",
					email: "bad",
				})
			).status,
		).toBe(400);
		await call(s, "PUT", "settings/git-identity", {
			name: " A ",
			email: "a@b.c",
		});
		expect((await call(s, "GET", "settings/git-identity")).json).toEqual({
			identity: { name: "A", email: "a@b.c" },
		});
		expect((await call(s, "DELETE", "settings/git-identity")).status).toBe(204);
		expect((await call(s, "GET", "settings/git-identity")).json).toEqual({});
	});

	it("项目身份覆盖：半套 400，空串清除", async () => {
		const s = new MockServer();
		const created = await call(s, "POST", "projects", {
			name: "p",
			gitAuthorName: "Bot",
			gitAuthorEmail: "bot@x.y",
		});
		expect(created.status).toBe(201);
		const id = created.json.id as string;
		expect(
			(await call(s, "PATCH", `projects/${id}`, { gitAuthorName: "Only" }))
				.status,
		).toBe(400);
		const cleared = await call(s, "PATCH", `projects/${id}`, {
			gitAuthorName: "",
			gitAuthorEmail: "",
		});
		expect(cleared.json.gitAuthorName).toBeUndefined();
		expect(cleared.json.gitAuthorEmail).toBeUndefined();
	});
});
