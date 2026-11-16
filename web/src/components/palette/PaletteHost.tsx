/**
 * 命令面板的挂载点（主包里）：只在打开时按需加载面板分块；分块加载期间不画任何东西，加载完即出现。
 */
import { lazy, type ReactNode, Suspense } from "react";
import { usePaletteOpen } from "./palette-state";

const CommandPalette = lazy(() =>
	import("./CommandPalette").then((m) => ({ default: m.CommandPalette })),
);

export function PaletteHost(): ReactNode {
	if (!usePaletteOpen()) return null;
	return (
		<Suspense fallback={null}>
			<CommandPalette />
		</Suspense>
	);
}
