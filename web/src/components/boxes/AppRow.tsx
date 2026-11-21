/**
 * box 页 APPS 分组的一行（M23 步骤 7.4）：首字母色块（有预览时是悬停预览的触发器）、名字、地址、私有徽标、部署中的
 * 脉冲点、一行元信息（有 live 时是 `repo · branch · 短 sha · 部署于 <相对时间>`，没有时是「首次部署进行中」）、
 * 服务异常的红标签、行尾「新线程」（指针悬停或聚焦整行时出现，触屏常显）与行菜单。
 * 介绍页的演示（`BoxesHeroPreview`）也用它，那时不给菜单与回调。
 */
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import type { BoxApp } from "../../api/box-queries";
import { formatRelative } from "../../lib/format";
import { Badge, ProjectBadge, StatusDot } from "../ui/data";
import { IconButton, Menu, type MenuItem } from "../ui/primitives";
import { AppPreview } from "./AppPreview";

export function AppRow({
	app,
	menu,
	onNewThread,
	threadDisabled,
	preview = false,
}: {
	app: BoxApp;
	menu?: MenuItem[];
	onNewThread?: () => void;
	/** 不能开线程的原因（没有 live、box 停着）；给了就禁用「新线程」并作悬停提示 */
	threadDisabled?: string;
	/** 启用悬停预览（调用方已判断过 `preview && url` 与能否悬停） */
	preview?: boolean;
}): ReactNode {
	const { t } = useTranslation();
	const unhealthy = app.services.some(
		(s) => s.status === "failed" || s.status === "unhealthy",
	);
	const url = app.url?.replace(/^https?:\/\//, "");
	return (
		<div
			data-testid="box-app-row"
			data-app={app.name}
			className="group flex min-h-14 items-center gap-3 rounded-md px-2 py-2 transition-colors duration-150 hover:bg-hover focus-within:bg-hover"
		>
			{preview ? (
				<AppPreview app={app} />
			) : (
				<ProjectBadge name={app.name} size={28} />
			)}
			<div className="flex min-w-0 flex-1 flex-col gap-0.5">
				<div className="flex min-w-0 items-center gap-1.5">
					<span className="truncate font-medium text-fg text-sm">
						{app.name}
					</span>
					{!app.public && <Badge>{t("boxes.apps.private")}</Badge>}
					{app.deploying && (
						<StatusDot tone="warn" pulse label={t("boxes.apps.deploying")} />
					)}
					{app.url && (
						<a
							href={app.url}
							target="_blank"
							rel="noreferrer"
							className="min-w-0 truncate font-mono text-accent-ink text-xs hover:underline"
						>
							{url}
						</a>
					)}
				</div>
				<div className="truncate text-faint text-xs">
					{app.live
						? [
								app.live.repo,
								app.live.branch,
								app.live.commit.slice(0, 7),
								t("boxes.apps.deployed", {
									when: formatRelative(app.live.activatedAt),
								}),
							].join(" · ")
						: t("boxes.apps.firstDeploy")}
				</div>
			</div>
			{unhealthy && <Badge tone="err">{t("boxes.apps.unhealthy")}</Badge>}
			{onNewThread && (
				<span
					title={threadDisabled}
					className="pointer-coarse:opacity-100 opacity-0 transition-opacity duration-150 focus-within:opacity-100 group-hover:opacity-100 group-focus-within:opacity-100"
				>
					<IconButton
						icon="plus"
						box="sm"
						label={t("boxes.apps.newThread")}
						disabled={Boolean(threadDisabled)}
						onClick={onNewThread}
					/>
				</span>
			)}
			{menu && (
				<Menu
					items={menu}
					trigger={(p) => (
						<IconButton
							{...p}
							icon="more"
							box="sm"
							label={t("boxes.apps.menu", { app: app.name })}
						/>
					)}
				/>
			)}
		</div>
	);
}
