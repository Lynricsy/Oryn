// 首帧前写入明暗、主题色与壁纸，避免闪色；规则与 src/state/theme.ts 一致。经典脚本，CSP script-src 'self' 放行。
(() => {
	const root = document.documentElement;
	let dark = false;
	try {
		dark = window.matchMedia("(prefers-color-scheme: dark)").matches;
	} catch {}
	root.dataset.wallpaper = "aurora";
	try {
		const raw = localStorage.getItem("oryn.theme");
		const v = (raw ? JSON.parse(raw) : {}) || {};
		const mode = v.mode === "light" || v.mode === "dark" ? v.mode : "system";
		const accents = ["cobalt", "violet", "berry", "fox", "graphite"];
		root.dataset.theme = mode === "system" ? (dark ? "dark" : "light") : mode;
		if (accents.includes(v.accent)) root.dataset.accent = v.accent;
		if (["waves", "grain"].includes(v.wallpaper))
			root.dataset.wallpaper = v.wallpaper;
		// 自定义壁纸：模糊后的 data URL 同步读出写成内联变量；图片不在时留在 aurora
		// 这里任何异常都会落到 catch 把明暗改回跟随系统，所以先判类型再调用字符串方法
		const image =
			v.wallpaper === "custom"
				? localStorage.getItem("oryn.wallpaper.image")
				: null;
		if (typeof image === "string" && image.startsWith("data:image/")) {
			const tone = Array.isArray(v.tone) ? v.tone : [0.5, 0.5];
			root.style.setProperty("--wallpaper-image", `url("${image}")`);
			root.style.setProperty("--wp-lo", String(tone[0]));
			root.style.setProperty("--wp-hi", String(tone[1]));
			root.dataset.wallpaper = "custom";
		}
	} catch {
		root.dataset.theme = dark ? "dark" : "light";
	}
})();
