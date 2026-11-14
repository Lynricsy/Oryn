// 字标「Oryn」专用；按 unicode-range 只会下载 latin 分片
import "@fontsource-variable/fraunces/opsz.css";
import "@fontsource-variable/jetbrains-mono/index.css";
import "@fontsource-variable/mona-sans/index.css";
import "@fontsource-variable/noto-sans-sc/index.css";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { BrowserRouter } from "react-router";
import { App } from "./App";
import "./i18n";
import "./index.css";
import { installDeviceCorners } from "./lib/device-corners";
import { host } from "./lib/host";
import { installSquircle } from "./lib/squircle";
import { installViewportTracking } from "./lib/viewport";
import { installTheme } from "./state/theme";

async function boot() {
	const root = document.getElementById("root");
	// 预取等宽字体：xterm 按首个可用字体测量单元格
	void document.fonts?.load('13px "JetBrains Mono Variable"');
	if (!root) throw new Error("缺少 #root 挂载点");
	// mock 模式（VITE_MOCK=1）：浏览器内假 REST + 假 WebSocket，回放 fakeomp 风格的帧序列；生产构建会剔除此分支
	if (import.meta.env.VITE_MOCK === "1") {
		const { installMock } = await import("./mock");
		installMock();
	}
	installViewportTracking();
	// 移动壳：贴边浮层的圆角与屏幕圆角同心（其它宿主无操作）
	installDeviceCorners(host, document.documentElement);
	installTheme();
	// 不支持 corner-shape 的引擎：挂载前启用超椭圆角形降级（原生支持时立即返回）
	await installSquircle();
	const queryClient = new QueryClient({
		defaultOptions: { queries: { refetchOnWindowFocus: false } },
	});
	createRoot(root).render(
		<StrictMode>
			<QueryClientProvider client={queryClient}>
				<BrowserRouter>
					<App />
				</BrowserRouter>
			</QueryClientProvider>
		</StrictMode>,
	);
}

void boot();
