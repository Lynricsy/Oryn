/**
 * 快捷键清单：设置 → 输入与快捷键 的只读一览从这里取，不在页面里另写一份。
 * 各组合键的实际处理在别处：应用级的在 AppShell 的 `useGlobalShortcuts`，角色循环在 RoleTrack 的 `useRoleShortcut`，
 * 输入框按键在 `composerKeyAction`（随发送键设置变化）。shortcuts.test 用真实的按键事件核对三处与清单一致。
 */
import type { ComposerAction, SendKey } from "./composer";

/** 一个组合键。`mod` = mac 上的 ⌘、其它平台的 Ctrl（处理方两者都认）。 */
export interface KeyCombo {
	/** `KeyboardEvent.key` */
	key: string;
	/** `KeyboardEvent.code`（物理键位；数字键按它判） */
	code: string;
	mod?: boolean;
	shift?: boolean;
	alt?: boolean;
}

export type AppShortcutId =
	| "palette"
	| "newThread"
	| "sidebar"
	| "assistant"
	| "sections"
	| "roleNext"
	| "rolePrev";

export interface AppShortcut {
	id: AppShortcutId;
	/** `app` = 任何页面（AppShell）；`thread` = 线程页（RoleTrack） */
	scope: "app" | "thread";
	/** 多个组合键时：`range` 显示成「首 – 尾」，否则逐个列出 */
	combos: readonly KeyCombo[];
	range?: boolean;
	/** 焦点在输入框里时不生效 */
	notInInput?: boolean;
}

const key = (k: string, extra: Partial<KeyCombo> = {}): KeyCombo => ({
	key: k,
	code: /^[a-z]$/.test(k) ? `Key${k.toUpperCase()}` : k,
	mod: true,
	...extra,
});

export const APP_SHORTCUTS: readonly AppShortcut[] = [
	{ id: "palette", scope: "app", combos: [key("k")] },
	{ id: "newThread", scope: "app", combos: [key("o", { shift: true })] },
	{ id: "sidebar", scope: "app", combos: [key("b")], notInInput: true },
	{ id: "assistant", scope: "app", combos: [key("/", { code: "Slash" })] },
	{
		id: "sections",
		scope: "app",
		range: true,
		combos: ["1", "2", "3", "4"].map((d) => key(d, { code: `Digit${d}` })),
	},
	{ id: "roleNext", scope: "thread", combos: [key("p")] },
	{ id: "rolePrev", scope: "thread", combos: [key("p", { shift: true })] },
];

export type ComposerShortcutId =
	| "send"
	| "newline"
	| "steer"
	| "followUp"
	| "abortAndSend"
	| "abort"
	| "rewind";

export interface ComposerShortcut {
	id: ComposerShortcutId;
	/** `composerKeyAction` 给出的动作 */
	action: ComposerAction;
	/** 回合进行中才有的动作 */
	busy: boolean;
	/** 输入框为空时才有的动作 */
	empty?: boolean;
	combos: readonly KeyCombo[];
	/** 连按两次 */
	twice?: boolean;
}

const ENTER = { key: "Enter", code: "Enter" } as const;
const ESC = { key: "Escape", code: "Escape" } as const;

/** 输入框按键（随发送键设置）；映射见 lib/composer.ts 文件头的表。 */
export function composerShortcuts(send: SendKey): readonly ComposerShortcut[] {
	const s: KeyCombo = send === "mod" ? { ...ENTER, mod: true } : ENTER;
	return [
		{ id: "send", action: "prompt", busy: false, combos: [s] },
		{
			id: "newline",
			action: "newline",
			busy: false,
			combos: send === "mod" ? [ENTER] : [{ ...ENTER, shift: true }],
		},
		{ id: "steer", action: "steer", busy: true, combos: [s] },
		{
			id: "followUp",
			action: "follow_up",
			busy: true,
			combos: [send === "mod" ? { ...s, shift: true } : { ...s, mod: true }],
		},
		{
			id: "abortAndSend",
			action: "abort_and_prompt",
			busy: true,
			combos: [{ ...s, alt: true }],
		},
		{ id: "abort", action: "abort", busy: true, combos: [ESC], twice: true },
		{
			id: "rewind",
			action: "rewind",
			busy: false,
			empty: true,
			combos: [ESC],
			twice: true,
		},
	];
}

/** mac / iOS 显示 ⌘ ⇧ ⌥，其它平台显示 Ctrl+Shift+Alt。 */
export function isApplePlatform(): boolean {
	return (
		typeof navigator !== "undefined" &&
		/mac|iphone|ipad/i.test(navigator.platform || navigator.userAgent)
	);
}

const KEY_LABEL: Record<string, string> = { Escape: "Esc", Enter: "Enter" };

/** 组合键的显示文字：mac 按系统惯例 ⌥⇧⌘ 连写（`⇧⌘P`），其它平台 `Ctrl+Shift+P`。 */
export function formatCombo(c: KeyCombo, mac = isApplePlatform()): string {
	const k = KEY_LABEL[c.key] ?? c.key.toUpperCase();
	if (mac)
		return `${c.alt ? "⌥" : ""}${c.shift ? "⇧" : ""}${c.mod ? "⌘" : ""}${k}`;
	return [c.mod && "Ctrl", c.shift && "Shift", c.alt && "Alt", k]
		.filter(Boolean)
		.join("+");
}
