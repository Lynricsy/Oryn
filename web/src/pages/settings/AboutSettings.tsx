/**
 * 设置页「关于」（default export，由设置页接入）：版本、所有者、会话类型、界面语言、协调器信息。
 * 移动壳（M7）另列 App 版本、WebView、令牌存储与「更换服务器…」；退出登录只在纯浏览器里出现
 * （两种壳里的会话由设备令牌换来，改用「更换服务器」）。
 */
import { useQueryClient } from "@tanstack/react-query";
import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { useNavigate } from "react-router";
import { api, unwrap } from "../../api/client";
import {
	type LanguageCode,
	saveLanguageSettings,
	useLanguageSettings,
} from "../../api/language-queries";
import { useHealth, useMe } from "../../api/queries";
import { useAsync } from "../../components/settings/useAsync";
import { Badge, Card, KeyValue } from "../../components/ui/data";
import { Button, ErrorText, Select } from "../../components/ui/primitives";
import { formatDateTime } from "../../lib/format";
import { host, type MobileInfo } from "../../lib/host";
import { CoordinatorAbout } from "./CoordinatorAbout";

export default function AboutSettings() {
	const { t, i18n } = useTranslation();
	const health = useHealth();
	const me = useMe();
	const navigate = useNavigate();
	const logout = useAsync();
	const qc = useQueryClient();
	const langQuery = useLanguageSettings();
	const saveLang = useAsync();
	const mobile = host.mobile;
	const [mobileInfo, setMobileInfo] = useState<MobileInfo | null>(null);
	const [mobileError, setMobileError] = useState<unknown>(null);
	useEffect(() => {
		if (!mobile) return;
		let live = true;
		mobile.info().then(
			(i) => {
				if (live) setMobileInfo(i);
			},
			(e) => {
				if (live) setMobileError(e);
			},
		);
		return () => {
			live = false;
		};
		// 宿主在页面生命周期内不变
	}, []);
	const mobileRows = mobile
		? [
				{
					key: "appVersion",
					label: t("mobile.about.appVersion"),
					value: (
						<span className="font-mono">{mobileInfo?.version || "—"}</span>
					),
				},
				{
					key: "webview",
					label: t("mobile.about.webview"),
					value: (
						<span className="font-mono">{mobileInfo?.webview || "—"}</span>
					),
				},
				{
					key: "tokenStore",
					label: t("mobile.about.tokenStore"),
					value: mobileInfo
						? t(`mobile.about.tokenStoreKind.${mobileInfo.tokenStore}`)
						: "—",
				},
			]
		: [];
	return (
		<Card title={t("about.title")} icon="info">
			<KeyValue
				rows={[
					{
						key: "version",
						label: t("about.version"),
						value: (
							<span className="font-mono">{health.data?.version ?? "—"}</span>
						),
					},
					{
						key: "owner",
						label: t("about.owner"),
						value: me.data?.displayName ?? "—",
					},
					{
						key: "since",
						label: t("about.since"),
						value: me.data ? formatDateTime(me.data.createdAt) : "—",
					},
					{
						key: "session",
						label: t("about.session"),
						value: me.data ? (
							<Badge>{t(`about.authKind.${me.data.auth.kind}`)}</Badge>
						) : (
							"—"
						),
					},
					{
						key: "language",
						label: t("about.language"),
						value: (
							<div className="flex flex-col gap-1">
								<Select
									wrapperClassName="max-w-40"
									aria-label={t("about.language")}
									disabled={saveLang.busy}
									value={langQuery.data?.language ?? i18n.language}
									onChange={(e) => {
										const next = e.target.value as LanguageCode;
										void saveLang.run(() => saveLanguageSettings(qc, next));
									}}
								>
									<option value="zh-CN">简体中文</option>
									<option value="en">English</option>
								</Select>
								<ErrorText error={saveLang.error} />
							</div>
						),
					},
					...mobileRows,
				]}
			/>
			<CoordinatorAbout />
			{mobile && (
				<div className="mt-4 flex flex-col gap-1.5 border-border border-t pt-3">
					<ErrorText error={mobileError} />
					<div>
						<Button
							variant="danger"
							onClick={() => mobile.changeServer()}
							data-testid="mobile-change-server"
						>
							{t("desktop.changeServer")}
						</Button>
					</div>
					<p className="text-faint text-xs">{t("desktop.changeServerHint")}</p>
				</div>
			)}
			{/* 退出登录只给纯浏览器：两种壳里的会话由壳的设备令牌换来，退出后壳会立刻再换一个，改用「更换服务器」 */}
			{host.desktop === null && host.mobile === null && (
				<div className="mt-4 border-border border-t pt-3">
					<Button
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
						{t("about.logout")}
					</Button>
					<ErrorText error={logout.error} />
				</div>
			)}
		</Card>
	);
}
