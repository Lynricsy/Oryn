/** 系统分享进来、尚未被新建线程框取走的内容（M7 计划 4.6）：AppShell 收到后存这里并跳到首页，HomePage 取走预填。 */
import { useStore } from "zustand";
import { createStore } from "zustand/vanilla";
import type { ShareIntake } from "../lib/host";

export interface ShareIntakeState {
	pending: ShareIntake | null;
	/** 只保留最新一次分享（与壳一致）。 */
	set(s: ShareIntake): void;
	/** 取走并清空。 */
	take(): ShareIntake | null;
}

export const shareIntakeStore = createStore<ShareIntakeState>((set, get) => ({
	pending: null,
	set: (pending) => set({ pending }),
	take: () => {
		const s = get().pending;
		if (s) set({ pending: null });
		return s;
	},
}));

export function useShareIntake<T>(selector: (s: ShareIntakeState) => T): T {
	return useStore(shareIntakeStore, selector);
}
