/** Boxes 纯逻辑：box 线程标签、首屏胶囊的三种状态、时段文案、名字校验、最近部署的 app。 */
import { describe, expect, it } from "vitest";
import type { Box, BoxApp } from "../../api/box-queries";
import i18n from "../../i18n";
import {
	appOfCwd,
	boxLabel,
	boxStateText,
	boxThreadLabel,
	lastDeployedApp,
	scheduleText,
	validBoxName,
} from "./box-model";

const t = i18n.t.bind(i18n);

function app(name: string, live?: number): BoxApp {
	return {
		name,
		public: true,
		repo: `github.com/acme/${name}`,
		root: "",
		source: true,
		port: 30000,
		deploying: live === undefined,
		preview: false,
		services: [],
		createdAt: 1,
		updatedAt: 1,
		...(live !== undefined
			? {
					live: {
						id: `bxr_${name}`,
						commit: "abcdef1234567",
						branch: "main",
						repo: `github.com/acme/${name}`,
						root: "",
						source: true,
						sizeBytes: 1,
						state: "live",
						createdAt: live,
						activatedAt: live,
					},
				}
			: {}),
	};
}

function box(p: Partial<Box> = {}): Box {
	return {
		id: "box_1",
		name: "workbench",
		orbId: "orb-box-1",
		state: "running",
		desired: "running",
		provider: "local",
		type: "medium",
		cpus: 4,
		memoryMB: 8192,
		createdAt: 1,
		apps: [app("bug-tracker", 200), app("tiny-crm"), app("wiki", 300)],
		...p,
	};
}

describe("boxThreadLabel", () => {
	it("app 目录里的线程写 app 名，机器上的写「机器」", () => {
		const orb = { id: "orb-box-1", box: true };
		expect(
			boxThreadLabel(orb, { cwd: "~/apps/bug-tracker/current" }, [box()], t),
		).toBe("workbench · bug-tracker");
		expect(
			boxThreadLabel(orb, { cwd: "~/apps/wiki/current/apps/wiki" }, [box()], t),
		).toBe("workbench · wiki");
		expect(boxThreadLabel(orb, { cwd: "~" }, [box()], t)).toBe(
			`workbench · ${t("boxes.picker.machine")}`,
		);
	});

	it("不是 box、或 box 列表还没到时为 null", () => {
		expect(
			boxThreadLabel({ id: "orb-x" }, { cwd: "~" }, [box()], t),
		).toBeNull();
		expect(
			boxThreadLabel(
				{ id: "orb-box-1", box: true },
				{ cwd: "~" },
				undefined,
				t,
			),
		).toBeNull();
		expect(
			boxThreadLabel({ id: "orb-box-2", box: true }, { cwd: "~" }, [box()], t),
		).toBeNull();
	});

	it("appOfCwd 只认 ~/apps/<名>/ 前缀", () => {
		expect(appOfCwd("~/apps/a/current")).toBe("a");
		expect(appOfCwd("~/apps/a")).toBe("a");
		expect(appOfCwd("/home/user/apps/a/current")).toBeUndefined();
		expect(appOfCwd(undefined)).toBeUndefined();
	});
});

describe("boxLabel", () => {
	it("列表没到 → loading，主文案是 …", () => {
		const l = boxLabel({ boxId: "box_1" }, undefined, t);
		expect(l.state).toBe("loading");
		expect(l.text.main).toBe("…");
	});

	it("可用 → box 名加 app / 机器徽标", () => {
		const l = boxLabel({ boxId: "box_1", app: "wiki" }, [box()], t);
		expect(l).toEqual({
			state: "ok",
			text: { label: "workbench · wiki", main: "workbench", badge: "wiki" },
		});
		expect(boxLabel({ boxId: "box_1" }, [box()], t).text.badge).toBe(
			t("boxes.picker.machine"),
		);
	});

	it("box 不在、停着、或 app 没有 live → 警示态", () => {
		for (const [pick, list] of [
			[{ boxId: "box_9" }, [box()]],
			[{ boxId: "box_1" }, [box({ desired: "stopped" })]],
			[{ boxId: "box_1", app: "tiny-crm" }, [box()]],
			[{ boxId: "box_1", app: "nope" }, [box()]],
		] as const) {
			const l = boxLabel(pick, list, t);
			expect(l.state).toBe("gone");
			expect(l.text.warn).toBe(true);
			expect(l.text.main).toBe(t("boxes.picker.gone"));
		}
	});
});

describe("文案与校验", () => {
	it("时段文案；没有时段是「一直运行」", () => {
		expect(
			scheduleText({ on: "05:00", off: "23:00", tz: "Europe/Berlin" }, t),
		).toBe("05:00–23:00 Europe/Berlin");
		expect(scheduleText(undefined, t)).toBe(t("boxes.about.alwaysOn"));
	});

	it("被时段停下的写「按时段停机」", () => {
		expect(
			boxStateText(box({ state: "stopped", stoppedBy: "schedule" }), t),
		).toBe(t("boxes.state.stoppedBySchedule"));
		expect(boxStateText(box({ state: "stopped", stoppedBy: "owner" }), t)).toBe(
			t("boxes.state.stopped"),
		);
	});

	it("box 名同服务端的正则，不含 --", () => {
		for (const ok of ["a", "workbench", "lab-1", "a".repeat(30)])
			expect(validBoxName(ok)).toBe(true);
		for (const bad of [
			"",
			"-a",
			"a-",
			"A",
			"a_b",
			"a--b",
			"a".repeat(31),
			"a.b",
		])
			expect(validBoxName(bad)).toBe(false);
	});

	it("最近部署的 app 取 live 激活时间最大的", () => {
		expect(lastDeployedApp(box())?.name).toBe("wiki");
		expect(lastDeployedApp(box({ apps: [app("x")] }))).toBeUndefined();
	});
});
