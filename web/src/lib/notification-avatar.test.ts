import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { setCachedAssistantAvatar } from "../api/identity-queries";
import { indexStore } from "../state/stores";
import {
	clearNotificationAvatarCache,
	resolveNotificationAvatar,
} from "./notification-avatar";

const TID = "T-0193a1b2-0000-7000-8000-0000000000a1";

beforeEach(() => {
	setCachedAssistantAvatar(undefined);
	clearNotificationAvatarCache();
	indexStore.setState({ threads: {} });
});

afterEach(() => {
	setCachedAssistantAvatar(undefined);
	clearNotificationAvatarCache();
});

describe("resolveNotificationAvatar", () => {
	it("优先使用服务端下发的普通线程图标与颜色", () => {
		expect(
			resolveNotificationAvatar({
				threadId: TID,
				title: "DOCKER: Fix",
				url: `/t/${TID}`,
				avatar: { kind: "thread", icon: "🐳", color: "blue" },
			}),
		).toEqual({ kind: "thread", icon: "🐳", color: "blue" });
	});

	it("服务端未下发 avatar（兼容旧端）时，普通线程从标题卡与线程索引提取图标和颜色", () => {
		indexStore.setState({
			threads: {
				[TID]: {
					id: TID,
					title: "🐳 DOCKER: Build",
					kind: "work",
					color: "teal",
				} as never,
			},
		});
		expect(
			resolveNotificationAvatar({
				threadId: TID,
				title: "🐳 DOCKER: Build",
				url: `/t/${TID}`,
			}),
		).toEqual({ kind: "thread", icon: "🐳", color: "teal" });
	});

	it("全局助手对话与主线动态统一使用 assistant 头像，并在本地缓存有字形时补全", () => {
		setCachedAssistantAvatar({ avatarIcon: "🦊" });
		indexStore.setState({
			threads: {
				[TID]: {
					id: TID,
					title: "主线",
					kind: "assistant",
					main: true,
				} as never,
			},
		});
		expect(
			resolveNotificationAvatar({
				threadId: TID,
				title: "酒狐",
				url: `/t/${TID}`,
			}),
		).toEqual({ kind: "assistant", icon: "🦊" });

		// 服务端明确声明 assistant 且设置的是图片头像（有 hash 无 icon）时，缓存的图标不覆盖图片
		setCachedAssistantAvatar({
			avatarImage: "data:image/png;base64,AAAA",
		});
		expect(
			resolveNotificationAvatar({
				threadId: TID,
				title: "酒狐",
				url: `/t/${TID}`,
				avatar: { kind: "assistant", hash: "1234abcd" },
			}),
		).toEqual({ kind: "assistant", hash: "1234abcd" });
	});

	it("无线程系统通知：/settings/assistant 归属全局助手，其余为 system", () => {
		expect(
			resolveNotificationAvatar({
				title: "Research paused",
				url: "/settings/assistant",
			}),
		).toEqual({ kind: "assistant" });
		expect(
			resolveNotificationAvatar({
				title: "Budget exceeded",
				url: "/stats/usage",
			}),
		).toEqual({ kind: "system" });
	});
});
