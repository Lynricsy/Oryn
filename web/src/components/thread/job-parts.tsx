/** 后台任务的共用小件：状态外观、任务字形（带状态角标）、一行名字与最近一行输出。面板分组与任务对话框共用。 */
import type { ReactNode } from "react";
import type { JobStatus, JobView } from "../../state/jobs";
import type { Tone } from "../ui/data";
import { Icon, type IconName } from "../ui/icons";

export const JOB_STATUS_LOOK: Record<
	JobStatus,
	{ icon: IconName; tone: Tone }
> = {
	running: { icon: "timer", tone: "neutral" },
	completed: { icon: "checkCircle", tone: "ok" },
	failed: { icon: "xCircle", tone: "err" },
	cancelled: { icon: "minusCircle", tone: "warn" },
	ended: { icon: "circle", tone: "neutral" },
};

export const JOB_TONE_TEXT: Record<Tone, string> = {
	neutral: "text-muted",
	accent: "text-accent",
	ok: "text-ok",
	warn: "text-warn",
	err: "text-err",
	frost: "text-frost",
};

const TYPE_ICON: Record<string, IconName> = {
	bash: "terminal",
	eval: "code",
	task: "robot",
};

const BADGE_FILL: Record<JobStatus, string> = {
	running: "bg-accent",
	completed: "bg-ok",
	failed: "bg-err",
	cancelled: "bg-warn",
	ended: "bg-ghost",
};

/**
 * 任务字形：类型图标放在浅底方块里，右下角一枚状态角标（与子 agent 头像的状态角标同一位置）。
 * 运行中角标向外扩散；减弱动效时只留实心点。
 */
export function JobGlyph({
	job,
	size = 24,
}: {
	job: Pick<JobView, "type" | "status">;
	size?: number;
}): ReactNode {
	const dot = Math.max(7, Math.round(size * 0.32));
	return (
		<span
			aria-hidden
			className="relative grid shrink-0 place-items-center rounded-md bg-inset text-muted"
			style={{ width: size, height: size }}
		>
			<Icon
				name={TYPE_ICON[job.type] ?? "hourglass"}
				size={Math.round(size * 0.56)}
			/>
			<span
				className="absolute grid place-items-center rounded-full ring-2 ring-panel"
				style={{ width: dot, height: dot, right: -2, bottom: -2 }}
			>
				{job.status === "running" && (
					<span
						className={`absolute inset-0 animate-ping rounded-full opacity-50 motion-reduce:hidden ${BADGE_FILL.running}`}
					/>
				)}
				<span
					className={`relative h-full w-full rounded-full ${BADGE_FILL[job.status]}`}
				/>
			</span>
		</span>
	);
}

/** 任务的一行名字：模型写的意图优先，其次标签（命令首行），最后 id。 */
export function jobHeadline(job: JobView): string {
	return job.intent || job.label || job.id;
}

/** 正在做什么：小组件的阶段、输出的最后一个非空行，都没有时用 Core 的说明。 */
export function jobStage(job: JobView): string | undefined {
	if (job.widget?.stage) return job.widget.stage;
	const lines = job.output?.trimEnd().split("\n") ?? [];
	for (let i = lines.length - 1; i >= 0; i--) {
		const line = lines[i]?.trim();
		if (line) return line;
	}
	return job.caption;
}

/** 已运行 / 运行了多久：结束的任务用冻结的耗时，运行中的按当前时刻算。 */
export function jobElapsed(job: JobView, now: number): number | undefined {
	if (job.durationMs !== undefined) return job.durationMs;
	if (job.startedAt === undefined) return undefined;
	const end = job.status === "running" ? now : job.endedAt;
	return end === undefined ? undefined : Math.max(0, end - job.startedAt);
}
