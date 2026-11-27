/**
 * 视图（M33）的两个直出渲染器：`render_ui`（声明式块）与 `render_html`（沙箱帧）。不套工具卡外壳、不随批次开合。
 *
 * 显示与可用性按 docs/plan-m33.md 的 C3 / C10：
 * - 结果 `isError` 且没有 `details.view`（参数没通过校验、会话中断补的合成结果）→ 只留一行安静的提示；
 * - 规格来源：结果的 `details.view.spec` → `tool_execution_start` 的已修复参数 → toolCall 块里的参数（流式中残缺）；
 * - 可作答：宿主给了 `submit`（只读页不给）、toolCallId 能放进回执、参数不在流式，且调用已结束或正挂起等待作答；
 *   参数流完、尚未开始执行，或执行中但没有挂起的带标记请求时，控件禁用。
 * 渲染器与宿主无关：作答、挂起状态、帧都经 `host.view` 注入。
 */
import { type ReactNode, useMemo } from "react";
import { useTranslation } from "react-i18next";
import { HtmlView } from "../../components/view/HtmlView";
import { UiView } from "../../components/view/UiView";
import {
	HTML_BLOCK,
	isAnswerableCallId,
	type ViewAnswer,
} from "../../lib/view/answer";
import { normalizeHtmlSpec, normalizeUiSpec } from "../../lib/view/spec";
import type { ToolInlineProps, ToolRenderer, ToolRenderProps } from "../types";
import { detailsRecord, isRecord, resultTextOf, str } from "../util";

const NO_ANSWERS: ReadonlyMap<string, ViewAnswer> = new Map();

interface ViewCall {
	/** 不显示视图，只留一行提示。 */
	failed: boolean;
	/** 规格来源（未归一化）：结果的 `details.view.spec`，没有时取参数（已修复的优先）。 */
	source: unknown;
	/** 参数（已修复的优先）：HTML 视图结果里的 spec 不含标题，标题取自参数。 */
	args: unknown;
	answers: ReadonlyMap<string, ViewAnswer>;
	/** 正挂起等待作答（`await`）。 */
	awaiting: boolean;
	interactive: boolean;
	onAnswer?: (answer: ViewAnswer, text: string) => Promise<void>;
	/** 不是只读页（宿主能提交作答）。 */
	owner: boolean;
}

function viewCall(p: ToolInlineProps): ViewCall {
	const view = detailsRecord(p.result)?.view;
	const host = p.host?.view;
	const ended = p.result !== undefined;
	const awaiting = !ended && host?.awaiting(p.toolCallId) === true;
	const submit = host?.submit;
	const canAnswer = submit !== undefined && isAnswerableCallId(p.toolCallId);
	return {
		failed: p.result?.isError === true && !isRecord(view),
		source:
			isRecord(view) && isRecord(view.spec)
				? view.spec
				: (p.liveArgs ?? p.args),
		args: p.liveArgs ?? p.args,
		answers: host?.answers(p.toolCallId) ?? NO_ANSWERS,
		awaiting,
		interactive: canAnswer && !p.streaming && (ended || awaiting),
		onAnswer:
			canAnswer && submit
				? (answer, text) => submit(p.toolCallId, answer, text)
				: undefined,
		owner: submit !== undefined,
	};
}

/** 视图没有生成（参数没通过校验等）：一行安静的提示，悬停看原因。 */
function ViewFailed({ p }: { p: ToolInlineProps }): ReactNode {
	const { t } = useTranslation();
	const reason = resultTextOf(p.result).trim();
	return (
		<div
			ref={p.rootRef}
			data-testid="view-failed"
			data-view-call={p.toolCallId}
			className="text-faint text-xs"
			title={reason ? reason.slice(0, 600) : undefined}
		>
			{t("view.state.failed")}
		</div>
	);
}

/** 参数还没流到第一个完整的块。 */
function ViewPending({ p }: { p: ToolInlineProps }): ReactNode {
	const { t } = useTranslation();
	return (
		<div
			ref={p.rootRef}
			data-testid="view-pending"
			data-view-call={p.toolCallId}
			className="h-16 animate-pulse rounded-lg border border-border bg-inset px-3 py-2 text-faint text-xs"
		>
			{t("view.state.loading")}
		</div>
	);
}

/** 挂起等待作答时视图下方的一行提示。 */
function AwaitNote(): ReactNode {
	const { t } = useTranslation();
	return (
		<p data-testid="view-awaiting" className="mt-1.5 text-2xs text-muted">
			{t("view.state.awaiting")}
		</p>
	);
}

function RenderUiInline(p: ToolInlineProps): ReactNode {
	const call = viewCall(p);
	const spec = useMemo(() => normalizeUiSpec(call.source), [call.source]);
	if (call.failed) return <ViewFailed p={p} />;
	if (!spec)
		return p.result || p.started ? <ViewFailed p={p} /> : <ViewPending p={p} />;
	return (
		<div
			ref={p.rootRef}
			data-testid="view-ui"
			data-view-call={p.toolCallId}
			className="min-w-0"
		>
			<UiView
				spec={spec}
				toolCallId={p.toolCallId}
				answers={call.answers}
				interactive={call.interactive}
				streaming={p.streaming}
				onAnswer={call.onAnswer}
			/>
			{call.awaiting && call.interactive && <AwaitNote />}
		</div>
	);
}

function RenderHtmlInline(p: ToolInlineProps): ReactNode {
	const call = viewCall(p);
	// render_html 的 `details.view.spec` 只有 `{ libs, height }`，标题取自参数（参数修复不改字符串）
	const spec = normalizeHtmlSpec(
		call.source !== call.args && isRecord(call.args) && isRecord(call.source)
			? { title: call.args.title, ...call.source }
			: call.source,
	);
	if (call.failed) return <ViewFailed p={p} />;
	// 帧内容由服务端从转录里取：参数通过校验（开始执行）或调用已结束才挂。公开端点只服务成功结束的调用，
	// 只读页上出错结束（中止）的调用不挂帧
	const mount =
		!p.streaming &&
		(p.started === true ||
			(p.result !== undefined && (p.result.isError !== true || call.owner)));
	return (
		<div
			ref={p.rootRef}
			data-testid="view-html"
			data-view-call={p.toolCallId}
			className="min-w-0"
		>
			<HtmlView
				toolCallId={p.toolCallId}
				spec={spec}
				mount={mount}
				openFrame={p.host?.view?.openFrame}
				answered={call.answers.has(HTML_BLOCK)}
				interactive={call.interactive}
				onAnswer={call.onAnswer}
			/>
			{call.awaiting && call.interactive && <AwaitNote />}
		</div>
	);
}

/** 头部摘要（直出时不显示；其它列出工具调用的地方用）：视图标题。 */
function Summary({ args }: ToolRenderProps): ReactNode {
	const title = str(args.title);
	return title ? <span className="tv-muted">{title}</span> : null;
}

export const renderUiRenderer: ToolRenderer = {
	Summary,
	Inline: RenderUiInline,
};

export const renderHtmlRenderer: ToolRenderer = {
	Summary,
	Inline: RenderHtmlInline,
};
