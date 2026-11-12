import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { BrowserRouter } from "react-router";
import { App } from "./App";
import "./i18n";
import "./index.css";
import { installViewportTracking } from "./lib/viewport";

async function boot() {
	const root = document.getElementById("root");
	if (!root) throw new Error("缺少 #root 挂载点");
	// mock 模式（VITE_MOCK=1）：浏览器内假 REST + 假 WebSocket，回放 fakeomp 风格的帧序列；生产构建会剔除此分支
	if (import.meta.env.VITE_MOCK === "1") {
		const { installMock } = await import("./mock");
		installMock();
	}
	installViewportTracking();
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
