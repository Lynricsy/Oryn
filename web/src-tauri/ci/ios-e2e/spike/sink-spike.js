// M20 预案 C2 的验收探针页（ci/ios-sink-c2 分支专用，不合入；壳是 feat/ios-native-sink 的 bridge 8）。
// 读数：状态按钮文字 sx / sy / iw / max / vs / vy / kb / vst / pre / n / g / m / sk（sk = host_sink_set 是否生效）。
// 加载时先 host_sink_set {on: true}（SinkFollow 接管），程序化开合可选 native = host_sink_show（UIKit 动画）。
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
	let sk = 0;
	let sh = 0; // 页面发起 host_sink_show 的次数（invoke 之前自增：只证明网页发了，不证明原生收到）
	// 程序化开合的方式：0 smooth；1 instant；2 nosnap；3 snapoff（第 1–3 轮）；
	// 4 native = host_sink_show {animated: true}（预案 C2）；5 native-instant = host_sink_show {animated: false}；
	// 6 native-snapoff = 动画期间根上 scroll-snap-type: none，命中端点后恢复（C2 第 3–4 轮）；
	// 7 instant-snapoff = 网页 instant 落位，落位前关吸附、命中端点后恢复（C2 第 4 轮，减少动效的路径）；
	// 8 native-instant-snapoff = 原生一步落位，期间关吸附、命中端点后恢复（C2 第 5 轮）
	const MODES = [
		"smooth",
		"instant",
		"nosnap",
		"snapoff",
		"native",
		"native-instant",
		"native-snapoff",
		"instant-snapoff",
		"native-instant-snapoff",
	];
	let mode = 0;
	let restore = null;
	const go = (x) => {
		const m = MODES[mode];
		// 每次请求都清 rx：第 4 轮只在关吸附的方式里清，别的组读到的是上一组残留
		rx = -1;
		rb = -1;
		seq++;
		if (m.endsWith("snapoff") || m === "nosnap") {
			root.style.scrollSnapType = "none";
			restore = x;
		}
		if (m.startsWith("native")) {
			sh++;
			const animated = m === "native" || m === "native-snapoff";
			invoke?.("host_sink_show", { open: x === 0, animated }).catch((e) => {
				error = `show ${e}`;
				update();
			});
			update();
			return;
		}
		scrollTo({ left: x, behavior: m.startsWith("instant") ? "instant" : "smooth" });
	};
	// 恢复吸附：nosnap（第 1 轮）到位或 scrollend 后恢复；其余关吸附的方式只在命中目标端点后恢复（逐帧落位途中也会有
	// scrollend，第 3 轮在那时恢复等于中途重开吸附，实验不成立）。恢复写在下一帧：
	// rx = 写回 scrollSnapType 前一刻（同一个 rAF 里）的 scrollX，rb = 写回之后再一帧的 scrollX；-1 = 还没恢复过。
	// seq 防止上一次请求排下的 rAF 在新请求期间把吸附打开。
	let rx = -1;
	let rb = -1;
	let rt = -1; // 写回吸附的时刻（Date.now() 毫秒），与 events.jsonl 里 sink.trace 的 ts 对齐
	let seq = 0;
	const maybeRestore = (end) => {
		if (restore === null) return;
		const at = Math.abs(scrollX - restore) <= 0.5;
		if (!at && !(end && MODES[mode] === "nosnap")) return;
		restore = null;
		const mine = seq;
		requestAnimationFrame(() => {
			if (mine !== seq) return;
			rx = r1(scrollX);
			rt = Date.now();
			root.style.scrollSnapType = MODES[mode] === "snapoff" ? "none" : "";
			requestAnimationFrame(() => {
				if (mine !== seq) return;
				rb = r1(scrollX);
				update();
			});
			update();
		});
	};

	const update = () => {
		const v = window.visualViewport;
		const kb = v ? Math.round(innerHeight - v.height - v.offsetTop) : -1;
		status.textContent =
			`sx=${r1(scrollX)};sy=${r1(scrollY)};iw=${innerWidth};max=${r1(max())};` +
			`vs=${v ? r1(v.scale) : -1};vy=${v ? r1(v.offsetTop) : -1};kb=${kb};` +
			`vst=${Math.round(vs.scrollTop)};pre=${Math.round(pre.scrollLeft)};n=${n};g=${guard ? 1 : 0};m=${mode};sk=${sk};sh=${sh};rx=${rx};rb=${rb};rt=${rt}` +
			(error ? `;err ${error}` : "");
	};

	// bridge 8 没有逐帧上报卡片位置的命令（原生自己跟随），这里不再发
	const report = () => {};
	invoke?.("host_sink_set", { on: true })
		.then(() => {
			sk = 1;
			update();
		})
		.catch((e) => {
			error = `sink ${e}`;
			update();
		});

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
	// 守卫关着时不排静止定时器：否则点按钮 / 松手 100ms 后若还在中途，settle 会额外 go(nearer())，
	// 多发一次 host_sink_show（第 3 轮 native-snapoff 的 sh +2），污染程序化开合的对照
	const touch = (e) => {
		touching = e.touches.length;
		if (!guard) return;
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
