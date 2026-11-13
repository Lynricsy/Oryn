/**
 * 用户头像：设置里上传的图片或选的单个字形；未设置时是中性色方块，
 * 有 git 身份名字就放名字首字，否则放 User 图标。用户回合起点与侧栏页脚使用。
 */
import type { ReactNode } from "react";
import { useGitIdentity, useUserIdentity } from "../../api/queries";
import { AvatarTile } from "./AvatarTile";
import { firstGrapheme } from "./data";
import { Icon } from "./icons";

export function UserAvatar({ size = 20 }: { size?: number }): ReactNode {
	const { data } = useUserIdentity();
	const name = useGitIdentity().data?.identity?.name.trim();
	return (
		<AvatarTile
			of="user"
			identity={data}
			size={size}
			tone="neutral"
			fallback={
				name ? (
					<span
						className="font-semibold"
						style={{ fontSize: Math.round(size * 0.5) }}
					>
						{firstGrapheme(name)}
					</span>
				) : (
					<Icon name="user" size={Math.round(size * 0.65)} />
				)
			}
		/>
	);
}
