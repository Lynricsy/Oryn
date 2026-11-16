import { describe, expect, it } from "vitest";
import {
	deriveDesktopControl,
	desktopAskTitle,
	parseDesktopAsk,
} from "./desktop-ask";

const BOTH = ["完成", "跳过"];

describe("parseDesktopAsk", () => {
	it("识别四种事项，原因原样保留（含换行）", () => {
		expect(
			parseDesktopAsk("需要你操作桌面（登录）：登录 GitHub", BOTH),
		).toEqual({ kind: "sign_in", reason: "登录 GitHub" });
		expect(parseDesktopAsk("需要你操作桌面（输入验证码）：短信", BOTH)).toEqual(
			{
				kind: "code",
				reason: "短信",
			},
		);
		expect(
			parseDesktopAsk("需要你操作桌面（人工验证）：过滑块\n再点继续", BOTH),
		).toEqual({ kind: "check", reason: "过滑块\n再点继续" });
		expect(parseDesktopAsk("需要你操作桌面：确认支付", BOTH)).toEqual({
			kind: "other",
			reason: "确认支付",
		});
	});

	it("标题或选项不符约定时按普通 select 处理", () => {
		expect(parseDesktopAsk("需要你操作桌面（未知）：x", BOTH)).toBeNull();
		expect(parseDesktopAsk("需要你操作桌面：", BOTH)).toBeNull();
		expect(parseDesktopAsk("允许执行？", BOTH)).toBeNull();
		expect(parseDesktopAsk("需要你操作桌面：x", ["完成"])).toBeNull();
		expect(parseDesktopAsk("需要你操作桌面：x", ["跳过", "完成"])).toBeNull();
	});

	it("与拼标题互逆", () => {
		for (const kind of ["sign_in", "code", "check", "other"] as const)
			expect(parseDesktopAsk(desktopAskTitle(kind, "原因"), BOTH)).toEqual({
				kind,
				reason: "原因",
			});
	});
});

describe("deriveDesktopControl", () => {
	const ask = {
		threadId: "T-1",
		requestId: "r1",
		kind: "sign_in" as const,
		reason: "登录",
	};

	it("没有上报按 idle", () => {
		expect(deriveDesktopControl(undefined)).toEqual({
			state: "idle",
			userHeld: false,
			locked: false,
		});
	});

	it("agent：锁定为仅观看", () => {
		expect(
			deriveDesktopControl({ state: "agent", userHeld: false }),
		).toMatchObject({ state: "agent", locked: true });
	});

	it("user：放开输入、可交还", () => {
		expect(
			deriveDesktopControl({ state: "user", userHeld: true, userSince: 1 }),
		).toEqual({ state: "user", userHeld: true, locked: false });
	});

	it("ask：带请求、不锁定，保留用户是否持有控制", () => {
		expect(deriveDesktopControl({ state: "ask", userHeld: true, ask })).toEqual(
			{ state: "ask", userHeld: true, locked: false, ask },
		);
		expect(
			deriveDesktopControl({ state: "ask", userHeld: false, ask }),
		).toMatchObject({ state: "ask", userHeld: false, locked: false });
	});

	it("ask 缺请求详情时退回 user / idle", () => {
		expect(deriveDesktopControl({ state: "ask", userHeld: true })).toEqual({
			state: "user",
			userHeld: true,
			locked: false,
		});
		expect(deriveDesktopControl({ state: "ask", userHeld: false })).toEqual({
			state: "idle",
			userHeld: false,
			locked: false,
		});
	});
});
