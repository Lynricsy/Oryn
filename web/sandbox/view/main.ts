// HTML 视图运行时的入口（`vite build -c vite.sandbox.config.ts --mode view` → sandbox/view.js），由 orynd 内联进视图文档。
import css from "./base.css?inline";
import { install } from "./runtime";

// 先缓存 window.parent：之后执行的页面脚本改写全局也不影响通道
install(window, window.parent, css);
