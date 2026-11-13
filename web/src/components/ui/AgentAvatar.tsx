/** agent 头像：设置里上传的图片或选的单个字形（emoji / Nerd Font）；未设置时为品牌标。助手回合起点使用。 */
import type { ReactNode } from "react";
import { useAgentIdentity } from "../../api/queries";
import { AvatarTile } from "./AvatarTile";
import { BrandMark } from "./data";

export function AgentAvatar({
	size = 20,
	working = false,
}: {
	size?: number;
	/** agent 正在这个回合里工作：默认品牌标的光标竖线闪烁；自定义图片 / 字形不闪。 */
	working?: boolean;
}): ReactNode {
	const { data } = useAgentIdentity();
	return (
		<AvatarTile
			of="agent"
			identity={data}
			size={size}
			tone="accent"
			fallback={<BrandMark size={Math.round(size * 0.7)} blink={working} />}
		/>
	);
}
