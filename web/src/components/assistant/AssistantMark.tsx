/**
 * 全局助手的小图形：设置了助手头像时是圆形头像，否则是 sparkle 图标（不跟随 Agent 身份）。
 * 助手页页头与「助手」分区侧栏的主线行共用。
 */
import type { ReactNode } from "react";
import { useAssistantIdentity } from "../../api/identity-queries";
import { AssistantAvatar } from "../ui/IdentityAvatar";
import { Icon } from "../ui/icons";

export function AssistantMark({ size }: { size: number }): ReactNode {
	const { avatar } = useAssistantIdentity();
	return avatar.avatarIcon || avatar.avatarImage ? (
		<AssistantAvatar size={size} shape="circle" />
	) : (
		<Icon
			name="sparkle"
			size={16}
			aria-hidden
			className="shrink-0 text-faint"
		/>
	);
}
