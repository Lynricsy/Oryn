/**
 * 设置 → 账户与设备：我的头像 → Passkey 与恢复码 → 设备（列表、配对新设备）→ 连接与会话。
 * 「连接与会话」按宿主分支：两种壳（桌面 / 移动）里是「更换服务器…」（交给壳，回到连接页）；
 * 纯浏览器里是「退出登录」（两种壳里的会话由壳的设备令牌换来，退出后壳会立刻再换一个，所以只给浏览器）。
 */
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { useNavigate } from "react-router";
import { api, unwrap } from "../../api/client";
import { useAsync } from "../../components/settings/useAsync";
import { Button, ErrorText } from "../../components/ui/primitives";
import { Group, GroupList, GroupRow } from "../../components/ui/workbench";
import { changeServerFn } from "../../lib/change-server";
import DevicesSettings from "./DevicesSettings";
import { UserAvatarCard } from "./IdentitySettings";
import PasskeySettings from "./PasskeySettings";

function ConnectionGroup() {
	const { t } = useTranslation();
	const navigate = useNavigate();
	const logout = useAsync();
	const changeServer = changeServerFn();
	return (
		<Group title={t("account.connection")}>
			<GroupList>
				{changeServer ? (
					<GroupRow
						icon="hardDrives"
						title={t("account.server")}
						meta={
							<>
								<span className="break-all font-mono">
									{window.location.origin}
								</span>
								<span aria-hidden className="text-faint">
									·
								</span>
								<span>{t("account.serverHint")}</span>
							</>
						}
						trailing={
							<Button
								size="sm"
								variant="danger"
								data-testid="change-server"
								onClick={() => changeServer()}
							>
								{t("account.changeServer")}
							</Button>
						}
					/>
				) : (
					<GroupRow
						icon="signOut"
						title={t("account.session")}
						meta={t("account.sessionHint")}
						trailing={
							<Button
								size="sm"
								variant="danger"
								disabled={logout.busy}
								onClick={() =>
									void logout
										.run(() => unwrap(api.POST("/api/v1/auth/logout")))
										.then((ok) => {
											if (ok) navigate("/login");
										})
								}
							>
								{t("account.logout")}
							</Button>
						}
					>
						{logout.error ? <ErrorText error={logout.error} /> : null}
					</GroupRow>
				)}
			</GroupList>
		</Group>
	);
}

export default function AccountSettings(): ReactNode {
	return (
		<>
			<UserAvatarCard />
			<PasskeySettings />
			<DevicesSettings />
			<ConnectionGroup />
		</>
	);
}
