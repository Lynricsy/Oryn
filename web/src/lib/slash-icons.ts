/**
 * `/` 命令菜单里每条命令的图标。RPC 公布的命令不带图标，按命令名取，语义对齐 omp TUI 给各 builtin 配的 `icon`；
 * 表里没有的命令（omp 新增的 builtin、skill、MCP prompt、扩展…）按来源兜底。
 * 只列菜单里会出现的命令：`UNAVAILABLE_COMMANDS` 与被客户端拦下的命令不配图标。
 */
import type { IconName } from "../components/ui/icons";
import type { RpcAvailableSlashCommand } from "../omp/types";

type CommandSource = RpcAvailableSlashCommand["source"];

const COMMAND_ICONS: Record<string, IconName> = {
	// Oryn 客户端命令
	plan: "map",
	"plan-review": "map",
	tree: "tree",
	fork: "fork",
	new: "plus",
	resume: "history",
	clear: "eraser",
	queue: "tray",
	btw: "question",
	tan: "rocket",
	omfg: "gavel",
	vibe: "waveform",
	loop: "loop",
	goal: "target",
	"guided-goal": "compass",
	pause: "pause",
	handoff: "handoff",
	compact: "compress",
	shake: "vibrate",
	fresh: "restart",
	retry: "refresh",
	model: "cpu",
	fast: "bolt",
	slow: "hourglass",
	context: "stack",
	usage: "gauge",
	jobs: "terminal",
	tools: "wrench",
	session: "info",
	agents: "robot",
	git: "branch",
	// omp 公布的 builtin
	advisor: "lightbulb",
	trace: "chart",
	dump: "clipboard",
	share: "share",
	browser: "globe",
	commit: "commit",
	force: "hammer",
	"forget-approvals": "trash",
	ssh: "hardDrives",
	delete: "trash",
	pin: "pin",
	unpin: "pin",
	memory: "memory",
	rename: "edit",
	"add-dir": "folderPlus",
	"remove-dir": "folderMinus",
	dirs: "folder",
	marketplace: "cart",
	plugins: "package",
	"reload-plugins": "refresh",
	security: "shield",
	switch: "swap",
	skillful: "sparkle",
	"extended-context": "maximize",
	computer: "monitor",
	ratchet: "loop",
	prewalk: "footprints",
	modelpreset: "sliders",
	effort: "brain",
	todo: "list",
	changelog: "newspaper",
	hotkeys: "keyboard",
	extensions: "puzzle",
	local: "edit",
	hub: "robot",
	branch: "branch",
	rewind: "undo",
	login: "signIn",
	logout: "signOut",
	mcp: "plugs",
};

const SOURCE_ICONS: Record<CommandSource, IconName> = {
	builtin: "code",
	skill: "sparkle",
	extension: "puzzle",
	custom: "fileText",
	file: "fileText",
	mcp_prompt: "plugs",
};

/** 命令的菜单图标；Oryn 客户端命令按 builtin 处理。 */
export function commandIcon(
	name: string,
	source: CommandSource = "builtin",
): IconName {
	return (
		(source === "builtin" ? COMMAND_ICONS[name] : undefined) ??
		SOURCE_ICONS[source]
	);
}
