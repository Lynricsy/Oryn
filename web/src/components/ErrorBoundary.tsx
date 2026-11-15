/**
 * 顶层错误边界：渲染期（含 effect 清理）抛出的异常不再让整页白屏，而是换成一张居中卡片——
 * 哪里出了问题（标题）、为什么（错误原文，等宽小字）、下一步（「重新加载」）。外观沿用认证页的卡片。
 */
import { Component, type ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { AuthCard } from "./AuthCard";
import { Button } from "./ui/primitives";

function CrashCard({ error }: { error: unknown }): ReactNode {
	const { t } = useTranslation();
	const detail =
		error instanceof Error ? error.message || error.name : String(error);
	return (
		<AuthCard title={t("crash.title")}>
			<div role="alert" className="flex flex-col gap-3">
				<p className="text-muted text-sm">{t("crash.body")}</p>
				{detail && (
					<pre className="max-h-32 overflow-auto whitespace-pre-wrap break-words rounded-md bg-inset px-3 py-2 font-mono text-2xs text-faint">
						{detail}
					</pre>
				)}
				<Button
					variant="primary"
					icon="refresh"
					className="self-start"
					onClick={() => window.location.reload()}
				>
					{t("crash.reload")}
				</Button>
			</div>
		</AuthCard>
	);
}

export class ErrorBoundary extends Component<
	{ children: ReactNode },
	{ error: unknown; failed: boolean }
> {
	state = { error: null as unknown, failed: false };

	static getDerivedStateFromError(error: unknown) {
		return { error, failed: true };
	}

	render(): ReactNode {
		if (this.state.failed) return <CrashCard error={this.state.error} />;
		return this.props.children;
	}
}
