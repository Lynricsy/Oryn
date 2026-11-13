/**
 * 全局提示：服务端 `error` 消息里没有对应在途命令的那些（订阅失败、侧通道应答被拒等）。
 * 带线程上下文时提供跳转与「重新同步」（重新订阅取回权威快照）。
 */
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { Link } from "react-router";
import { OmpCommandError, realtime } from "../realtime/socket";
import { useConnection, useIndex } from "../state/stores";
import { Icon } from "./ui/icons";
import { Button, describeError, IconButton } from "./ui/primitives";

export function ServerErrors(): ReactNode {
	const { t } = useTranslation();
	const errors = useConnection((s) => s.errors);
	const threads = useIndex((s) => s.threads);
	if (errors.length === 0) return null;
	return (
		<section
			aria-label={t("alerts.region")}
			className="pointer-events-none fixed right-3 bottom-3 z-[60] flex w-[min(24rem,calc(100vw-1.5rem))] flex-col gap-2 pb-safe"
		>
			{errors.map((e) => {
				const thread = e.threadId ? threads[e.threadId] : undefined;
				return (
					<div
						key={e.key}
						role="alert"
						className="glass pop-in pointer-events-auto rounded-lg px-3 py-2.5 text-sm"
					>
						<div className="flex items-start gap-2">
							<Icon
								name="alert"
								size={16}
								weight="fill"
								aria-hidden
								className="mt-0.5 shrink-0 text-err"
							/>
							<p className="min-w-0 flex-1 break-words pt-px">
								{describeError(new OmpCommandError("", e.code, e.message), t)}
							</p>
							<IconButton
								icon="close"
								label={t("common.dismiss")}
								size={14}
								className="-my-1.5 -mr-2"
								onClick={() => realtime.dismissError(e.key)}
							/>
						</div>
						{e.threadId && (
							<div className="mt-1.5 flex items-center gap-2 pl-6 text-xs">
								<Link
									to={`/t/${e.threadId}`}
									className="min-w-0 flex-1 truncate text-muted hover:text-fg"
								>
									{thread?.title || t("thread.untitled")}
								</Link>
								<Button
									size="sm"
									onClick={() => {
										if (e.threadId) realtime.resync(e.threadId);
										realtime.dismissError(e.key);
									}}
								>
									{t("alerts.resync")}
								</Button>
							</div>
						)}
					</div>
				);
			})}
		</section>
	);
}
