/** mock 后端的 M9.6 接口：身份设置、role 头像、oryn_select_model / 旧 Core、分享页的匿名身份与公开来源记录。 */
import { describe, expect, it } from "vitest";
import type { Entry, Schemas } from "../api/client";
import { parseModelSource } from "../lib/turn-role";
import { M96_IDS } from "./fixtures";
import { MOCK_SHARE_SLUGS, MockServer, type MockSocket } from "./server";

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
	return { status: res.status, json: await res.json(), text: "" };
}

class Capture implements MockSocket {
	msgs: Record<string, unknown>[] = [];
	deliver(msg: Record<string, unknown>) {
		this.msgs.push(msg);
	}
	/** 某条命令的应答。 */
	res(id: string) {
		return this.msgs.find((m) => m.type === "res" && m.id === id)?.response as
			| { success: boolean; data?: Record<string, unknown>; error?: string }
			| undefined;
	}
}

async function cmd(
	s: MockServer,
	c: Capture,
	threadId: string,
	frame: Record<string, unknown>,
) {
	const id = `c${c.msgs.length}-${String(frame.type)}`;
	s.onClient(c, { type: "cmd", threadId, id, frame });
	// handleCmd 在第一个 await 之前就同步应答了这些命令
	return c.res(id);
}

const tail = (s: MockServer, threadId: string, n: number) =>
	(s.entries.get(threadId) ?? [])
		.slice(-n)
		.map((e) => ({ type: e.type, payload: e.payload }));

describe("身份设置接口", () => {
	it("agent / assistant：名字去空白、超长与控制字符 400、头像互斥；互不影响", async () => {
		const s = new MockServer();
		expect(
			(
				await call(s, "PUT", "settings/agent-identity", {
					name: "  Oryn ",
					avatarIcon: "🦊",
				})
			).json,
		).toEqual({ name: "Oryn", avatarIcon: "🦊" });
		expect(
			(
				await call(s, "PUT", "settings/agent-identity", {
					name: "x".repeat(41),
				})
			).status,
		).toBe(400);
		expect(
			(await call(s, "PUT", "settings/agent-identity", { name: "a\nb" }))
				.status,
		).toBe(400);
		expect(
			(
				await call(s, "PUT", "settings/assistant-identity", {
					avatarIcon: "✨",
					avatarImage: "data:image/png;base64,AA==",
				})
			).status,
		).toBe(400);
		await call(s, "PUT", "settings/assistant-identity", { name: "小助手" });
		expect((await call(s, "GET", "settings/agent-identity")).json).toEqual({
			name: "Oryn",
			avatarIcon: "🦊",
		});
		expect((await call(s, "GET", "settings/assistant-identity")).json).toEqual({
			name: "小助手",
		});
	});

	it("role-identities：按 cycleOrder，带 selector、没有 models；设置 / 清除；不在 cycle 里不能设", async () => {
		const s = new MockServer();
		const list = (await call(s, "GET", "settings/role-identities"))
			.json as Schemas["RoleIdentityList"];
		expect(list.roles.map((r) => r.role)).toEqual([
			"Hyacinthia",
			"Nahida",
			"Mare",
			"Cyrene",
			"Arianna",
		]);
		const mare = list.roles.find((r) => r.role === "Mare");
		expect(mare).toMatchObject({
			inCycle: true,
			selector: "openai/gpt-6.1-sol:high",
			avatarIcon: "🌊",
		});
		expect(mare && "models" in mare).toBe(false);
		expect(
			list.roles.find((r) => r.role === "Nahida")?.avatarIcon,
		).toBeUndefined();

		expect(
			(
				await call(s, "PUT", "settings/role-identities", {
					role: "Nahida",
					avatarIcon: "🌿",
				})
			).json,
		).toMatchObject({ role: "Nahida", avatarIcon: "🌿", inCycle: true });
		expect(
			(
				await call(s, "PUT", "settings/role-identities", {
					role: "slow",
					avatarIcon: "🐢",
				})
			).status,
		).toBe(400);
		await call(s, "PUT", "settings/role-identities", { role: "Mare" });
		expect(s.roleAvatars.Mare).toBeUndefined();
		// 遗留项：在列表末尾，可清除
		s.roleAvatars.Old = { avatarIcon: "👻" };
		const withOrphan = (await call(s, "GET", "settings/role-identities"))
			.json as Schemas["RoleIdentityList"];
		expect(withOrphan.roles.at(-1)).toMatchObject({
			role: "Old",
			inCycle: false,
		});
		await call(s, "PUT", "settings/role-identities", { role: "Old" });
		expect(s.roleAvatars.Old).toBeUndefined();
	});
});

