/**
 * 回合头的 role 判定（M9.6 修订稿 §4）：只认会话里的来源记录，不做任何模型 → role 的反推。
 * 每条用例都带负向：同模型的手动选择、未记录来源的切换、回退期间都必须回落。
 */
import { describe, expect, it } from "vitest";
import type { Entry } from "../api/client";
import {
	advanceTrail,
	EMPTY_TRAIL,
	parseModelSource,
	type RoleFace,
	resolveTurnRole,
	type SourceTrail,
	trailOf,
} from "./turn-role";

let seq = 0;
function e(
	type: string,
	payload: Record<string, unknown>,
	file = "s.jsonl",
): Entry {
	seq++;
	return { seq, file, entryId: `x${seq}`, type, payload: { type, ...payload } };
}
const record = (data: Record<string, unknown>, file?: string) =>
	e(
		"custom",
		{ customType: "oryn-model-source", data: { v: 1, ...data } },
		file,
	);
const change = (model: string, extra: Record<string, unknown> = {}) =>
	e("model_change", { model, ...extra });

const SOL = "openai/gpt-6.1-sol";
const OPUS = "anthropic/claude-opus-5-5";
const FABLE = "anthropic/claude-fable-5-2";
const sol = { provider: "openai", model: "gpt-6.1-sol" };
const opus = { provider: "anthropic", model: "claude-opus-5-5" };
const fable = { provider: "anthropic", model: "claude-fable-5-2" };
const haiku = { provider: "anthropic", model: "claude-haiku-5" };

/** 设了头像的 role（站内 key = role id）；Nahida 没头像所以不在表里。 */
const FACES: RoleFace[] = [
	{ key: "Mare", role: "Mare", avatarIcon: "🌊" },
	{ key: "Cyrene", role: "Cyrene", avatarIcon: "🌸" },
	{ key: "Hyacinthia", role: "Hyacinthia", avatarIcon: "🪻" },
];

const walk = (entries: Entry[]) => trailOf([entries]);
const roleOf = (
	entries: Entry[],
	model: { provider: string; model: string } | undefined,
	faces: readonly RoleFace[] = FACES,
) => resolveTurnRole(model, walk(entries), faces)?.role;

