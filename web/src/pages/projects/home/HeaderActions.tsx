/**
 * 项目页头的动作按钮（M24）。每个组件都只接收 `project`。
 */
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { useNavigate } from "react-router";
import type { Project } from "../../../api/client";
import { Button } from "../../../components/ui/primitives";
import { uiStore } from "../../../state/ui";

/** 在这个项目里新建线程：把项目交给首屏后跳到 `/`。已归档的项目不显示。 */
export function NewThreadButton({ project }: { project: Project }): ReactNode {
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
			<span className="hidden sm:inline">{t("sidebar.newThread")}</span>
		</Button>
	);
}
