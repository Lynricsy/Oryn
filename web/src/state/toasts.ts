/**
 * 操作结果的轻提示（toast）：「已移到最近删除」带撤销、「正在导出…」→「已导出」、恢复版本冲突等。
 * 与通知 toast（`state/notifications.ts`，服务端推来的通知）分开：这里只是本机操作的反馈，不进通知列表。
 * - 同一个 `id` 再次 `showToast` 就原地替换（进度 → 结果），计时重新开始。
 * - `busy` 的提示（进行中）不自动消失，等结果把它替换掉；其余 6 秒后消失。
 * 由 `components/ui/Toasts.tsx` 渲染，挂在 AppShell。
 */
import { useStore } from "zustand";
import { createStore } from "zustand/vanilla";

export const ACTION_TOAST_MS = 6_000;
const MAX_TOASTS = 4;

export interface ToastAction {
	label: string;
	run(): void;
}

export interface Toast {
	id: string;
	text: string;
	tone: "neutral" | "err";
	/** 进行中：显示转圈，不自动消失。 */
	busy: boolean;
	action?: ToastAction;
}

export interface ToastInput {
	/** 缺省自动生成；给出时同 id 的提示被原地替换。 */
	id?: string;
	text: string;
	tone?: Toast["tone"];
	busy?: boolean;
	action?: ToastAction;
}

interface ToastsState {
	toasts: Toast[];
}

export const toastsStore = createStore<ToastsState>(() => ({ toasts: [] }));

const timers = new Map<string, ReturnType<typeof setTimeout>>();
let seq = 0;

export function dismissToast(id: string): void {
	clearTimeout(timers.get(id));
	timers.delete(id);
	toastsStore.setState((s) => ({
		toasts: s.toasts.filter((x) => x.id !== id),
	}));
}

/** 显示（或替换）一条提示，返回它的 id。 */
export function showToast(input: ToastInput): string {
	seq += 1;
	const id = input.id ?? `toast-${seq}`;
	const toast: Toast = {
		id,
		text: input.text,
		tone: input.tone ?? "neutral",
		busy: input.busy ?? false,
		...(input.action ? { action: input.action } : {}),
	};
	toastsStore.setState((s) => {
		const at = s.toasts.findIndex((x) => x.id === id);
		if (at >= 0) return { toasts: s.toasts.with(at, toast) };
		return { toasts: [...s.toasts, toast].slice(-MAX_TOASTS) };
	});
	clearTimeout(timers.get(id));
	timers.delete(id);
	if (!toast.busy)
		timers.set(
			id,
			setTimeout(() => dismissToast(id), ACTION_TOAST_MS),
		);
	return id;
}

export function useToasts(): Toast[] {
	return useStore(toastsStore, (s) => s.toasts);
}
