# <!-- oryn:title -->

由 Oryn 设计画布导出的前端工程：React 19 + Vite 7 + Tailwind CSS v4。每个画框是 `src/frames/<id>.tsx`，页面用 hash 路由切换：

- `#/<id>` 打开某一帧，`#/` 打开起始帧，`#/_frames` 列出全部帧；
- 帧间跳转仍写 `import { Link } from "oryn:design"`，`vite.config.ts` 把它指到 `oryn-design.tsx`（改成 hash 跳转）；
- `src/frames/_layout.tsx`（如有）包住每一帧；`src/theme.css` 是设计 token（`@theme {…}`），由 `styles.css` 引入。
- `src/themes/<名>.css`（如有）是方案主题（`:root[data-oryn-theme="<名>"] {…}`），由 `styles.css` 在 `theme.css` 之后引入；切到某帧时按 `oryn-frames.json` 里该帧的 `theme` 设置 `<html data-oryn-theme>`。

## 运行

```sh
npm ci            # 按 package-lock.json 安装，版本与画布引擎一致
npm run dev       # 开发服务器
npm run build     # 产物在 dist/
npm start         # node server.mjs：零依赖静态服务 dist/，端口取 PORT（缺省 3000）
```

`package.json` 是画布引擎的依赖与设计级依赖（`src/package.json`）合并的结果，`package-lock.json` 与它严格对应，所以用 `npm ci`。加减依赖请在根目录 `npm install <包>`；`src/package.json` 只是画布里的原始记录，导出工程不再使用它。

## 部署到 box

`.oryn/box.yaml` 已写好：在 orb 里 `npm ci && npm run build`，release 只带 `dist` 与 `server.mjs`，服务命令 `node server.mjs`（监听 `$PORT`）。把工程放进一个 git 仓库并提交后：

```sh
oryn boxes deploy --on <box> --name <app>
```

## 与画布的差异

- 不带画布的元素定位标记（`data-oryn-loc`）、页面运行时与设计检查探针；
- `oryn-frames.json` 记录导出时的帧顺序、名字与起始帧，之后新增的帧排在后面，名字取帧文件里 `export const frame` 的 `name`。
- 方案组里落选方案的帧与只被它们用到的主题不导出；已选定的组从 `src/variants.json` 里去掉，选中方案的帧成了普通帧，主题记在 `oryn-frames.json` 的 `theme`。想交付落选方案，先在画布上改选它。
<!-- oryn:conflicts -->
