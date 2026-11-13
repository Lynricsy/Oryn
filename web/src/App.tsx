import { lazy, type ReactNode, Suspense } from "react";
import { Navigate, Route, Routes } from "react-router";
import { AppShell } from "./components/layout/AppShell";
import { Spinner } from "./components/ui/primitives";
import { HomePage } from "./pages/HomePage";
import type { SettingsSection } from "./pages/SettingsPage";
import { ThreadPage } from "./pages/ThreadPage";

// 不常用的页面（认证、设置、匿名分享页）按需加载
const SetupPage = lazy(() =>
	import("./pages/AuthPages").then((m) => ({ default: m.SetupPage })),
);
const LoginPage = lazy(() =>
	import("./pages/AuthPages").then((m) => ({ default: m.LoginPage })),
);
const PairPage = lazy(() =>
	import("./pages/AuthPages").then((m) => ({ default: m.PairPage })),
);
const SettingsPage = lazy(() =>
	import("./pages/SettingsPage").then((m) => ({ default: m.SettingsPage })),
);
const SharePage = lazy(() =>
	import("./pages/SharePage").then((m) => ({ default: m.SharePage })),
);

const SETTINGS: SettingsSection[] = [
	"devices",
	"providers",
	"projects",
	"secrets",
	"git-identity",
	"agent-config",
	"notifications",
	"shares",
	"appearance",
	"desktop",
	"about",
];

/** 路由表（SPA）；外层由 main.tsx 提供 BrowserRouter 与 QueryClient。 */
export function App(): ReactNode {
	return (
		<Suspense
			fallback={
				<div className="flex h-dvh items-center justify-center text-muted">
					<Spinner size={20} />
				</div>
			}
		>
			<Routes>
				<Route path="/setup" element={<SetupPage />} />
				<Route path="/login" element={<LoginPage />} />
				<Route path="/pair" element={<PairPage />} />
				{/* 匿名只读分享页：在 AppShell（鉴权守卫、私有 realtime）之外 */}
				<Route path="/s/:slug" element={<SharePage />} />
				<Route element={<AppShell />}>
					<Route index element={<HomePage />} />
					<Route path="t/:threadId" element={<ThreadPage />} />
					<Route
						path="settings"
						element={<Navigate to="/settings/devices" replace />}
					/>
					{SETTINGS.map((s) => (
						<Route
							key={s}
							path={`settings/${s}`}
							element={<SettingsPage section={s} />}
						/>
					))}
				</Route>
				<Route path="*" element={<Navigate to="/" replace />} />
			</Routes>
		</Suspense>
	);
}
