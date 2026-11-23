import { describe, expect, it } from "vitest";
import { githubAvatarUrl } from "./github-avatar";

describe("githubAvatarUrl", () => {
	it.each([
		// 新式：数字 id
		[
			"12345+Lynricsy@users.noreply.github.com",
			"https://avatars.githubusercontent.com/u/12345?s=40&v=4",
		],
		// bot 账号
		[
			"41898282+github-actions[bot]@users.noreply.github.com",
			"https://avatars.githubusercontent.com/u/41898282?s=40&v=4",
		],
		// 旧式：只有登录名；域名不分大小写
		[
			"WineFoxDev@Users.NoReply.GitHub.com",
			"https://github.com/WineFoxDev.png?size=40",
		],
		// 推不出账号
		["fox@ling.plus", undefined],
		["evil@users.noreply.github.com.example", undefined],
		["", undefined],
	])("%s", (email, want) => {
		expect(githubAvatarUrl(email, 40)).toBe(want);
	});
});
