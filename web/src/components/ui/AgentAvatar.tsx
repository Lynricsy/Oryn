/** agent 头像：设置里上传的图片或选的单个字形（emoji / Nerd Font）；未设置时为品牌标。助手回合起点使用。 */
import type { ReactNode } from "react";
import { useAgentIdentity } from "../../api/queries";
import { BrandMark } from "./data";

export function AgentAvatar({ size = 20 }: { size?: number }): ReactNode {
	const { data } = useAgentIdentity();
	const box = { width: size, height: size };
	if (data?.avatarImage)
		return (
			<img
				src={data.avatarImage}
				alt=""
				data-agent-avatar="image"
				width={size}
				height={size}
				className="shrink-0 rounded-md object-cover ring-1 ring-border"
				style={box}
			/>
		);
	if (data?.avatarIcon)
		return (
			<span
				aria-hidden
				data-agent-avatar="icon"
				className="inline-grid shrink-0 place-items-center rounded-md bg-accent-soft text-accent-ink leading-none"
				style={{ ...box, fontSize: Math.round(size * 0.66) }}
			>
				{data.avatarIcon}
			</span>
		);
	return (
		<span
			aria-hidden
			data-agent-avatar="default"
			className="inline-grid shrink-0 place-items-center rounded-md bg-accent-soft"
			style={box}
		>
			<BrandMark size={Math.round(size * 0.7)} />
		</span>
	);
}