describe("resolveTurnRole", () => {
	const cases: {
		name: string;
		entries: () => Entry[];
		model: { provider: string; model: string } | undefined;
		faces?: RoleFace[];
		want: string | undefined;
	}[] = [
		{
			name: "role 记录、回合跑在记录的模型上 → 该 role",
			entries: () => [
				change(SOL, { role: "Mare" }),
				record({ source: "role", role: "Mare", model: SOL }),
			],
			model: sol,
			want: "Mare",
		},
		{
			name: "同模型的 manual 记录（手动选了 Mare 的模型）→ 无",
			entries: () => [
				change(SOL, { role: "default" }),
				record({ source: "manual", model: SOL }),
			],
			model: sol,
			want: undefined,
		},
		{
			name: "没有记录，只有 model_change{default}（旧 Core）→ 无",
			entries: () => [change(SOL), change(SOL, { role: "default" })],
			model: sol,
			want: undefined,
		},
		{
			name: "记录后跟一条无记录的 model_change（同模型）→ 无",
			entries: () => [
				record({ source: "role", role: "Mare", model: SOL }),
				change(SOL, { role: "default" }),
			],
			model: sol,
			want: undefined,
		},
		{
			name: "role 名写在 model_change 里但没有记录 → 无（不信 model_change.role）",
			entries: () => [change(SOL, { role: "Mare" })],
			model: sol,
			want: undefined,
		},
		{
			name: "重试回退期间（fallback + resolvedModelIsFallback）→ 无",
			entries: () => [
				record({ source: "role", role: "Hyacinthia", model: FABLE }),
				change("anthropic/claude-haiku-5", {
					role: "fallback",
					resolvedModelIsFallback: true,
				}),
			],
			model: haiku,
			want: undefined,
		},
		{
			name: "回退标志为真、模型与记录相同 → 仍无",
			entries: () => [
				record({ source: "role", role: "Hyacinthia", model: FABLE }),
				change(FABLE, { role: "fallback", resolvedModelIsFallback: true }),
			],
			model: fable,
			want: undefined,
		},
		{
			name: "切回主模型（fallback、无标志）→ 记录恢复",
			entries: () => [
				record({ source: "role", role: "Hyacinthia", model: FABLE }),
				change("anthropic/claude-haiku-5", {
					role: "fallback",
					resolvedModelIsFallback: true,
				}),
				change(FABLE, { role: "fallback" }),
			],
			model: fable,
			want: "Hyacinthia",
		},
		{
			name: "prewalk 期间（模型不同）→ 第 4 步否决",
			entries: () => [
				record({ source: "role", role: "Mare", model: SOL }),
				change("anthropic/claude-sonnet-5", { role: "prewalk" }),
			],
			model: { provider: "anthropic", model: "claude-sonnet-5" },
			want: undefined,
		},
		{
			name: "prewalk 结束回到原模型 → 恢复",
			entries: () => [
				record({ source: "role", role: "Mare", model: SOL }),
				change("anthropic/claude-sonnet-5", { role: "prewalk" }),
				change(SOL, { role: "prewalk" }),
			],
			model: sol,
			want: "Mare",
		},
		{
			name: "startup + alias：default 没头像、别名目标有 → 别名目标",
			entries: () => [
				change(OPUS),
				record({
					source: "startup",
					role: "default",
					alias: "Cyrene",
					model: OPUS,
				}),
			],
			model: opus,
			want: "Cyrene",
		},
		{
			name: "startup + alias：default 自己有头像 → default",
			entries: () => [
				change(OPUS),
				record({
					source: "startup",
					role: "default",
					alias: "Cyrene",
					model: OPUS,
				}),
			],
			model: opus,
			faces: [...FACES, { key: "default", role: "default", avatarIcon: "⭐" }],
			want: "default",
		},
		{
			name: "startup + alias：都没头像 → 无",
			entries: () => [
				change(OPUS),
				record({
					source: "startup",
					role: "default",
					alias: "Cyrene",
					model: OPUS,
				}),
			],
			model: opus,
			faces: FACES.filter((f) => f.role !== "Cyrene"),
			want: undefined,
		},
		{
			name: "role 没头像（Nahida）→ 无",
			entries: () => [
				record({
					source: "role",
					role: "Nahida",
					model: "google/gemini-4-carbon",
				}),
			],
			model: { provider: "google", model: "gemini-4-carbon" },
			want: undefined,
		},
		{
			name: "回合模型与记录不等（切了分支）→ 无",
			entries: () => [record({ source: "role", role: "Mare", model: SOL })],
			model: opus,
			want: undefined,
		},
		{
			name: "回合没有模型 → 无",
			entries: () => [record({ source: "role", role: "Mare", model: SOL })],
			model: undefined,
			want: undefined,
		},
		{
			name: "大小写不同仍相等",
			entries: () => [
				record({ source: "role", role: "Mare", model: "OpenAI/GPT-6.1-Sol" }),
			],
			model: sol,
			want: "Mare",
		},
		{
			name: "v:2 / 缺 model / 未知 source 的记录被忽略，且不清掉前一条记录",
			entries: () => [
				record({ source: "role", role: "Mare", model: SOL }),
				e("custom", {
					customType: "oryn-model-source",
					data: { v: 2, source: "manual", model: SOL },
				}),
				record({ source: "manual" }),
				record({ source: "magic", model: SOL }),
			],
			model: sol,
			want: "Mare",
		},
		{
			name: "其它 custom 条目不影响轨迹",
			entries: () => [
				record({ source: "role", role: "Mare", model: SOL }),
				e("custom", {
					customType: "something-else",
					data: { v: 1, source: "manual", model: SOL },
				}),
			],
			model: sol,
			want: "Mare",
		},
	];
	for (const c of cases)
		it(c.name, () => {
			expect(roleOf(c.entries(), c.model, c.faces)).toBe(c.want);
		});

	it("点 default（role 记录）与手动选 default 的模型结果不同", () => {
		const faces = [
			...FACES,
			{ key: "default", role: "default", avatarIcon: "⭐" },
		];
		const picked = [
			change(OPUS, { role: "default" }),
			record({ source: "role", role: "default", model: OPUS }),
		];
		const manual = [
			change(OPUS, { role: "default" }),
			record({ source: "manual", model: OPUS }),
		];
		expect(roleOf(picked, opus, faces)).toBe("default");
		expect(roleOf(manual, opus, faces)).toBeUndefined();
	});

	it("跨会话文件延续：新文件里的回合沿用上一个文件的记录", () => {
		const a = [
			change(SOL),
			record({ source: "role", role: "Mare", model: SOL }, "a.jsonl"),
		];
		const b = [e("session", {}, "b.jsonl")];
		const trail = trailOf([a, b]);
		expect(resolveTurnRole(sol, trail, FACES)?.role).toBe("Mare");
	});

	it("faces 按 key 匹配（分享页的不透明键）", () => {
		const trail = walk([record({ source: "role", role: "k1", model: SOL })]);
		expect(
			resolveTurnRole(sol, trail, [
				{ key: "k1", role: "Mare", avatarIcon: "🌊" },
			])?.role,
		).toBe("Mare");
		expect(
			resolveTurnRole(sol, trail, [
				{ key: "Mare", role: "Mare", avatarIcon: "🌊" },
			]),
		).toBeUndefined();
	});
});

