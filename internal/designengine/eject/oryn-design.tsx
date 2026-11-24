// 帧间跳转（Oryn 设计画布导出）：与画布引擎的 oryn:design 同名同参，导出工程里改成 hash 路由跳转。
// 设计源码照旧写 import { Link } from "oryn:design"，vite.config.ts 把这个模块名指到本文件。

import type { AnchorHTMLAttributes } from "react";

export interface LinkProps
	extends Omit<AnchorHTMLAttributes<HTMLAnchorElement>, "href"> {
	/** 目标帧 id，即 src/frames/<id>.tsx 的文件名。 */
	to: string;
}

export function frameHref(to: string): string {
	return `#/${to}`;
}

export function Link({ to, ...rest }: LinkProps) {
	return <a {...rest} href={frameHref(to)} />;
}
