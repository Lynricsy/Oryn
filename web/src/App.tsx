import { lazy, type ReactNode, Suspense } from "react";
import { Navigate, Route, Routes } from "react-router";
import { ErrorBoundary } from "./components/ErrorBoundary";
import { AppShell } from "./components/layout/AppShell";
import { ConfirmHost } from "./components/ui/ConfirmHost";
import { ContextMenuHost } from "./components/ui/ContextMenu";
import { Spinner } from "./components/ui/primitives";
import { HomePage } from "./pages/HomePage";
import type { SettingsSection } from "./pages/SettingsPage";
import { ThreadPage } from "./pages/ThreadPage";

// Space 页与助手页（M9）：协调对话不常开，按需加载
const SpacePage = lazy(() =>
	import("./pages/SpacePage").then((m) => ({ default: m.SpacePage })),
);
const AssistantPage = lazy(() =>
	import("./pages/AssistantPage").then((m) => ({ default: m.AssistantPage })),
);
// 助手的电脑（M19）：状态、文件与桌面
const AssistantComputerPage = lazy(() =>
	import("./pages/AssistantComputerPage").then((m) => ({
		default: m.AssistantComputerPage,
	})),
);

// 项目分区（M9.6 / M24）：总览、新建与项目页
const ProjectsOverviewPage = lazy(() =>
	import("./pages/projects/ProjectsOverviewPage").then((m) => ({
		default: m.ProjectsOverviewPage,
	})),
);
const ProjectCreatePage = lazy(() =>
	import("./pages/projects/ProjectCreatePage").then((m) => ({
		default: m.ProjectCreatePage,
	})),
);
const ProjectPage = lazy(() =>
	import("./pages/ProjectsPage").then((m) => ({ default: m.ProjectPage })),
);
const ProjectThreadsPage = lazy(() =>
	import("./pages/projects/ProjectThreadsPage").then((m) => ({
		default: m.ProjectThreadsPage,
	})),
);

// 统计页（M10）：全宽数据页，图表与表格都在这一个分块里
const StatsPage = lazy(() =>
	import("./pages/StatsPage").then((m) => ({ default: m.StatsPage })),
);

// 收件箱：跨线程的「需要你」总览（数据取实时索引）
const InboxPage = lazy(() =>
	import("./pages/InboxPage").then((m) => ({ default: m.InboxPage })),
);

// Artifacts（M14）：整页查看器
const ArtifactPage = lazy(() =>
	import("./pages/ArtifactPage").then((m) => ({ default: m.ArtifactPage })),
);
// Artifacts（M14）：总览页与最近删除
const ArtifactsPage = lazy(() =>
	import("./pages/ArtifactsPage").then((m) => ({ default: m.ArtifactsPage })),
);
// Artifacts（M14）：私有发布页的登录中转（换授权后跳回发布页）
const ArtifactOpenPage = lazy(() =>
	import("./pages/ArtifactOpenPage").then((m) => ({
		default: m.ArtifactOpenPage,
	})),
);
// Boxes（M23）：总览页与单台 box
const BoxesPage = lazy(() =>
	import("./pages/BoxesPage").then((m) => ({ default: m.BoxesPage })),
);
const BoxPage = lazy(() =>
	import("./pages/BoxPage").then((m) => ({ default: m.BoxPage })),
);

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

/** 设置分区（路由与命令面板的「设置」组共用这一份） */
export const SETTINGS: SettingsSection[] = [
	"devices",
	"providers",
	"runners",
	"secrets",
	"browser-profiles",
	"git-identity",
	"github",
	"agent-skills",
	"agent-subagents",
	"agent-roles",
	"agent-prompts",
	"agent-commands",
	"agent-extensions",
	"agent-memory",
	"agent-ssh",
	"agent-composer",
	"selection-actions",
	"agent-config",
	"identity",
	"assistant",
	"appearance",
	"notifications",
	"shares",
	"artifacts",
	"automations",
	"desktop",
	"about",
];

/** 路由表（SPA）；外层由 main.tsx 提供 BrowserRouter 与 QueryClient。渲染异常由顶层错误边界接住，不整页白屏。 */
export function App(): ReactNode {
	return (
		<ErrorBoundary>
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
						<Route path="space/:spaceId" element={<SpacePage />} />
						<Route path="assistant" element={<AssistantPage />} />
						<Route
							path="assistant/computer"
							element={<AssistantComputerPage />}
						/>
						<Route path="assistant/:threadId" element={<AssistantPage />} />
						<Route path="projects" element={<ProjectsOverviewPage />} />
						<Route path="projects/new" element={<ProjectCreatePage />} />
						<Route path="projects/:projectId" element={<ProjectPage />} />
						<Route
							path="projects/:projectId/threads"
							element={<ProjectThreadsPage />}
						/>
						<Route
							path="stats"
							element={<Navigate to="/stats/usage" replace />}
						/>
						<Route path="stats/:tab" element={<StatsPage />} />
						<Route path="inbox" element={<InboxPage />} />
						<Route path="artifacts" element={<ArtifactsPage />} />
						<Route path="artifacts/trash" element={<ArtifactsPage trash />} />
						<Route path="artifacts/:artifactId" element={<ArtifactPage />} />
						<Route path="artifacts/open/:slug" element={<ArtifactOpenPage />} />
						<Route path="boxes" element={<BoxesPage />} />
						<Route path="boxes/:boxId" element={<BoxPage />} />
						{/* 项目管理搬到了项目分区（M9.6） */}
						<Route
							path="settings/projects"
							element={<Navigate to="/projects" replace />}
						/>
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
			<ContextMenuHost />
			<ConfirmHost />
		</ErrorBoundary>
	);
}
