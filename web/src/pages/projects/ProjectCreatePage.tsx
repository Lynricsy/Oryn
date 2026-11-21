/** 新建项目（M24，`/projects/new`）：精简表单（名称、图标、描述、仓库、基线分支），创建后进入新项目的主页，取消回到项目总览。 */
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { useNavigate } from "react-router";
import { Card } from "../../components/ui/data";
import { EMPTY_PROJECT, ProjectEditor } from "./ProjectEditor";
import { ProjectShell } from "./ProjectShell";

export function ProjectCreatePage(): ReactNode {
	const { t } = useTranslation();
	const navigate = useNavigate();
	return (
		<ProjectShell crumbs={[{ label: t("projects.new") }]}>
			<Card>
				<ProjectEditor
					section="create"
					initial={EMPTY_PROJECT}
					onSaved={(p) => navigate(`/projects/${p.id}`, { replace: true })}
					onCancel={() => navigate("/projects")}
				/>
			</Card>
		</ProjectShell>
	);
}
