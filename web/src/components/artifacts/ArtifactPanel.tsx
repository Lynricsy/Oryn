/**
 * 右面板的 Artifacts 标签（计划 M14 2.4）：一行工具条（`h-11`，`role="toolbar"`）——左侧是 artifact 切换菜单
 * （类型图标 + 标题，列出该线程的全部），右侧是查看器的重新加载 / 在浏览器中打开与「放大查看」（跳到 `/artifacts/<id>`）；
 * 下面是 `ArtifactViewer variant="panel"`。工具条与查看器顶栏合成一行（切换菜单换掉查看器的图标与标题）。
 * 选中项（`artifactSel`）不在该线程的列表里时取最近更新的一个。
 */
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { useNavigate } from "react-router";
import { useThreadArtifacts } from "../../api/artifact-queries";
import { kindIcon } from "../../lib/artifacts/kinds";
import { uiStore, useUi } from "../../state/ui";
import { Icon } from "../ui/icons";
import { EmptyState, IconButton, Menu, Spinner } from "../ui/primitives";
import { ArtifactViewer } from "./ArtifactViewer";

export default function ArtifactPanel({
	threadId,
}: {
	threadId: string;
}): ReactNode {
	const { t } = useTranslation();
	const navigate = useNavigate();
	const list = useThreadArtifacts(threadId);
	const sel = useUi((s) => s.artifactSel[threadId]);
	const items = list.data ?? [];
	// 选中项不在列表里时取最近更新的一个（列表按更新时间倒序）；列表还没回来或为空时仍按选中项显示，
	// 查看器自己给出加载、已删除或不在了的状态
	const currentId = items.find((a) => a.id === sel)?.id ?? items[0]?.id ?? sel;

	if (!currentId) {
		if (list.isPending)
			return (
				<div className="flex h-full items-center justify-center text-faint">
					<Spinner />
				</div>
			);
		return <EmptyState icon="shapes" title={t("artifacts.panel.empty")} />;
	}

	const shown = items.find((a) => a.id === currentId);
	// 列表里还没有它（刚建好、还在加载）时用查看器自己的标题
	const lead = shown && (
		<Menu
			align="left"
			className="min-w-0"
			items={items.map((a) => ({
				key: a.id,
				label: a.title,
				icon: kindIcon(a.kind),
				checked: a.id === currentId,
				onSelect: () => uiStore.getState().openArtifact(threadId, a.id),
			}))}
			trigger={(p) => (
				<button
					type="button"
					data-testid="artifact-switch"
					title={t("artifacts.panel.switch")}
					className="flex h-7 min-w-0 max-w-full items-center gap-1.5 rounded-md px-1.5 font-semibold text-sm hover:bg-hover"
					{...p}
				>
					<Icon
						name={kindIcon(shown.kind)}
						size={15}
						className="shrink-0 text-faint"
					/>
					<span className="truncate">{shown.title}</span>
					<Icon name="chevronDown" size={12} className="shrink-0 text-faint" />
				</button>
			)}
		/>
	);
	return (
		<div data-testid="artifact-panel" className="h-full">
			<ArtifactViewer
				// 换 artifact 即重建：帧、提示条都属于那一个
				key={currentId}
				artifactId={currentId}
				variant="panel"
				lead={lead}
				actions={
					<IconButton
						icon="maximize"
						box="sm"
						size={14}
						label={t("artifacts.panel.expand")}
						onClick={() => navigate(`/artifacts/${currentId}`)}
					/>
				}
			/>
		</div>
	);
}
