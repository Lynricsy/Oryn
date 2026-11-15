/**
 * `/` 命令菜单：合并 Oryn 客户端命令与 omp 公布的命令（`get_available_commands` / `available_commands_update`），
 * 命令名后输入空格进入参数补全：静态子命令 + 动态候选（`get_command_arg_completions`），并在底部给出该命令的用法。
 * 只能在 TUI 里执行、客户端又没有接管的命令不列（`hiddenFromMenu`）。
 */
import { useQuery } from "@tanstack/react-query";
import { type ReactNode, useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import {
	CLIENT_COMMANDS,
	type ClientCommand,
	COMMAND_HINTS,
	COORDINATOR_CLIENT_COMMANDS,
	findCommand,
	hiddenFromMenu,
} from "../../lib/slash";
import type { SlashCommandInfo } from "../../omp/types";
import { realtime } from "../../realtime/socket";
import { Badge } from "../ui/data";

export interface MenuEntry {
	/** 选中后输入框的新内容 */
	insert: string;
	label: string;
	/** 参数提示（`<work>`、`[on|off]`），命令名后的灰色等宽字 */
	hint?: string;
	description?: string;
	badge?: string;
	/** 选中即收起菜单（无参数命令，再按 Enter 执行） */
	submit: boolean;
}

/** 正在输入参数的那条命令的用法（菜单底部）。 */
export interface CommandUsage {
	label: string;
	hint?: string;
	description?: string;
}

export interface CommandMenuModel {
	entries: MenuEntry[];
	usage?: CommandUsage;
}

/** Oryn 客户端命令的来源名。 */
const ORYN_SOURCE = "Oryn";

function useDebounced<T>(value: T, ms: number): T {
	const [v, setV] = useState(value);
	useEffect(() => {
		const timer = setTimeout(() => setV(value), ms);
		return () => clearTimeout(timer);
	}, [value, ms]);
	return v;
}

/** 计算菜单内容；输入不是斜杠命令（或参数阶段的命令不认识）时返回 null。`coordinator`：协调对话只列它能执行的客户端命令。 */
export function useCommandMenu(
	threadId: string,
	text: string,
	commands: readonly SlashCommandInfo[] | null,
	coordinator = false,
): CommandMenuModel | null {
	const { t } = useTranslation();
	const m = /^\/(\S*)(\s+([\s\S]*))?$/.exec(text);
	const name = m?.[1]?.toLowerCase() ?? "";
	const inArgs = m?.[2] !== undefined;
	const argPrefix = m?.[3] ?? "";
	const server = commands ?? [];
	const serverCmd = findCommand(server, name);
	const dynamic = Boolean(m && inArgs && serverCmd?.hasDynamicArgCompletion);
	const debouncedPrefix = useDebounced(argPrefix, 150);
	const completions = useQuery({
		queryKey: [
			"rpc",
			threadId,
			"arg-completions",
			serverCmd?.name,
			debouncedPrefix,
		],
		enabled: dynamic && Boolean(serverCmd),
		retry: false,
		staleTime: 10_000,
		queryFn: () =>
			realtime.cmd(threadId, {
				type: "get_command_arg_completions",
				command: serverCmd?.name ?? name,
				prefix: debouncedPrefix,
			}),
	});
	if (!m) return null;
	const clientShown = (c: string) =>
		(CLIENT_COMMANDS as readonly string[]).includes(c) &&
		(!coordinator ||
			COORDINATOR_CLIENT_COMMANDS[
				c as keyof typeof COORDINATOR_CLIENT_COMMANDS
			]);

	if (!inArgs) {
		const seen = new Set<string>();
		const entries: MenuEntry[] = [];
		for (const c of CLIENT_COMMANDS) {
			seen.add(c);
			if (!clientShown(c)) continue;
			const hint = COMMAND_HINTS[c] ?? findCommand(server, c)?.input?.hint;
			entries.push({
				insert: `/${c}${hint ? " " : ""}`,
				label: `/${c}`,
				hint,
				description: t(`slash.desc.${c}`),
				badge: ORYN_SOURCE,
				submit: !hint,
			});
		}
		for (const c of server) {
			if (seen.has(c.name) || hiddenFromMenu(c)) continue;
			seen.add(c.name);
			const takesArgs = Boolean(c.allowArgs || c.subcommands?.length);
			entries.push({
				insert: `/${c.name}${takesArgs ? " " : ""}`,
				label: `/${c.name}`,
				hint: c.input?.hint,
				description: c.description,
				badge: c.source,
				submit: !takesArgs,
			});
		}
		const filtered = entries.filter(
			(e) =>
				e.label.slice(1).startsWith(name) ||
				(name.length > 1 && e.label.includes(name)),
		);
		return {
			entries: filtered.sort(
				(a, b) =>
					Number(!a.label.slice(1).startsWith(name)) -
					Number(!b.label.slice(1).startsWith(name)),
			),
		};
	}

	const client = clientShown(name);
	const known =
		client || (serverCmd !== undefined && !hiddenFromMenu(serverCmd));
	const entries: MenuEntry[] = [];
	if (known) {
		for (const sub of serverCmd?.subcommands ?? []) {
			if (!sub.name.startsWith(argPrefix.trim())) continue;
			entries.push({
				insert: `/${name} ${sub.name}${sub.usage ? " " : ""}`,
				label: sub.name,
				hint: sub.usage,
				description: sub.description,
				submit: !sub.usage,
			});
		}
	}
	for (const item of completions.data?.items ?? []) {
		entries.push({
			insert: `/${name} ${item.value}`,
			label: item.label ?? item.value,
			description: item.description ?? item.hint,
			submit: false,
		});
	}
	if (!known) return entries.length > 0 ? { entries } : null;
	return {
		entries,
		usage: {
			label: `/${name}`,
			hint:
				(client ? COMMAND_HINTS[name] : undefined) ?? serverCmd?.input?.hint,
			description: client
				? t(`slash.desc.${name as ClientCommand}`)
				: serverCmd?.description,
		},
	};
}

export function CommandMenu({
	entries,
	usage,
	active,
	onPick,
	hint,
}: {
	entries: MenuEntry[];
	usage?: CommandUsage;
	active: number;
	onPick: (e: MenuEntry) => void;
	hint?: string;
}): ReactNode {
	const { t } = useTranslation();
	// 来源徽标只用来区分来源：所有项来源相同时不显示；混排时 Oryn 自己的命令（排在最前）不挂徽标，
	// 只给 omp 公布的命令标出来源（builtin、skill…），避免每行都挂同一个「Oryn」
	const showBadges =
		new Set(entries.map((e) => e.badge).filter(Boolean)).size > 1;
	const listRef = useRef<HTMLDivElement>(null);
	// 键盘移到可见区之外的项时把它滚进来（滚动区带 scroll-padding，不会贴边）
	// biome-ignore lint/correctness/useExhaustiveDependencies: active 变化即需要滚动
	useEffect(() => {
		listRef.current
			?.querySelector<HTMLElement>("[aria-selected=true]")
			?.scrollIntoView?.({ block: "nearest" });
	}, [active]);
	const showList = entries.length > 0 || !usage;
	return (
		// 玻璃放在外层、滚动放在内层：玻璃的模糊底是伪元素，放进滚动容器会随内容滚走。
		// 提示行与用法放在滚动区外；滚动区高度是 9 个单行项（h-7）加上下内边距，高亮项展开后底部露出的半行正好提示还能往下滚
		<div className="glass pop-in absolute right-0 bottom-full left-0 z-30 mb-2 overflow-hidden rounded-xl">
			{hint && (
				<div className="border-[var(--glass-line)] border-b px-3 py-1 text-2xs text-faint">
					{hint}
				</div>
			)}
			{showList && (
				<div
					ref={listRef}
					role="listbox"
					aria-label={t("composer.commands")}
					className="max-h-[calc(9*1.75rem+0.5rem)] overflow-y-auto scroll-py-1 p-1"
				>
					{entries.length === 0 && (
						<div className="flex h-7 items-center px-2 text-faint text-sm">
							{t("composer.noCommands")}
						</div>
					)}
					{entries.map((e, i) => {
						const selected = i === active;
						const badge = showBadges && e.badge && e.badge !== ORYN_SOURCE && (
							<Badge>{e.badge}</Badge>
						);
						return (
							<button
								key={`${e.label}-${e.insert}`}
								type="button"
								role="option"
								aria-selected={selected}
								title={selected ? undefined : e.description}
								className={`w-full rounded-sm px-2 text-left text-xs transition-colors duration-150 hover:bg-hover ${selected ? "bg-hover pt-1.5 pb-2" : ""}`}
								onMouseDown={(ev) => {
									ev.preventDefault();
									onPick(e);
								}}
							>
								{/* 高亮项展开：参数提示折行写全，描述另起一行写全；其余项单行，窄屏省掉参数提示 */}
								<span
									className={`flex gap-2 ${selected ? "items-baseline" : "h-7 items-center"}`}
								>
									<span className="shrink-0 font-medium font-mono text-fg">
										{e.label}
									</span>
									{e.hint && (
										<span
											data-testid="menu-hint"
											className={`font-mono text-2xs text-faint ${selected ? "min-w-0 break-words" : "max-w-[40%] shrink-0 truncate max-sm:hidden"}`}
										>
											{e.hint}
										</span>
									)}
									<span
										className={`min-w-0 flex-1 text-muted ${selected ? "" : "truncate"}`}
									>
										{selected ? null : e.description}
									</span>
									{badge}
								</span>
								{selected && e.description && (
									<span className="mt-1 block text-muted leading-relaxed">
										{e.description}
									</span>
								)}
							</button>
						);
					})}
				</div>
			)}
			{usage && (
				<div
					data-testid="menu-usage"
					className={`px-3 py-2 text-xs ${showList ? "border-[var(--glass-line)] border-t" : ""}`}
				>
					<span className="font-medium font-mono text-fg">{usage.label}</span>
					{usage.hint && (
						<span className="ml-2 break-words font-mono text-2xs text-faint">
							{usage.hint}
						</span>
					)}
					{usage.description && (
						<span className="mt-0.5 block text-muted leading-relaxed">
							{usage.description}
						</span>
					)}
				</div>
			)}
		</div>
	);
}
