// 首帧前写入明暗与主题色，避免闪色；规则与 src/state/theme.ts 一致。经典脚本，CSP script-src 'self' 放行。
(() => {
	const root = document.documentElement;
	let dark = false;
	try {
		dark = window.matchMedia("(prefers-color-scheme: dark)").matches;
	} catch {}
	try {
		const raw = localStorage.getItem("oryn.theme");
		const v = raw ? JSON.parse(raw) : {};
		const mode =
			v && (v.mode === "light" || v.mode === "dark") ? v.mode : "system";
		const accents = ["cobalt", "violet", "berry", "fox", "graphite"];
		root.dataset.theme = mode === "system" ? (dark ? "dark" : "light") : mode;
		if (v && accents.includes(v.accent)) root.dataset.accent = v.accent;
	} catch {
		root.dataset.theme = dark ? "dark" : "light";
	}
})();
