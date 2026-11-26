/**
 * 设置 → 分享与 Artifacts：公开的线程、公开的文件（`SharesSettings`）→ 已发布的 Artifacts（含自动发布开关）→
 * Artifacts 页面能力授权。每项只出现一次。
 */
import type { ReactNode } from "react";
import { ArtifactsSection } from "../../components/settings/ArtifactsSection";
import { ArtifactPublishing } from "./ArtifactPublishing";
import SharesSettings from "./SharesSettings";

export default function SharingSettings(): ReactNode {
	return (
		<>
			<SharesSettings />
			<ArtifactPublishing />
			<ArtifactsSection />
		</>
	);
}
