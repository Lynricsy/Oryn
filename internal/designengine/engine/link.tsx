// 帧间跳转（M29）。设计源码这样引入：import { Link } from "oryn:design";
// dev 时链到 dev server 的 /frames/<id>；构建产物里是同源相对链接 ./<id>.html（静态帧运行时放行同源链接）。
// 构建时引擎静态扫描 <Link to="字面量"> 生成 dist/frames.json 的 links，所以 to 要写字符串字面量。

import type { AnchorHTMLAttributes } from "react";

export interface LinkProps
	extends Omit<AnchorHTMLAttributes<HTMLAnchorElement>, "href"> {
	/** 目标帧 id，即 src/frames/<id>.tsx 的文件名。 */
	to: string;
}

export function frameHref(to: string): string {
	return import.meta.env.DEV ? `/frames/${to}` : `./${to}.html`;
}

export function Link({ to, ...rest }: LinkProps) {
	return <a {...rest} href={frameHref(to)} />;
}
