/** 信息面板「环境」小节的快照来源（M11a）。 */
import { render, screen } from "@testing-library/react";
import { beforeEach, expect, it } from "vitest";
import type { Schemas } from "../../api/client";
import i18n from "../../i18n";
import { SnapshotOrigin } from "./InfoPanel";

const RUN: Schemas["OrbSetupRun"] = {
	id: "osr_1",
	orbId: "orb-1",
	phase: "setup",
	trigger: "auto",
	startedAt: 1,
	finishedAt: 2,
	ok: true,
	setupRan: false,
	durationMs: 1,
	hasRepoScript: true,
	hasProjectScript: false,
};

beforeEach(() => {
	i18n.changeLanguage("zh-CN");
});

it("来自项目快照：显示提交前 7 位；未刷新时不提增量重跑", () => {
	render(
		<SnapshotOrigin
			orb={{
				snapshotKind: "project",
				lastSetup: { ...RUN, prepared: true, baseCommit: "abc1234def5678" },
			}}
		/>,
	);
	const box = screen.getByTestId("env-snapshot");
	expect(box).toHaveTextContent("来自项目快照 · 提交 abc1234");
	expect(box).not.toHaveTextContent("abc1234d");
	expect(box).not.toHaveTextContent(i18n.t("accel.refreshed"));
});

it("快照上增量重跑过 setup：两行都显示", () => {
	render(
		<SnapshotOrigin
			orb={{
				snapshotKind: "project",
				lastSetup: { ...RUN, prepared: true, setupRan: true },
			}}
		/>,
	);
	const box = screen.getByTestId("env-snapshot");
	expect(box).toHaveTextContent(i18n.t("accel.fromSnapshot"));
	expect(box).toHaveTextContent("基线有新提交，已增量重跑 setup");
});

it("fork 快照或普通 orb：不显示", () => {
	const { rerender } = render(
		<SnapshotOrigin
			orb={{ snapshotKind: "fork", lastSetup: { ...RUN, setupRan: true } }}
		/>,
	);
	expect(screen.queryByTestId("env-snapshot")).toBeNull();
	rerender(<SnapshotOrigin orb={{ lastSetup: RUN }} />);
	expect(screen.queryByTestId("env-snapshot")).toBeNull();
});
