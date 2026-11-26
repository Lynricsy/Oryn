/**
 * 设置页「关于」（default export，由设置页接入）：只放信息，每行「标题 + 行尾值」。
 * 1. 服务器：版本、所有者、创建时间、当前凭据类型。
 * 2. 移动 App（仅移动壳）：App 版本、WebView、令牌存储。
 * 3. 协调宿主（CoordinatorAbout）。
 * 语言在「外观」，更换服务器与退出登录在「账户与设备」。
 */
import { type ReactNode, useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { useHealth, useMe } from "../../api/queries";
import { ErrorText } from "../../components/ui/primitives";
import { Group, GroupList, GroupRow } from "../../components/ui/workbench";
import { formatDateTime } from "../../lib/format";
import { host, type MobileInfo } from "../../lib/host";
import { CoordinatorAbout } from "./CoordinatorAbout";

/** 信息行：标题 + 行尾值（值用次要色，长值在窄屏换到下一行）。 */
function InfoRow({
	label,
	value,
	mono,
}: {
	label: ReactNode;
	value: ReactNode;
	mono?: boolean;
}): ReactNode {
	return (
		<GroupRow
			title={label}
			trailing={
				<span
					className={`break-all text-[12.5px] text-muted ${mono ? "font-mono" : ""}`}
				>
					{value}
				</span>
			}
		/>
	);
}

function MobileAbout() {
	const { t } = useTranslation();
	const mobile = host.mobile;
	const [info, setInfo] = useState<MobileInfo | null>(null);
	const [error, setError] = useState<unknown>(null);
	useEffect(() => {
		if (!mobile) return;
		let live = true;
		mobile.info().then(
			(i) => {
				if (live) setInfo(i);
			},
			(e) => {
				if (live) setError(e);
			},
		);
		return () => {
			live = false;
		};
		// 宿主在页面生命周期内不变
	}, []);
	if (!mobile) return null;
	return (
		<Group title={t("about.mobileApp")}>
			<GroupList>
				<InfoRow
					label={t("mobile.about.appVersion")}
					value={info?.version || "—"}
					mono
				/>
				<InfoRow
					label={t("mobile.about.webview")}
					value={info?.webview || "—"}
					mono
				/>
				<InfoRow
					label={t("mobile.about.tokenStore")}
					value={
						info ? t(`mobile.about.tokenStoreKind.${info.tokenStore}`) : "—"
					}
				/>
			</GroupList>
			<ErrorText error={error} />
		</Group>
	);
}

export default function AboutSettings(): ReactNode {
	const { t } = useTranslation();
	const health = useHealth();
	const me = useMe();
	return (
		<>
			<Group title={t("about.server")}>
				<GroupList>
					<InfoRow
						label={t("about.version")}
						value={health.data?.version ?? "—"}
						mono
					/>
					<InfoRow
						label={t("about.owner")}
						value={me.data?.displayName ?? "—"}
					/>
					<InfoRow
						label={t("about.since")}
						value={me.data ? formatDateTime(me.data.createdAt) : "—"}
					/>
					<InfoRow
						label={t("about.session")}
						value={me.data ? t(`about.authKind.${me.data.auth.kind}`) : "—"}
					/>
				</GroupList>
			</Group>
			<MobileAbout />
			<CoordinatorAbout />
		</>
	);
}
