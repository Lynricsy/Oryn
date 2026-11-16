/**
 * 首屏「最近的 Artifacts」（计划 M14 2.5）：输入框下方、有 artifact 时显示最近打开（`max(openedAt, updatedAt)`）的
 * 至多 6 个 `ArtifactCard form="row"`，点击进入 `/artifacts/<id>`；没有（或还在加载、读取失败）时不渲染任何东西。
 */
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { Link, useNavigate } from "react-router";
import { useArtifacts } from "../../api/artifact-queries";
import { ArtifactCard } from "./ArtifactCard";
import { sortArtifacts } from "./artifacts-model";

export const RECENT_ON_HOME = 6;

export function RecentArtifacts(): ReactNode {
	const { t } = useTranslation();
	const navigate = useNavigate();
	const list = useArtifacts();
	const recent = sortArtifacts(list.data ?? [], "recent").slice(
		0,
		RECENT_ON_HOME,
	);
	if (recent.length === 0) return null;
	return (
		<section
			aria-labelledby="home-recent-artifacts"
			data-testid="home-recent-artifacts"
			className="mt-6 flex flex-col gap-2"
		>
			<div className="flex items-center justify-between gap-2 px-1">
				<h2
					id="home-recent-artifacts"
					className="font-medium text-muted text-xs"
				>
					{t("artifacts.home.recent")}
				</h2>
				<Link
					to="/artifacts"
					className="rounded text-faint text-xs hover:text-fg"
				>
					{t("artifacts.home.all")}
				</Link>
			</div>
			<ul className="grid grid-cols-1 gap-1.5 sm:grid-cols-2">
				{recent.map((a) => (
					<li key={a.id} className="min-w-0">
						<ArtifactCard
							artifact={a}
							form="row"
							onOpen={(id) => navigate(`/artifacts/${id}`)}
						/>
					</li>
				))}
			</ul>
		</section>
	);
}
