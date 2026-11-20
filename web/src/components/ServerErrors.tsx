/**
 * 全局提示：服务端 `error` 消息里没有对应在途命令的那些（订阅失败、侧通道应答被拒等）。
 * 带线程上下文时提供跳转与「重新同步」（重新订阅取回权威快照）。
 * 与操作提示（`ui/Toasts`）同在 AppShell 的底部提示栏（`BottomDock`）里上下排列：本组靠右、在上，操作提示居中、在下，
 * 窄屏上两组不再各自 fixed 在同一块底边上互相压住。
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
			className="pointer-events-none flex w-[min(24rem,100%)] flex-col gap-2 self-end"
		>
			{errors.map((e) => {
				const thread = e.threadId ? threads[e.threadId] : undefined;
				return (
					<div
						key={e.key}
						role="alert"
						className="glass pop-in dc-br pointer-events-auto rounded-lg px-3 py-2.5 text-sm [--dc-gap:100vmax] [--dc-r:var(--r-lg)] last:[--dc-gap:0.75rem]"
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
