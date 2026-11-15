/** describeError：已知错误码的文案；managed_by_oryn 点名被拦的命令并保留服务端原文（M12.1 走查问题 5）。 */
import { describe, expect, it } from "vitest";
import { ApiError } from "../../api/client";
import i18n from "../../i18n";
import { OmpCommandError } from "../../realtime/socket";
import { describeError } from "./primitives";

const t = i18n.t.bind(i18n);

describe("describeError", () => {
	it("managed_by_oryn：点名斜杠命令并保留服务端原文", () => {
		const e = new OmpCommandError(
			"prompt",
			"managed_by_oryn",
			"/session delete is managed by Oryn",
		);
		const msg = describeError(e, t);
		expect(msg).toBe(
			"/session delete 由 Oryn 管理（会改动线程绑定的会话），不能直接发给 agent。服务端原文：/session delete is managed by Oryn",
		);
	});

	it("managed_by_oryn：RPC 命令同样点名", () => {
		const e = new OmpCommandError(
			"switch_session",
			"managed_by_oryn",
			"switch_session is managed by Oryn",
		);
		expect(describeError(e, t)).toMatch(/^switch_session 由 Oryn 管理/);
	});

	it("managed_by_oryn：原文对不上格式时仍附上原文；没有原文时用通用说明", () => {
		const odd = new ApiError(403, "managed_by_oryn", "blocked by policy");
		expect(describeError(odd, t)).toBe(
			"该操作由 Oryn 管理，不能直接发给 agent。服务端原文：blocked by policy",
		);
		const bare = new OmpCommandError("prompt", "managed_by_oryn", "");
		expect(describeError(bare, t)).toBe(i18n.t("errors.managed_by_oryn"));
	});

	it("英文界面同样点名", async () => {
		await i18n.changeLanguage("en");
		try {
			const e = new OmpCommandError(
				"prompt",
				"managed_by_oryn",
				"/move is managed by Oryn",
			);
			expect(describeError(e, t)).toMatch(/^\/move is managed by Oryn \(/);
		} finally {
			await i18n.changeLanguage("zh-CN");
		}
	});

	it("其它已知错误码与未知错误不受影响", () => {
		expect(
			describeError(new OmpCommandError("prompt", "timeout", "late"), t),
		).toBe(i18n.t("errors.timeout"));
		expect(describeError(new Error("boom"), t)).toBe("boom");
	});
});
