/** 标签子页（M24 阶段 ②，`/projects/<id>/tags`）：与分支子页同形，见 `RefsPage`。 */
import type { ReactNode } from "react";
import { RefsPage } from "./BranchesPage";

export function TagsPage(): ReactNode {
	return <RefsPage kind="tag" />;
}
