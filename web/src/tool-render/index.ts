/**
 * 工具卡片渲染器，移植自 oryn-agent `packages/collab-web/src/tool-render/`（MIT，许可见同目录 LICENSE）。
 * 改动：去掉 web component 包装（element / standalone）；INTENT_FIELD 内联；
 * highlight.js 改为按需懒加载（`../lib/highlight`）；Markdown 改用 Oryn 的渲染器。
 */
export * from "./parts";
export * from "./registry";
export * from "./ToolView";
export * from "./types";
export * from "./util";
