/** 原生竖栏控制器：spec 生成（与壳共用的夹具）、接管 / 回退 / 去重 / 撤掉。 */
import { beforeAll, describe, expect, it, type Mock, vi } from "vitest";
import type { NativeRailSpec } from "./host";
import {
	buildRailSpec,
	createNativeRail,
	type RailSpecInput,
} from "./native-rail";
import { buildTabItems } from "./native-tab-bar";

// 前端 tsconfig 不带 Node 类型（静态导入 node:fs 过不了 tsc），只声明用到的函数（同 native-tab-bar.test）；工作目录是 web/
let readFile: (path: string) => string;
beforeAll(async () => {
	const fs = (await import("node:fs" as string)) as {
		readFileSync(path: string, encoding: "utf8"): string;
	};
	readFile = (path) => fs.readFileSync(path, "utf8");
});

const FIXTURES = "src-tauri/plugins/oryn-mobile/fixtures";

/** rail-set.json 对应的输入：树（开着、带提醒点）+ 更多（菜单），四项分区里助手是字形且未读。 */
const INPUT: RailSpecInput = {
	key: "t/T-1",
	dark: false,
	palette: {
		accent: "#2f6fed",
		brand: "#3b82f6",
		err: "#c8372d",
		warn: "#b7791f",
		good: "#2f9e5b",
		frost: "#5b93c9",
	},
	covered: false,
	lead: { label: "侧栏", active: false },
	actions: [
		{ id: "tree", icon: "tree", label: "树", active: true, dot: "accent" },
		{
			id: "more",
			icon: "more",
			label: "更多",
			menu: [
				{ key: "archive", icon: "archive", label: "归档" },
				{ key: "rename", icon: "edit", label: "重命名" },
			],
		},
	],
	tabs: {
		label: "分区",
		items: buildTabItems({
			labels: {
				threads: "线程",
				assistant: "小助手",
				projects: "项目",
				more: "更多",
			},
			assistant: { image: null, glyph: "🦊", unread: true },
			conn: null,
		}),
		selected: "threads",
	},
};

const flush = () => new Promise((r) => setTimeout(r, 0));

function fakeHost(reject = false): {
	set: Mock<(spec: NativeRailSpec | null) => Promise<void>>;
} {
	return {
		set: vi.fn(async (_spec: NativeRailSpec | null) => {
			if (reject) throw "unsupported";
		}),
	};
}

describe("buildRailSpec", () => {
	it("与壳共用的夹具一致：图标换成 SF Symbol，菜单是一组原生菜单项，没有的可选字段不输出", () => {
		const fixture = JSON.parse(readFile(`${FIXTURES}/rail-set.json`)) as {
			spec: NativeRailSpec;
		};
		expect(buildRailSpec(INPUT)).toEqual(fixture.spec);
	});

	it("操作至多 4 个、文字按上限截断；菜单项全被略去（没有纯文本名字）时不带菜单", () => {
		const spec = buildRailSpec({
			...INPUT,
			lead: { label: "侧".repeat(80), active: true },
			actions: [
				...Array.from({ length: 5 }, (_, i) => ({
					id: `a${i}`,
					icon: "tree" as const,
					label: "x",
				})),
			],
		});
		expect(spec.actions.map((a) => a.id)).toEqual(["a0", "a1", "a2", "a3"]);
		expect([...spec.lead.label]).toHaveLength(64);
		expect(spec.lead.active).toBe(true);

		const warn = vi.spyOn(console, "warn").mockImplementation(() => {});
		const bare = buildRailSpec({
			...INPUT,
			actions: [
				{
					id: "m",
					icon: "views",
					label: "视图",
					menu: [{ key: "k", label: 42 }],
				},
			],
		});
		warn.mockRestore();
		expect(bare.actions).toEqual([
			{ id: "m", symbol: "square.grid.2x2", label: "视图" },
		]);
	});
});

describe("createNativeRail", () => {
	const spec = buildRailSpec(INPUT);

	it("应答后才接管；内容没变不重发；撤掉立刻退回网页竖栏", async () => {
		const api = fakeHost();
		const rail = createNativeRail(api);
		const seen = vi.fn();
		rail.subscribe(seen);
		rail.setSpec(spec);
		expect(rail.engaged()).toBe(false);
		await flush();
		expect(rail.engaged()).toBe(true);
		rail.setSpec(JSON.parse(JSON.stringify(spec)) as NativeRailSpec);
		await flush();
		expect(api.set).toHaveBeenCalledTimes(1);
		rail.setSpec(null);
		expect(rail.engaged()).toBe(false);
		await flush();
		expect(api.set.mock.calls).toEqual([[spec], [null]]);
		expect(seen).toHaveBeenCalledTimes(2);
	});

	it("被拒（iOS < 27.1）：不再可用、不接管，之后的 spec 不再发", async () => {
		const warn = vi.spyOn(console, "warn").mockImplementation(() => {});
		const api = fakeHost(true);
		const rail = createNativeRail(api);
		rail.setSpec(spec);
		await flush();
		expect(rail.available()).toBe(false);
		expect(rail.engaged()).toBe(false);
		rail.setSpec({ ...spec, covered: true });
		await flush();
		expect(api.set).toHaveBeenCalledTimes(1);
		warn.mockRestore();
	});

	it("没有宿主能力：不可用，一切空操作", () => {
		const rail = createNativeRail(null);
		expect(rail.available()).toBe(false);
		rail.setSpec(spec);
		expect(rail.engaged()).toBe(false);
	});
});
