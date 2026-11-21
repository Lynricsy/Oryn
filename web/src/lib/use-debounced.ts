/** 防抖：值停止变化 `ms` 毫秒后才更新（命令菜单、命令面板、项目线程搜索共用）。 */
import { useEffect, useState } from "react";

export function useDebounced<T>(value: T, ms: number): T {
	const [v, setV] = useState(value);
	useEffect(() => {
		const timer = setTimeout(() => setV(value), ms);
		return () => clearTimeout(timer);
	}, [value, ms]);
	return v;
}
