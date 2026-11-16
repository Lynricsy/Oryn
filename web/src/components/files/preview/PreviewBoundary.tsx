/**
 * 预览错误边界：预览组件渲染时抛出的异常（文件结构损坏、第三方解析库的边界情况）只替换预览区域，
 * 不冒泡到顶层错误边界，顶栏的返回、切换源码与下载仍然可用。由调用方以文件路径作 `key`，换文件即重置。
 */
import { Component, type ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { ErrorText } from "../../ui/primitives";

function PreviewCrash({ error }: { error: unknown }): ReactNode {
	const { t } = useTranslation();
	return (
		<div role="alert" className="flex flex-col gap-1.5 p-3">
			<p className="text-muted text-sm">{t("files.preview.crashed")}</p>
			<ErrorText error={error} />
		</div>
	);
}

export class PreviewBoundary extends Component<
	{ children: ReactNode },
	{ error: unknown; failed: boolean }
> {
	state = { error: null as unknown, failed: false };

	static getDerivedStateFromError(error: unknown) {
		return { error, failed: true };
	}

	render(): ReactNode {
		if (this.state.failed) return <PreviewCrash error={this.state.error} />;
		return this.props.children;
	}
}
