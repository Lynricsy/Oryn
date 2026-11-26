/** 设置 → 账户与设备：我的头像、已配对的设备与配对入口。 */
import type { ReactNode } from "react";
import DevicesSettings from "./DevicesSettings";
import { UserAvatarCard } from "./IdentitySettings";

export default function AccountSettings(): ReactNode {
	return (
		<>
			<UserAvatarCard />
			<DevicesSettings />
		</>
	);
}
