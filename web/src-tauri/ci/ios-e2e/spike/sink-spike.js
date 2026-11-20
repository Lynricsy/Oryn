// M20 阶段 1 探针页的脚本（ci/ios-sink-spike 分支专用，不合入）。
// 读数：状态按钮文字 sx / sy / iw / max / vs / vy / kb / vst / pre / n / g（n = 根 scroll 事件计数，g = 守卫是否开）。
// 壳里时，每帧最多一次把卡片左缘（max − scrollX）经 host_tab_bar_reveal 发给壳：debug 包把它写进事件日志
// tabbar.reveal，脚本在按住拖动期间（测试进程被手势阻塞、读不到状态按钮）据此读 sx。
(() => {
	const $ = (id) => document.getElementById(id);
	const root = document.documentElement;
	const status = $("status");
	const vs = $("vs");
	const pre = $("pre");
	const invoke =
		window.__TAURI__?.core?.invoke ?? window.__TAURI_INTERNALS__?.invoke ?? null;

	for (let i = 0; i < 60; i++) {
		const p = document.createElement("p");
		p.textContent = `row ${i}`;
		vs.append(p);
	}
	pre.textContent = `${"const sinkSpike = ".repeat(2)}${Array.from({ length: 40 }, (_, i) => `item${i}`).join(", ")};`;

	const max = () => root.scrollWidth - innerWidth;
	const r1 = (v) => Math.round(v * 10) / 10;
	let n = 0;
	let guard = false;
	let error = "";
	// 程序化开合的方式（第 2 轮探针：第 1 轮里 smooth 到位后约 140ms 被拉回关闭位）
	// 0 smooth；1 instant；2 nosnap = 滚动期间去掉根的 scroll-snap-type、到位后恢复；3 snapoff = 根上始终不吸附
	const MODES = ["smooth", "instant", "nosnap", "snapoff"];
	let mode = 0;
	let restore = null;
	const go = (x) => {
		const m = MODES[mode];
		if (m === "nosnap") {
			root.style.scrollSnapType = "none";
			restore = x;
		}
		scrollTo({ left: x, behavior: m === "instant" ? "instant" : "smooth" });
	};
	// nosnap：到位（或 scrollend）后下一帧恢复吸附
	const maybeRestore = (end) => {
		if (restore === null) return;
		if (!end && Math.abs(scrollX - restore) > 0.5) return;
		restore = null;
		requestAnimationFrame(() => {
			root.style.scrollSnapType = MODES[mode] === "snapoff" ? "none" : "";
		});
	};

	const update = () => {
		const v = window.visualViewport;
		const kb = v ? Math.round(innerHeight - v.height - v.offsetTop) : -1;
		status.textContent =
			`sx=${r1(scrollX)};sy=${r1(scrollY)};iw=${innerWidth};max=${r1(max())};` +
			`vs=${v ? r1(v.scale) : -1};vy=${v ? r1(v.offsetTop) : -1};kb=${kb};` +
			`vst=${Math.round(vs.scrollTop)};pre=${Math.round(pre.scrollLeft)};n=${n};g=${guard ? 1 : 0};m=${mode}` +
			(error ? `;err ${error}` : "");
	};

	let raf = 0;
	const report = () => {
		if (!invoke || raf) return;
		raf = requestAnimationFrame(() => {
			raf = 0;
			invoke("host_tab_bar_reveal", {
				reveal: { x: max() - scrollX, covered: false },
			}).catch((e) => {
				error = `reveal ${e}`;
			});
		});
	};

	// ---------------------------------------------------------------- 守卫（plan-m20 步骤 3.2 的 createSinkScroll，精简版）
	// 非触摸、非本页请求引起的根位移立即拉回到已提交的静止位；静止 100ms 后提交，停在中间则就近平滑吸附。
	const REST_MS = 100;
	let committed = "closed";
	let free = false;
	let touching = 0;
	let timer = 0;
	const rest = () => {
		const m = max();
		if (m < 1) return null;
		if (scrollX <= 0.5) return "open";
		if (scrollX >= m - 0.5) return "closed";
		return null;
	};
	const left = (to) => (to === "open" ? 0 : max());
	const nearer = () => (scrollX < max() / 2 ? "open" : "closed");
	const settle = () => {
		timer = 0;
		if (touching) return;
		const r = rest();
		if (r === null) {
			go(left(nearer()));
			return;
		}
		committed = r;
		free = false;
	};
	const arm = () => {
		clearTimeout(timer);
		timer = setTimeout(settle, REST_MS);
	};
	const request = (to) => {
		free = true;
		go(left(to));
		arm();
	};

	addEventListener(
		"scroll",
		() => {
			n++;
			update();
			report();
			maybeRestore(false);
			if (!guard) return;
			if (!free && rest() !== committed) {
				scrollTo({ left: left(committed), behavior: "instant" });
				return;
			}
			arm();
		},
		{ passive: true },
	);
	addEventListener(
		"scrollend",
		() => {
			report();
			maybeRestore(true);
		},
		{ passive: true },
	);
	const touch = (e) => {
		touching = e.touches.length;
		if (e.type === "touchstart") free = true;
		else arm();
	};
	for (const t of ["touchstart", "touchend", "touchcancel"]) {
		addEventListener(t, touch, { passive: true });
	}
	window.visualViewport?.addEventListener("resize", update);
	window.visualViewport?.addEventListener("scroll", update);
	vs.addEventListener("scroll", update, { passive: true });
	pre.addEventListener("scroll", update, { passive: true });
	setInterval(update, 250);

	// ---------------------------------------------------------------- 按钮
	$("open").addEventListener("click", () => {
		if (guard) request("open");
		else go(0);
	});
	$("close").addEventListener("click", () => {
		if (guard) request("closed");
		else go(max());
	});
	$("mode").addEventListener("click", () => {
		mode = (mode + 1) % MODES.length;
		restore = null;
		root.style.scrollSnapType = MODES[mode] === "snapoff" ? "none" : "";
		update();
	});
	$("reset").addEventListener("click", () => {
		pre.scrollLeft = 0;
		vs.scrollTop = 0;
		if (document.activeElement instanceof HTMLElement) document.activeElement.blur();
		update();
	});
	$("guard").addEventListener("click", () => {
		guard = !guard;
		committed = rest() ?? "closed";
		free = false;
		update();
	});
	$("tabbar").addEventListener("click", () => {
		if (!invoke) {
			error = "no invoke";
			update();
			return;
		}
		const r = $("slot").getBoundingClientRect();
		// 与 plugins/oryn-mobile/fixtures/tab-bar-set.json 相同的项，frame 换成页面量出的矩形
		const spec = {
			items: [
				{ id: "threads", label: "线程", icon: { symbol: "bubble.left.and.bubble.right" } },
				{ id: "assistant", label: "小助手", icon: { glyph: "🦊" }, badge: "accent" },
				{ id: "projects", label: "项目", icon: { symbol: "folder" } },
				{ id: "more", label: "更多", icon: { symbol: "ellipsis" }, badge: "warn" },
			],
			selected: "threads",
			frame: { x: r.x, y: r.y, width: r.width, height: r.height },
			dark: false,
			accent: "#2f6fed",
			bg: "#f7f6f3",
		};
		invoke("host_tab_bar_set", { spec })
			.then(() => report())
			.catch((e) => {
				error = `set ${e}`;
				update();
			});
	});

	scrollTo({ left: max(), behavior: "instant" });
	update();
	report();
})();
