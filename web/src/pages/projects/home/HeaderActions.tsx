/**
 * 项目页头的动作按钮（M24）：复制项目链接、复制克隆命令、项目设置（纯图标）与新建线程（图标加文字）。
 * 每个组件都只接收 `project`。复制成功时图标换成 ✓，1.2 秒后复原；失败时弹错误提示并附上原文。
 */
import { type ReactNode, useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { useNavigate } from "react-router";
import type { Project } from "../../../api/client";
import type { IconName } from "../../../components/ui/icons";
import { Button, IconButton } from "../../../components/ui/primitives";
import { copyToClipboard } from "../../../lib/clipboard";
import { shellArg } from "../../../lib/shell-quote";
import { showToast } from "../../../state/toasts";
import { uiStore } from "../../../state/ui";

/** 复制按钮的共同部分：成功后 1.2 秒内显示 ✓；失败时弹错误提示（缺省文案附上原文，`failText` 覆盖它）。 */
export function CopyIconButton({
	icon,
	label,
	text,
	failText,
}: {
	icon: IconName;
	label: string;
	text: string;
	failText?: string;
}): ReactNode {
	const { t } = useTranslation();
	const [copied, setCopied] = useState(false);
	useEffect(() => {
		if (!copied) return;
		const timer = setTimeout(() => setCopied(false), 1200);
		return () => clearTimeout(timer);
	}, [copied]);
	return (
		<IconButton
			icon={copied ? "check" : icon}
			label={label}
			onClick={async () => {
				if (await copyToClipboard(text)) setCopied(true);
				else
					showToast({
						text: failText ?? t("projectShell.copyFailed", { text }),
						tone: "err",
					});
			}}
		/>
	);
}

/** 复制项目主页的完整链接。 */
export function CopyLinkButton({ project }: { project: Project }): ReactNode {
	const { t } = useTranslation();
	return (
		<CopyIconButton
			icon="link"
			label={t("projectShell.copyLink")}
			text={`${location.origin}/projects/${project.id}`}
		/>
	);
}

/**
 * 复制克隆命令：托管项目是 `oryn clone <项目名>`（它登记凭据助手，用 oryn login 的设备令牌认证），
 * 外部仓库是 `git clone <仓库地址>`；没有仓库的项目不显示。
 */
export function CloneButton({ project }: { project: Project }): ReactNode {
	const { t } = useTranslation();
	if (project.hosted)
		return (
			<CopyIconButton
				icon="clone"
				label={t("projectShell.copyOrynClone")}
				text={`oryn clone ${shellArg(project.name)}`}
			/>
		);
	if (!project.repoUrl) return null;
	return (
		<CopyIconButton
			icon="clone"
			label={t("projectShell.copyClone")}
			text={`git clone ${project.repoUrl}`}
		/>
	);
}

/** 进项目设置：宽屏落到「概览」分组，窄屏是分组列表。 */
export function SettingsButton({ project }: { project: Project }): ReactNode {
	const { t } = useTranslation();
	const navigate = useNavigate();
	return (
		<IconButton
			icon="settings"
			label={t("projectShell.settings")}
			onClick={() => navigate(`/projects/${project.id}/settings`)}
		/>
	);
}

/**
 * 在这个项目里新建线程：把项目交给首屏后跳到 `/`。已归档的项目不显示。
 * 页头里窄屏只留图标；`labeled` 时（如空状态里）总带文字，免得正文里出现一个孤零零的「+」。
 */
export function NewThreadButton({
	project,
	labeled = false,
}: {
	project: Project;
	labeled?: boolean;
}): ReactNode {
	const { t } = useTranslation();
	const navigate = useNavigate();
	if (project.archivedAt) return null;
	return (
		<Button
			variant="primary"
			size="sm"
			icon="plus"
			aria-label={t("sidebar.newThread")}
			onClick={() => {
				uiStore.getState().seedHome({ text: "", projectId: project.id });
				navigate("/");
			}}
		>
			<span className={labeled ? undefined : "hidden sm:inline"}>
				{t("sidebar.newThread")}
			</span>
		</Button>
	);
}
