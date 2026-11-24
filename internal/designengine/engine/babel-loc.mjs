// babel 插件（M29）：给设计源码里的 JSX 起始标签加 data-oryn-loc="src/frames/x.tsx:行:列"。
// 路径相对工作副本根，行、列都从 1 起。dev 与构建都打（裁决 3）。
// 只处理 <工作副本>/src 下、不在 node_modules 里的文件；Fragment 不能带属性，跳过；已写了 data-oryn-loc 的不覆盖。
// 属性追加在最后：元素自己的标记总是盖过 {...props} 传进来的调用处标记。

import path from "node:path";

const ATTR = "data-oryn-loc";

function isFragment(t, name) {
	if (t.isJSXIdentifier(name)) return name.name === "Fragment";
	if (t.isJSXMemberExpression(name)) return name.property.name === "Fragment";
	return false;
}

/** @param {{ types: typeof import("@babel/types") }} api */
export default function orynLoc({ types: t }, options) {
	const root = path.resolve(options.root);
	const src = path.join(root, "src") + path.sep;
	return {
		name: "oryn-loc",
		visitor: {
			JSXOpeningElement(p, state) {
				const file = state.filename;
				if (
					!file ||
					!file.startsWith(src) ||
					file.includes(`${path.sep}node_modules${path.sep}`)
				)
					return;
				const { node } = p;
				if (!node.loc || isFragment(t, node.name)) return;
				if (
					node.attributes.some(
						(a) => t.isJSXAttribute(a) && a.name.name === ATTR,
					)
				)
					return;
				const rel = path.relative(root, file).split(path.sep).join("/");
				const loc = `${rel}:${node.loc.start.line}:${node.loc.start.column + 1}`;
				node.attributes.push(
					t.jsxAttribute(t.jsxIdentifier(ATTR), t.stringLiteral(loc)),
				);
			},
		},
	};
}
