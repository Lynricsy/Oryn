/**
 * 四个分区侧栏的滑动轨道：四个 pane 并排同时挂载，切换只改轨道的 `translate`（`--section-i`，200ms；减少动态时
 * 轨道不滑、新分区 150ms 淡入，见 index.css 的 `.section-track`）。DOM 不卸载，所以滚动位置、分组展开都保留。
 * 非当前的 pane 带 `inert` + `aria-hidden`：不可聚焦、读屏忽略。
 */
import type { CSSProperties, ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { SECTION_ORDER, type SectionId } from "../../lib/sections";
import { AssistantPane } from "./AssistantPane";
import { MorePane } from "./MorePane";
import { ProjectsPane } from "./ProjectsPane";
import { ThreadsPane } from "./ThreadsPane";
import { useSection } from "./useSection";

function Pane({ id }: { id: SectionId }): ReactNode {
	switch (id) {
		case "threads":
			return <ThreadsPane />;
		case "assistant":
			return <AssistantPane />;
		case "projects":
			return <ProjectsPane />;
		case "more":
			return <MorePane />;
	}
}

export function SectionPanes(): ReactNode {
	const { t } = useTranslation();
	const section = useSection();
	return (
		<div
			data-testid="section-panes"
			className="relative min-h-0 flex-1 overflow-hidden"
		>
			<div
				className="section-track"
				style={
					{ "--section-i": SECTION_ORDER.indexOf(section) } as CSSProperties
				}
			>
				{SECTION_ORDER.map((id) => {
					const on = id === section;
					return (
						<section
							key={id}
							data-testid="section-pane"
							data-section={id}
							data-active={on ? "" : undefined}
							inert={!on}
							aria-hidden={on ? undefined : true}
							aria-label={t(`tabBar.${id}`)}
							className="section-pane flex min-w-0 flex-col"
						>
							<Pane id={id} />
						</section>
					);
				})}
			</div>
		</div>
	);
}
