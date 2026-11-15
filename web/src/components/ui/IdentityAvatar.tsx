/** agent 侧身份（默认 Agent 身份、全局助手、role）的头像，以及全局助手的头像。 */
import type { ReactNode } from "react";
import type { Schemas } from "../../api/client";
import { useAssistantIdentity } from "../../api/identity-queries";
import { type AvatarShape, AvatarTile } from "./AvatarTile";
import { BrandMark } from "./data";
import { Icon } from "./icons";

/**
 * agent 侧身份的头像：图片 / 字形 / 未设置时的默认内容——给了 `glyph`（内置 role 的默认字形）就显示它；
 * 全局助手是 sparkle 标记；其余是品牌标（working 时光标闪）。
 */
export function IdentityAvatar({
	identity,
	of,
	size = 20,
	shape,
	working = false,
	glyph,
}: {
	identity: Schemas["AvatarIdentity"] | undefined;
	of: "agent" | "assistant" | "role";
	size?: number;
	shape?: AvatarShape;
	/** agent 正在这个回合里工作：默认品牌标的光标竖线闪烁；自定义图片 / 字形不闪。 */
	working?: boolean;
	/** 未设置头像时显示的默认字形（内置 role）。 */
	glyph?: string;
}): ReactNode {
	const fallback = glyph ? (
		<span style={{ fontSize: Math.round(size * 0.66) }}>{glyph}</span>
	) : of === "assistant" ? (
		<Icon name="sparkle" size={Math.round(size * 0.66)} aria-hidden />
	) : (
		<BrandMark size={Math.round(size * 0.7)} blink={working} />
	);
	return (
		<AvatarTile
			of={of}
			identity={identity}
			size={size}
			tone="accent"
			shape={shape}
			fallback={fallback}
		/>
	);
}

/** 全局助手的头像（自己设的 → sparkle 标记；不跟随默认 Agent 身份）。 */
export function AssistantAvatar({
	size = 20,
	shape,
}: {
	size?: number;
	shape?: AvatarShape;
}): ReactNode {
	const { avatar } = useAssistantIdentity();
	return (
		<IdentityAvatar
			identity={avatar}
			of="assistant"
			size={size}
			shape={shape}
		/>
	);
}
