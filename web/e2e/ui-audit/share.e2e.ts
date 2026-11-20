/** 匿名分享页 `/s/:slug`：快照与实时（主线程、role 线程）。 */
import { surface } from "./audit";

const READY = "[data-testid=transcript]";
surface({
	id: "share.snapshot",
	path: "/s/5a0c1e7f3b9d42a8c6e1f0b2d4a69c3e",
	ready: READY,
});
surface({
	id: "share.live",
	path: "/s/c3e9a14b7d2f4e6a8b0c5d1e9f2a7b64",
	ready: READY,
});
surface({
	id: "share.role-snapshot",
	path: "/s/9d6e2b17c4a84f30b5e1d8c27a9f0e61",
	ready: READY,
});
surface({
	id: "share.gone",
	path: "/s/00000000000000000000000000000000",
	ready: "text=链接不存在或已被撤销",
});
