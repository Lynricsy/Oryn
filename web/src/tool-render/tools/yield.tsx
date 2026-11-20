/** `yield` — structured data handed back by a subagent at the end of its run. */
import type { ReactNode } from "react";
import { Output, ResultText } from "../parts";
import type { ToolRenderer, ToolRenderProps } from "../types";
import { argsDigest } from "../util";

function Summary({ args }: ToolRenderProps): ReactNode {
	return <span>{argsDigest(args.data ?? args)}</span>;
}

function Input({ args }: ToolRenderProps): ReactNode {
	if (args.data === undefined) return null;
	let dataText = "";
	try {
		dataText = JSON.stringify(args.data, null, 2) ?? "";
	} catch {
		dataText = String(args.data);
	}
	if (!dataText) return null;
	return <Output text={dataText} lang="json" variant="code" maxLines={12} />;
}

function Result({ result }: ToolRenderProps): ReactNode {
	return <ResultText result={result} maxLines={6} />;
}

export const yieldRenderer: ToolRenderer = { Summary, Input, Result };
