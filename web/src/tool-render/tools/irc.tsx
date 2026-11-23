/**
 * IRC 消息的渲染（旧转录）：registry 没有 `irc` 工具，现行 Core 也没有；这里只服务 `wait` 卡片——
 * 旧版 `wait` 取到对端消息时 details 带 `waited`（消息数组；超时为 null），wait.tsx 借用这里的 Summary / Result。
 * 消息画成信件：发件人首字母标记 + 名字、回复标记、正文。
 */
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { Badge, Note, ResultText } from "../parts";
import type { ToolRenderer, ToolRenderProps } from "../types";
import { detailsRecord, isRecord, normalizeWs, str, truncate } from "../util";

interface IrcMsg {
	from: string;
	body: string;
	replyTo?: string;
}

function parseMsgs(value: unknown): IrcMsg[] {
	if (!Array.isArray(value)) return [];
	const out: IrcMsg[] = [];
	for (const item of value) {
		if (!isRecord(item)) continue;
		const from = str(item.from);
		const body = str(item.body);
		if (from === null || body === null) continue;
		out.push({ from, body, replyTo: str(item.replyTo) ?? undefined });
	}
	return out;
}

/** 一封信：发件人行（首字母标记 + 名字 + 回复标记）+ 正文。 */
function Letter({ msg }: { msg: IrcMsg }): ReactNode {
	const { t } = useTranslation();
	const letter = (/[\p{L}\p{N}]/u.exec(msg.from)?.[0] ?? "?").toUpperCase();
	return (
		<div className="tv-irc-msg">
			<div className="tv-irc-msg-head">
				<span className="tv-irc-mark" aria-hidden="true">
					{letter}
				</span>
				<span className="tv-irc-from">{msg.from}</span>
				{msg.replyTo !== undefined && (
					<span className="tv-irc-tag">{t("toolRender.irc.reply")}</span>
				)}
			</div>
			<div className="tv-irc-body">{msg.body}</div>
		</div>
	);
}

/** 头部摘要：等到的发件人（一封时附正文开头），没等到时写等谁。 */
function Summary({ args, result }: ToolRenderProps): ReactNode {
	const { t } = useTranslation();
	const waited = parseMsgs(detailsRecord(result)?.waited);
	const op = (
		<Badge tone={result?.isError ? "err" : undefined}>
			{t("toolRender.irc.wait")}
		</Badge>
	);
	if (waited.length === 0)
		return (
			<>
				{op}
				<span className="tv-pattern">
					← {str(args.from) ?? t("toolRender.irc.anyone")}
				</span>
			</>
		);
	const senders = [...new Set(waited.map((msg) => msg.from))].join(", ");
	return (
		<>
			{op}
			<span className="tv-pattern">← {senders}</span>
			<span className="tv-muted">
				{waited.length === 1
					? truncate(normalizeWs(waited[0]?.body ?? ""), 80)
					: t("toolRender.irc.messages", { count: waited.length })}
			</span>
		</>
	);
}

/** 输出：等到的信件；超时给提醒；没有结构化内容（或出错）时给结果文本。 */
function Result({ result }: ToolRenderProps): ReactNode {
	const { t } = useTranslation();
	const d = detailsRecord(result);
	const waited = parseMsgs(d?.waited);
	const timedOut = d !== null && d.waited === null;
	return (
		<>
			{waited.length > 0 && (
				<div className="tv-irc-thread">
					{waited.map((msg, i) => (
						<Letter key={i} msg={msg} />
					))}
				</div>
			)}
			{timedOut && <Note tone="warn">{t("toolRender.irc.timedOutNote")}</Note>}
			{((waited.length === 0 && !timedOut) || result?.isError) && (
				<ResultText result={result} maxLines={8} />
			)}
		</>
	);
}

export const ircRenderer: ToolRenderer = { Summary, Result };
