// biome-ignore-all lint: ES5 经典脚本（旧 WebView 也要能解析），不能用箭头函数、let/const 与 Number.parseInt
/*
 * 旧 WebView 提示（M7 计划 4.8）：连接页在模块脚本之前以经典脚本加载，不依赖构建产物，必须保持 ES5。
 * UA 里 `Chrome/<主版本>` 小于 111（Android System WebView 过旧，跑不动现代界面）时，在 body 末尾插一条中英提示。
 * 非 Chromium 内核（无 Chrome/ 字样）不提示。
 */
(function () {
	var MIN = 111;
	// 文案放变量里：调用参数不折行，格式化器就不会补上 ES2017 的尾逗号
	var ZH = "系统 WebView 版本过旧，请在应用商店更新 Android System WebView";
	var EN =
		"Your system WebView is outdated. Update Android System WebView from the app store.";
	var m = /Chrome\/(\d+)/.exec(navigator.userAgent || "");
	if (!m || parseInt(m[1], 10) >= MIN) return;

	function show() {
		if (document.getElementById("webview-outdated")) return;
		var box = document.createElement("div");
		box.id = "webview-outdated";
		box.setAttribute("role", "alert");
		// 系统色：随系统明暗给出可读的中性底与正文色
		box.style.cssText =
			"position:fixed;left:0;right:0;bottom:0;z-index:2147483647;" +
			"padding:12px 16px;font:14px/1.5 sans-serif;" +
			"background:Canvas;color:CanvasText;border-top:1px solid GrayText;";
		var zh = document.createElement("p");
		zh.style.margin = "0";
		zh.appendChild(document.createTextNode(ZH));
		var en = document.createElement("p");
		en.style.margin = "0";
		en.appendChild(document.createTextNode(EN));
		box.appendChild(zh);
		box.appendChild(en);
		document.body.appendChild(box);
	}

	if (document.body) show();
	else document.addEventListener("DOMContentLoaded", show);
})();
