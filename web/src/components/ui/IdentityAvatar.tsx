/** agent 侧身份（全局 agent、全局助手、role）的头像，以及全局助手的生效头像。 */
import type { ReactNode } from "react";
import type { Schemas } from "../../api/client";
import { useAssistantIdentity } from "../../api/identity-queries";
import { type AvatarShape, AvatarTile } from "./AvatarTile";
import { BrandMark } from "./data";

/** agent 侧身份的头像：图片 / 字形 / 未设置时品牌标（working 时光标闪）。 */
export function IdentityAvatar({
	identity,
	of,
	size = 20,
	shape,
	working = false,
}: {
	identity: Schemas["AvatarIdentity"] | undefined;
	of: "agent" | "assistant" | "role";
	size?: number;
	shape?: AvatarShape;
	/** agent 正在这个回合里工作：默认品牌标的光标竖线闪烁；自定义图片 / 字形不闪。 */
	working?: boolean;
}): ReactNode {
	return (
		<AvatarTile
			of={of}
			identity={identity}
			size={size}
			tone="accent"
			shape={shape}
			fallback={<BrandMark size={Math.round(size * 0.7)} blink={working} />}
		/>
	);
}

/** 全局助手的生效头像（自己的 → agent 的 → 品牌标）。 */
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
