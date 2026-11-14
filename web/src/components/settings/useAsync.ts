/** 设置类表单的异步动作：忙碌态与最近一次错误；`run` 返回是否成功。 */
import { useState } from "react";

export function useAsync() {
	const [busy, setBusy] = useState(false);
	const [error, setError] = useState<unknown>(null);
	const run = async (fn: () => Promise<unknown>) => {
		setBusy(true);
		setError(null);
		try {
			await fn();
			return true;
		} catch (e) {
			setError(e);
			return false;
		} finally {
			setBusy(false);
		}
	};
	return { busy, error, run };
}
