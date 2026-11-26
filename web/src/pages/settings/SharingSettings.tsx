/** 设置 → 分享与 Artifacts：公开的线程与文件、自动发布、Artifacts 页面能力授权。 */
import type { ReactNode } from "react";
import { ArtifactsSection } from "../../components/settings/ArtifactsSection";
import SharesSettings from "./SharesSettings";

export default function SharingSettings(): ReactNode {
	return (
		<>
			<SharesSettings />
			<ArtifactsSection />
		</>
	);
}
