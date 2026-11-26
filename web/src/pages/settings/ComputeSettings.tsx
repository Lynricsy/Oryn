/** 设置 → 算力：Provider 与 Runner（同为「新建 → 一次性令牌 → 列表」）。 */
import type { ReactNode } from "react";
import ProvidersSettings from "./ProvidersSettings";
import RunnersSettings from "./RunnersSettings";

export default function ComputeSettings(): ReactNode {
	return (
		<>
			<ProvidersSettings />
			<RunnersSettings />
		</>
	);
}
