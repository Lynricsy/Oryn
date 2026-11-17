/**
 * 设置 → Agent → SSH 主机（M18）：登记的主机经控制面 SSH 网关连接，私钥与密码只在控制面。
 * 卡片顺序：说明 → 全局主机 → 钥匙 → 网关审计。项目主机在项目页的同款卡片里管理。
 */
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { Callout } from "../../../components/ui/data";
import { AuditCard } from "./ssh/AuditCard";
import { HostsCard } from "./ssh/HostsCard";
import { KeysCard } from "./ssh/KeysCard";

export default function SshPage(): ReactNode {
	const { t } = useTranslation();
	return (
		<>
			<Callout tone="neutral">{t("sshPage.scope")}</Callout>
			<HostsCard title={t("sshPage.hosts.title")} />
			<KeysCard />
			<AuditCard />
		</>
	);
}
