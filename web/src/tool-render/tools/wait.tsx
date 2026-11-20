import type { ReactNode } from "react";
import type { ToolRenderer, ToolRenderProps } from "../types";
import { detailsRecord } from "../util";
import { ircRenderer } from "./irc";
import { jobRenderer } from "./job";

/** A consumed peer message renders as IRC; otherwise show the job snapshot. */
function Summary(props: ToolRenderProps): ReactNode {
	return detailsRecord(props.result)?.waited ? (
		<ircRenderer.Summary {...props} args={{ op: "wait" }} />
	) : (
		<jobRenderer.Summary {...props} args={{ poll: [] }} />
	);
}

/**
 * 只有输出段：参数被替换成 irc/job 的合成形态，不是模型真正给出的参数，不能当「输入」展示；
 * 动作（wait / poll）已在头部摘要里。
 */
function Result(props: ToolRenderProps): ReactNode {
	if (detailsRecord(props.result)?.waited) {
		return ircRenderer.Result ? (
			<ircRenderer.Result {...props} args={{ op: "wait" }} />
		) : null;
	}
	return jobRenderer.Result ? (
		<jobRenderer.Result {...props} args={{ poll: [] }} />
	) : null;
}

export const waitRenderer: ToolRenderer = { Summary, Result };