describe("oryn_select_model（mock 的新 Core）", () => {
	it("点 role：model_change{role} + 来源记录；手动选同一模型：model_change{default} + manual 记录（没有 role）", async () => {
		const s = new MockServer();
		const c = new Capture();
		s.connect(c);
		const T = M96_IDS.roleThread;
		const role = await cmd(s, c, T, {
			type: "oryn_select_model",
			role: "Mare",
		});
		expect(role?.success).toBe(true);
		expect(role?.data).toMatchObject({ source: "role", role: "Mare" });
		expect(tail(s, T, 2)).toMatchObject([
			{
				type: "model_change",
				payload: { model: "openai/gpt-6.1-sol", role: "Mare" },
			},
			{
				type: "custom",
				payload: {
					customType: "oryn-model-source",
					data: {
						v: 1,
						source: "role",
						role: "Mare",
						model: "openai/gpt-6.1-sol",
					},
				},
			},
		]);
		expect(s.states.get(T)?.model?.id).toBe("gpt-6.1-sol");

		await cmd(s, c, T, {
			type: "oryn_select_model",
			provider: "openai",
			modelId: "gpt-6.1-sol",
		});
		const [change, rec] = (s.entries.get(T) ?? []).slice(-2) as Entry[];
		expect(change?.payload).toMatchObject({ role: "default" });
		expect(parseModelSource(rec as Entry)).toEqual({
			source: "manual",
			model: "openai/gpt-6.1-sol",
		});
	});

	it("default role 带别名；非法参数与未知 role 报错且不写条目", async () => {
		const s = new MockServer();
		const c = new Capture();
		s.connect(c);
		const T = M96_IDS.roleThread;
		const r = await cmd(s, c, T, {
			type: "oryn_select_model",
			role: "default",
		});
		expect(r?.data).toMatchObject({ role: "default", alias: "Cyrene" });
		const n = s.entries.get(T)?.length;
		for (const [frame, error] of [
			[{}, "oryn_select_model requires either role or provider+modelId"],
			[
				{ role: "" },
				"oryn_select_model requires either role or provider+modelId",
			],
			[{ provider: "openai" }, "provider and modelId must be given together"],
			[{ role: "nope" }, "Role has no available model: nope"],
			[{ provider: "x", modelId: "y" }, "Model not found: x/y"],
		] as const) {
			const res = await cmd(s, c, T, { type: "oryn_select_model", ...frame });
			expect(res).toMatchObject({ success: false, error });
		}
		expect(s.entries.get(T)?.length).toBe(n);
	});

	it("旧 Core 的线程：oryn_* 一律 Unknown command；set_model 照常、写 model_change{default}、没有来源记录", async () => {
		const s = new MockServer();
		const c = new Capture();
		s.connect(c);
		const T = M96_IDS.legacyThread;
		expect(
			await cmd(s, c, T, { type: "oryn_select_model", role: "Mare" }),
		).toMatchObject({
			success: false,
			error: "Unknown command: oryn_select_model",
		});
		await cmd(s, c, T, {
			type: "set_model",
			provider: "openai",
			modelId: "gpt-6.1-sol",
		});
		expect(tail(s, T, 1)).toMatchObject([
			{ type: "model_change", payload: { role: "default" } },
		]);
		expect(s.entries.get(T)?.some((e) => e.type === "custom")).toBe(false);
	});
});

describe("分享页的匿名身份", () => {
	it("快照：只含用到且有头像的 role，键与公开条目里的一致；没用到 / 没头像的 role 不出网；改设置不影响快照、影响实时", async () => {
		const s = new MockServer();
		const snap = (await call(
			s,
			"GET",
			`public/shares/${MOCK_SHARE_SLUGS.roleSnapshot}/identity`,
		)) as { status: number; json: Schemas["PublicShareIdentity"] };
		expect(snap.status).toBe(200);
		expect(snap.json.roles.map((r) => r.role).sort()).toEqual([
			"Cyrene",
			"Hyacinthia",
			"Mare",
		]);
		expect(snap.json.agent).toBeUndefined();
		const text = JSON.stringify(snap.json);
		expect(text).not.toContain("Nahida");
		expect(text).not.toContain("Arianna");

		const page = (
			await call(
				s,
				"GET",
				`public/shares/${MOCK_SHARE_SLUGS.roleSnapshot}/entries?limit=500`,
			)
		).json as Schemas["PublicEntryPage"];
		const keys = page.entries.flatMap((e) => {
			const r = parseModelSource(e);
			return r ? [r.role, r.alias].filter(Boolean) : [];
		});
		for (const r of snap.json.roles) expect(keys).toContain(r.key);
		const entriesText = JSON.stringify(page.entries);
		// 公开条目里没有任何 role id（来源记录与 model_change 都换成了键）
		for (const id of ["Mare", "Nahida", "Hyacinthia", "Cyrene"])
			expect(entriesText).not.toMatch(new RegExp(`"(role|alias)":"${id}"`));
		expect(entriesText).toContain('"role":"fallback"');

		// 改设置：快照不变，实时跟随
		await call(s, "PUT", "settings/agent-identity", { name: "Oryn" });
		await call(s, "PUT", "settings/role-identities", { role: "Mare" });
		const again = (
			await call(
				s,
				"GET",
				`public/shares/${MOCK_SHARE_SLUGS.roleSnapshot}/identity`,
			)
		).json;
		expect(again).toEqual(snap.json);
		const live = (
			await call(
				s,
				"GET",
				`public/shares/${MOCK_SHARE_SLUGS.roleLive}/identity`,
			)
		).json as Schemas["PublicShareIdentity"];
		expect(live.agent).toEqual({ name: "Oryn" });
		expect(live.roles.map((r) => r.role).sort()).toEqual([
			"Cyrene",
			"Hyacinthia",
		]);
		// 不同分享的键不同
		const k1 = snap.json.roles.find((r) => r.role === "Cyrene")?.key;
		const k2 = live.roles.find((r) => r.role === "Cyrene")?.key;
		expect(k1).toMatch(/^[0-9a-f]{16}$/);
		expect(k1).not.toBe(k2);
	});

	it("未知 slug：404", async () => {
		const s = new MockServer();
		expect(
			(await call(s, "GET", `public/shares/${"0".repeat(32)}/identity`)).status,
		).toBe(404);
	});
});
