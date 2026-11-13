import { describe, expect, it } from "vitest";
import { currentSpeed, speedCommands, speedTiers } from "./speed-tier";

const base = {
	fastModeSupported: false,
	fastModeEnabled: false,
	ultrafastSupported: false,
	ultrafastEnabled: false,
	slowModeSupported: false,
	slowModeEnabled: false,
};

describe("speedTiers", () => {
	it("只列当前模型支持的档位，从快到慢", () => {
		expect(speedTiers(base)).toEqual(["standard"]);
		expect(
			speedTiers({
				...base,
				fastModeSupported: true,
				ultrafastSupported: true,
				slowModeSupported: true,
			}),
		).toEqual(["ultrafast", "fast", "standard", "slow"]);
		// Claude：有 fast、有订阅慢速，没有 ultrafast
		expect(
			speedTiers({ ...base, fastModeSupported: true, slowModeSupported: true }),
		).toEqual(["fast", "standard", "slow"]);
	});
});

describe("currentSpeed", () => {
	it("ultrafast 同时让 fastModeEnabled 为真，仍认作极速", () => {
		expect(
			currentSpeed({ ...base, fastModeEnabled: true, ultrafastEnabled: true }),
		).toBe("ultrafast");
		expect(currentSpeed({ ...base, fastModeEnabled: true })).toBe("fast");
		expect(currentSpeed({ ...base, slowModeEnabled: true })).toBe("slow");
		expect(currentSpeed(base)).toBe("standard");
	});
});

describe("speedCommands", () => {
	it("已在目标档时不发命令", () => {
		expect(speedCommands(base, "standard")).toEqual([]);
	});

	it("快、慢可同时开着（Anthropic）：切到慢速先关快速，切到快速先关慢速", () => {
		const both = { ...base, fastModeEnabled: true, slowModeEnabled: true };
		expect(speedCommands({ ...base, fastModeEnabled: true }, "slow")).toEqual([
			{ type: "set_fast_mode", enabled: false },
			{ type: "set_slow_mode", enabled: true },
		]);
		expect(speedCommands({ ...base, slowModeEnabled: true }, "fast")).toEqual([
			{ type: "set_slow_mode", enabled: false },
			{ type: "set_fast_mode", enabled: true },
		]);
		expect(speedCommands(both, "standard")).toEqual([
			{ type: "set_fast_mode", enabled: false },
			{ type: "set_slow_mode", enabled: false },
		]);
	});

	it("极速与快速互切不经过关闭：直接设目标档", () => {
		expect(
			speedCommands({ ...base, fastModeEnabled: true }, "ultrafast"),
		).toEqual([{ type: "set_ultrafast_mode", enabled: true }]);
		expect(
			speedCommands(
				{ ...base, fastModeEnabled: true, ultrafastEnabled: true },
				"fast",
			),
		).toEqual([{ type: "set_fast_mode", enabled: true }]);
	});
});
