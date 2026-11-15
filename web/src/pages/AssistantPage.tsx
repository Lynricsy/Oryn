/**
 * 全局助手页（M9，`Ctrl/Cmd+/`）：`/assistant` 跳到最近一个未归档的助手对话，没有就新建一个；
 * `/assistant/<id>` 是该对话（`ThreadScreen variant="coordinator"`）。页头是助手的圆形小头像 + 对话标题 +
 * 动作菜单（新对话、归档此对话）；切换对话在侧栏的助手分区里做（M9.6）。协调宿主不可用（`GET coordinator` 的
 * `available === false`）时不创建对话：页面主体是「全局助手暂不可用」+ 原因 + 指向设置 → 关于；
 * 已有对话仍可只读浏览（输入区禁用，托盘里同样提示）。
 */
import { type ReactNode, useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { Link, Navigate, useNavigate, useParams } from "react-router";
import { useCoordinatorStatus } from "../api/coordinator-queries";
import { useAssistantIdentity } from "../api/identity-queries";
import { createAssistantConversation } from "../api/space-queries";
import { CoordinatorHeader } from "../components/thread/CoordinatorHeader";
import { Callout, Sheet } from "../components/ui/data";
import { AssistantAvatar } from "../components/ui/IdentityAvatar";
import { Icon } from "../components/ui/icons";
import {
	describeError,
	IconButton,
	Menu,
	type MenuItem,
	Spinner,
} from "../components/ui/primitives";
import {
	useArchiveAssistantConversation,
	useAssistantConversations,
} from "../lib/assistant-conversations";
import { parseTitleCard } from "../lib/title";
import { useBreakpoint } from "../lib/viewport";
import { useIndex } from "../state/stores";
import { uiStore, useSidebarToggleShown } from "../state/ui";
import { type ScreenHeaderLayout, ThreadScreen } from "./ThreadPage";

function Unavailable({ reason }: { reason?: string }): ReactNode {
	const { t } = useTranslation();
	return (
		<Callout
			tone="warn"
			role="status"
			title={t("coordinator.unavailableTitle")}
		>
			<span className="block break-words">{reason}</span>
			<Link
				to="/settings/about"
				className="mt-1 inline-block text-accent-ink underline"
			>
				{t("coordinator.seeAbout")}
			</Link>
		</Callout>
	);
}

/** 页头标题前的小图形：设置了助手头像时是圆形头像，否则是 sparkle 图标（不跟随 Agent 身份）。 */
function AssistantMark({ size }: { size: number }): ReactNode {
	const { avatar } = useAssistantIdentity();
	return avatar.avatarIcon || avatar.avatarImage ? (
		<AssistantAvatar size={size} shape="circle" />
	) : (
		<Icon
			name="sparkle"
			size={16}
			aria-hidden
			className="shrink-0 text-faint"
		/>
	);
}

/** `/assistant`：选最近的对话，或在可用时新建一个。 */
function AssistantLanding(): ReactNode {
	const { t } = useTranslation();
	const navigate = useNavigate();
	const bp = useBreakpoint();
	const sidebarToggle = useSidebarToggleShown();
	const loaded = useIndex((s) => s.loaded);
	const latest = useAssistantConversations()[0];
	const { name } = useAssistantIdentity();
	const status = useCoordinatorStatus();
	const [error, setError] = useState<unknown>(null);
	const started = useRef(false);
	const available = status.data?.available;
	useEffect(() => {
		if (!loaded || latest || available !== true || started.current) return;
		started.current = true;
		createAssistantConversation().then(
			(th) => navigate(`/assistant/${th.id}`, { replace: true }),
			setError,
		);
	}, [loaded, latest, available, navigate]);
	if (latest) return <Navigate to={`/assistant/${latest.id}`} replace />;
	return (
		<Sheet
			inset={bp === "expanded"}
			className="flex h-full min-w-0 flex-1 flex-col"
		>
			<header className="flex h-12 shrink-0 items-center gap-2 px-3">
				{sidebarToggle && (
					<IconButton
						icon={bp === "expanded" ? "sidebar" : "menu"}
						label={t("nav.sidebar")}
						onClick={() => uiStore.getState().toggleSidebar()}
					/>
				)}
				<AssistantMark size={18} />
				<h1 className="min-w-0 truncate font-semibold text-sm">
					{name || t("assistant.title")}
				</h1>
			</header>
			<div className="flex flex-1 items-start justify-center overflow-y-auto p-4 sm:items-center">
				<div className="w-full max-w-md" data-testid="assistant-landing">
					{available === false ? (
						<Unavailable reason={status.data?.reason} />
					) : error ? (
						<Callout
							tone="err"
							role="alert"
							title={t("assistant.createFailed")}
						>
							{describeError(error, t)}
						</Callout>
					) : status.isError ? (
						<Callout tone="err" role="alert">
							{describeError(status.error, t)}
						</Callout>
					) : (
						<div className="flex justify-center text-muted">
							<Spinner size={18} />
						</div>
					)}
				</div>
			</div>
		</Sheet>
	);
}

function AssistantHeader({
	layout,
	threadId,
	available,
}: {
	layout: ScreenHeaderLayout;
	threadId: string;
	available: boolean;
}): ReactNode {
	const { t } = useTranslation();
	const navigate = useNavigate();
	const current = useIndex((s) => s.threads[threadId]);
	const archive = useArchiveAssistantConversation();
	const [error, setError] = useState<unknown>(null);
	const title = current
		? parseTitleCard(current.title).text || t("assistant.untitled")
		: t("assistant.title");
	const items: MenuItem[] = [
		{
			key: "new",
			icon: "plus",
			label: t("assistant.new"),
			disabled: !available,
			onSelect: () => {
				setError(null);
				createAssistantConversation().then(
					(th) => navigate(`/assistant/${th.id}`),
					setError,
				);
			},
		},
		{
			key: "archive",
			icon: "archive",
			label: t("assistant.archive"),
			onSelect: () => {
				setError(null);
				// 完成时按最新路由决定是否回 landing（等待期间用户可能已切到别的对话），并同步已归档列表
				archive(threadId).catch(setError);
			},
		},
	];
	return (
		<CoordinatorHeader
			layout={layout}
			title={
				<>
					<AssistantMark size={18} />
					<h1 className="min-w-0 truncate font-semibold text-sm tracking-tight">
						{title}
					</h1>
				</>
			}
			actions={
				<>
					{error ? (
						<span className="max-w-48 truncate text-err text-xs" role="alert">
							{describeError(error, t)}
						</span>
					) : null}
					<Menu
						items={items}
						trigger={(p) => (
							<IconButton icon="more" label={t("assistant.actions")} {...p} />
						)}
					/>
				</>
			}
		/>
	);
}

export function AssistantPage(): ReactNode {
	const { threadId } = useParams();
	const status = useCoordinatorStatus();
	const unavailable = status.data?.available === false;
	if (!threadId) return <AssistantLanding />;
	return (
		<div data-testid="assistant-page" className="flex h-full min-w-0 flex-1">
			<ThreadScreen
				threadId={threadId}
				variant="coordinator"
				disabled={unavailable}
				dockNotice={
					unavailable ? (
						<div className="px-3 pt-3">
							<Unavailable reason={status.data?.reason} />
						</div>
					) : undefined
				}
				header={(layout) => (
					<AssistantHeader
						layout={layout}
						threadId={threadId}
						available={!unavailable}
					/>
				)}
			/>
		</div>
	);
}