describe("advanceTrail / trailOf", () => {
	it("tracked 只在见过合法记录后为真，之后的 model_change 不清掉它", () => {
		let t: SourceTrail = EMPTY_TRAIL;
		t = advanceTrail(t, change(SOL));
		expect(t.tracked).toBe(false);
		t = advanceTrail(t, record({ source: "manual" }));
		expect(t.tracked).toBe(false);
		t = advanceTrail(t, record({ source: "manual", model: SOL }));
		expect(t.tracked).toBe(true);
		t = advanceTrail(t, change(OPUS, { role: "default" }));
		expect(t).toEqual({ fallback: false, tracked: true });
	});

	it("对无关条目与无变化的 model_change 返回同一引用", () => {
		const t = advanceTrail(
			EMPTY_TRAIL,
			record({ source: "role", role: "Mare", model: SOL }),
		);
		expect(advanceTrail(t, e("message", { message: { role: "user" } }))).toBe(
			t,
		);
		expect(advanceTrail(t, e("thinking_level_change", {}))).toBe(t);
		expect(advanceTrail(t, change(SOL, { role: "fallback" }))).toBe(t);
		expect(advanceTrail(EMPTY_TRAIL, change(SOL))).toBe(EMPTY_TRAIL);
	});

	it("parseModelSource 只认 customType + v:1 + 合法 source + 非空 model", () => {
		expect(
			parseModelSource(
				record({
					source: "startup",
					role: "default",
					alias: "Cyrene",
					model: OPUS,
				}),
			),
		).toEqual({
			source: "startup",
			role: "default",
			alias: "Cyrene",
			model: OPUS,
		});
		expect(
			parseModelSource(record({ source: "role", role: "", model: SOL })),
		).toEqual({ source: "role", model: SOL });
		expect(
			parseModelSource(record({ source: "role", model: "" })),
		).toBeUndefined();
		expect(
			parseModelSource(
				e("message", {
					customType: "oryn-model-source",
					data: { v: 1, source: "role", model: SOL },
				}),
			),
		).toBeUndefined();
	});
});
