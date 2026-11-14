/** 全局 agent 头像：设置里上传的图片或选的单个字形（emoji / Nerd Font）；未设置时为品牌标。 */
import type { ReactNode } from "react";
import { useIdentity } from "../../api/identity-queries";
import { IdentityAvatar } from "./IdentityAvatar";

export function AgentAvatar({
	size = 20,
	working = false,
}: {
	size?: number;
	/** agent 正在这个回合里工作：默认品牌标的光标竖线闪烁；自定义图片 / 字形不闪。 */
	working?: boolean;
}): ReactNode {
	const { data } = useIdentity("agent");
	return (
		<IdentityAvatar identity={data} of="agent" size={size} working={working} />
	);
}
