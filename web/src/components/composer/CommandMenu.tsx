/**
 * `/` 命令菜单：合并 Oryn 客户端命令与 omp 公布的命令（`get_available_commands` / `available_commands_update`），
 * 命令名后输入空格进入参数补全：静态子命令 + 动态候选（`get_command_arg_completions`）。
 */
import { useQuery } from "@tanstack/react-query";
import { type ReactNode, useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { CLIENT_COMMANDS, UNAVAILABLE_COMMANDS } from "../../lib/slash";
import type { RpcAvailableSlashCommand } from "../../omp/types";
import { realtime } from "../../realtime/socket";
import { Badge } from "../ui/data";

export interface MenuEntry {
	/** 选中后输入框的新内容 */
	insert: string;
	label: string;
	description?: string;
	badge?: string;
	/** 选中即执行（无参数命令） */
	submit: boolean;
}

function useDebounced<T>(value: T, ms: number): T {
	const [v, setV] = useState(value);
	useEffect(() => {
		const timer = setTimeout(() => setV(value), ms);
		return () => clearTimeout(timer);
	}, [value, ms]);
	return v;
}

/** 计算菜单项；输入不是斜杠命令时返回 null。 */
export function useCommandMenu(
	threadId: string,
	text: string,
	commands: RpcAvailableSlashCommand[] | null,
) {
	const { t } = useTranslation();
	const m = /^\/(\S*)(\s+([\s\S]*))?$/.exec(text);
	const name = m?.[1]?.toLowerCase() ?? "";
	const inArgs = m?.[2] !== undefined;
	const argPrefix = m?.[3] ?? "";
	const server = commands ?? [];
	const serverCmd = server.find(
		(c) => c.name === name || c.aliases?.includes(name),
	);
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

	if (!inArgs) {
		const seen = new Set<string>();
		const entries: MenuEntry[] = [];
		for (const c of CLIENT_COMMANDS) {
			seen.add(c);
			entries.push({
				insert: `/${c} `,
				label: `/${c}`,
				description: t(`slash.desc.${c}`),
				badge: "Oryn",
				submit: false,
			});
		}
		for (const c of server) {
			if (
				seen.has(c.name) ||
				(UNAVAILABLE_COMMANDS as readonly string[]).includes(c.name)
			)
				continue;
			seen.add(c.name);
			entries.push({
				insert: `/${c.name}${c.allowArgs || c.subcommands?.length ? " " : ""}`,
				label: `/${c.name}`,
				description: c.description,
				badge: c.source,
				submit: !c.allowArgs && !c.subcommands?.length,
			});
		}
		const filtered = entries.filter(
			(e) =>
				e.label.slice(1).startsWith(name) ||
				(name.length > 1 && e.label.includes(name)),
		);
		return filtered.sort(
			(a, b) =>
				Number(!a.label.slice(1).startsWith(name)) -
				Number(!b.label.slice(1).startsWith(name)),
		);
	}

	const out: MenuEntry[] = [];
	for (const sub of serverCmd?.subcommands ?? []) {
		if (!sub.name.startsWith(argPrefix.trim())) continue;
		out.push({
			insert: `/${name} ${sub.name}${sub.usage ? " " : ""}`,
			label: sub.name,
			description: sub.description ?? sub.usage,
			submit: !sub.usage,
		});
	}
	for (const item of completions.data?.items ?? []) {
		out.push({
			insert: `/${name} ${item.value}`,
			label: item.label ?? item.value,
			description: item.description ?? item.hint,
			submit: false,
		});
	}
	return out;
}

export function CommandMenu({
	entries,
	active,
	onPick,
	hint,
}: {
	entries: MenuEntry[];
	active: number;
	onPick: (e: MenuEntry) => void;
	hint?: string;
}): ReactNode {
	const { t } = useTranslation();
	return (
		// 玻璃放在外层、滚动放在内层：玻璃的模糊底是伪元素，放进滚动容器会随内容滚走
		<div className="glass pop-in absolute right-0 bottom-full left-0 z-30 mb-2 overflow-hidden rounded-xl">
			<div
				role="listbox"
				aria-label={t("composer.commands")}
				className="max-h-72 overflow-y-auto p-1"
			>
				{hint && <div className="px-2 py-1 text-2xs text-faint">{hint}</div>}
				{entries.length === 0 && (
					<div className="px-2 py-1.5 text-faint text-sm">
						{t("composer.noCommands")}
					</div>
				)}
				{entries.map((e, i) => (
					<button
						key={`${e.label}-${e.insert}`}
						type="button"
						role="option"
						aria-selected={i === active}
						className={`flex w-full items-center gap-2 rounded-sm px-2 py-1.5 text-left text-sm transition-colors duration-150 hover:bg-hover ${i === active ? "bg-hover" : ""}`}
						onMouseDown={(ev) => {
							ev.preventDefault();
							onPick(e);
						}}
					>
						<span className="shrink-0 font-medium font-mono text-fg text-xs">
							{e.label}
						</span>
						<span className="min-w-0 flex-1 truncate text-muted text-xs">
							{e.description}
						</span>
						{e.badge && <Badge>{e.badge}</Badge>}
					</button>
				))}
			</div>
		</div>
	);
}
