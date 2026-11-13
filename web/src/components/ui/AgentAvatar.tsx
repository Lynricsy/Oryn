/** agent 头像：设置里上传的图片或选的图标；未设置时为品牌标。助手回合起点使用。 */
import type { ReactNode } from "react";
import { useAgentIdentity } from "../../api/queries";
import { BrandMark } from "./data";
import { Icon, type IconName, isIconName } from "./icons";

export function AgentAvatar({ size = 20 }: { size?: number }): ReactNode {
	const { data } = useAgentIdentity();
	const box = { width: size, height: size };
	if (data?.avatarImage)
		return (
			<img
				src={data.avatarImage}
				alt=""
				aria-hidden
				width={size}
				height={size}
				className="shrink-0 rounded-md object-cover ring-1 ring-border"
				style={box}
			/>
		);
	const icon: IconName | null =
		data?.avatarIcon && isIconName(data.avatarIcon) ? data.avatarIcon : null;
	if (icon)
		return (
			<span
				aria-hidden
				className="inline-grid shrink-0 place-items-center rounded-md bg-accent-soft text-accent-ink"
				style={box}
			>
				<Icon name={icon} size={Math.round(size * 0.65)} weight="fill" />
			</span>
		);
	return (
		<span
			aria-hidden
			className="inline-grid shrink-0 place-items-center rounded-md bg-accent-soft"
			style={box}
		>
			<BrandMark size={Math.round(size * 0.7)} />
		</span>
	);
}
