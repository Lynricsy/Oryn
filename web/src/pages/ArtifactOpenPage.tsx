/**
 * 打开自己的私有发布页（`/artifacts/open/:slug`，计划 M14 8.2）。发布页对没有授权的访客 302 到这里；
 * AppShell 的登录守卫保证已登录，这里向控制面换一个带授权段的地址后 `location.replace` 过去
 * （不留历史条目：返回键不会再回到这个中转页）。发布记录不存在时显示「这个页面已不在线上」。
 */
import { type ReactNode, useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { Navigate, useParams } from "react-router";
import { ApiError, api, unwrap } from "../api/client";
import {
	Button,
	describeError,
	EmptyState,
	Spinner,
} from "../components/ui/primitives";

type OpenState =
	| { kind: "loading" }
	| { kind: "gone" }
	| { kind: "error"; error: unknown };

/** 整页跳转（测试里替换）：发布页不在 SPA 路由里。 */
export const publishedPage = {
	replace(url: string): void {
		window.location.replace(url);
	},
};

export function ArtifactOpenPage(): ReactNode {
	const { slug } = useParams();
	// 「重试」换一个 key 重新挂载，请求随之重发
	const [attempt, setAttempt] = useState(0);
	if (!slug) return <Navigate to="/" replace />;
	return (
		<OpenScreen
			key={`${slug}:${attempt}`}
			slug={slug}
			onRetry={() => setAttempt((n) => n + 1)}
		/>
	);
}

function OpenScreen({
	slug,
	onRetry,
}: {
	slug: string;
	onRetry(): void;
}): ReactNode {
	const { t } = useTranslation();
	const [state, setState] = useState<OpenState>({ kind: "loading" });

	useEffect(() => {
		let live = true;
		unwrap(
			api.POST("/api/v1/artifact-publications/{slug}/grant", {
				params: { path: { slug } },
			}),
		).then(
			({ url }) => {
				if (live) publishedPage.replace(url);
			},
			(error: unknown) => {
				if (!live) return;
				setState(
					error instanceof ApiError && error.status === 404
						? { kind: "gone" }
						: { kind: "error", error },
				);
			},
		);
		return () => {
			live = false;
		};
	}, [slug]);
	return (
		<div className="flex h-full min-h-0 items-center justify-center">
			{state.kind === "loading" && (
				<div
					role="status"
					aria-label={t("artifacts.open.loading")}
					className="text-faint"
				>
					<Spinner size={18} />
				</div>
			)}
			{state.kind === "gone" && (
				<EmptyState icon="shapes" title={t("artifacts.open.gone")}>
					{t("artifacts.open.goneHint")}
				</EmptyState>
			)}
			{state.kind === "error" && (
				<EmptyState icon="shapes" title={t("artifacts.open.failed")}>
					<div className="flex flex-col items-center gap-3">
						<span>{describeError(state.error, t)}</span>
						<Button size="sm" icon="restart" onClick={onRetry}>
							{t("artifacts.open.retry")}
						</Button>
					</div>
				</EmptyState>
			)}
		</div>
	);
}
