/**
 * 设置 → SSH 主机（M18）：登记的主机经控制面 SSH 网关连接，私钥与密码只在控制面。
 * 分组顺序：全局主机（下方说明作用范围）→ 钥匙 → 网关审计。项目主机在项目页的同款分组里管理。
 */
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { AuditCard } from "./ssh/AuditCard";
import { HostsCard } from "./ssh/HostsCard";
import { KeysCard } from "./ssh/KeysCard";

export default function SshPage(): ReactNode {
	const { t } = useTranslation();
	return (
		<>
			<HostsCard title={t("sshPage.hosts.title")} intro={t("sshPage.scope")} />
			<KeysCard />
			<AuditCard />
		</>
	);
}
