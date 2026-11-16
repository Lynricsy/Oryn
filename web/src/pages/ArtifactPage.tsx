/**
 * 整页的 artifact 查看器（`/artifacts/:artifactId`，计划 M14 2.3）：进入时记一次「打开过」。
 * 返回：站内有上一页就后退，否则（直接打开的链接）回首页。
 */
import { type ReactNode, useEffect } from "react";
import { Navigate, useLocation, useNavigate, useParams } from "react-router";
import { useMarkArtifactOpened } from "../api/artifact-queries";
import { ArtifactViewer } from "../components/artifacts/ArtifactViewer";
import { Sheet } from "../components/ui/data";
import { useBreakpoint } from "../lib/viewport";

export function ArtifactPage(): ReactNode {
	const { artifactId } = useParams();
	if (!artifactId) return <Navigate to="/" replace />;
	return <ArtifactScreen key={artifactId} artifactId={artifactId} />;
}

function ArtifactScreen({ artifactId }: { artifactId: string }): ReactNode {
	const bp = useBreakpoint();
	const navigate = useNavigate();
	const location = useLocation();
	const { mutate: markOpened } = useMarkArtifactOpened();
	useEffect(() => {
		markOpened(artifactId);
	}, [artifactId, markOpened]);
	// 首个历史条目的 key 是 "default"：没有站内上一页
	const canGoBack = location.key !== "default";
	return (
		<Sheet inset={bp === "expanded"} className="flex h-full min-h-0 flex-col">
			<ArtifactViewer
				artifactId={artifactId}
				variant="page"
				onBack={() => (canGoBack ? navigate(-1) : navigate("/"))}
			/>
		</Sheet>
	);
}
