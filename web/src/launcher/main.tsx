// 连接页入口（桌面壳内置的唯一页面，M6 计划 4）。中文不打包思源黑体分片（用系统字体），避免把十几 MB 字体打进安装包
import "@fontsource-variable/fraunces/opsz.css";
import "@fontsource-variable/jetbrains-mono/index.css";
import "@fontsource-variable/mona-sans/index.css";
import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import "../i18n";
import "../index.css";
import { installTheme } from "../state/theme";
import { Launcher } from "./Launcher";

const root = document.getElementById("root");
if (!root) throw new Error("缺少 #root 挂载点");
installTheme();
createRoot(root).render(
	<StrictMode>
		<Launcher />
	</StrictMode>,
);
