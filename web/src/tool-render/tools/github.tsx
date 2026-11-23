/**
 * `github` — gh CLI dispatch（oryn-core `tools/gh.ts`）：仓库概览、读文件、PR 创建 / 检出 / 推送、五种搜索、Actions 盯运行。
 * 头部：op 的中文名 + 主要对象（查询、PR、分支……），读数按 op：搜索的结果数、文件行数、CI 的作业进度与结论。
 * 展开体按 op 给原生形态：搜索是查询栏 + 结果列表（issue / PR 带状态图标、编号、标题链接、作者与标签；
 * 代码命中带高亮片段；提交带短 sha；仓库带语言与星数），读文件按路径高亮，盯运行是工作流 / 作业的状态列表。
 * 搜索结果只在正文里（Core 的 `formatSearch*Results` 文本），这里按那个格式解析；解析不出时退回 Markdown。
 * details（`GhToolDetails`）残缺或类型不对时一律当作没有。
 */
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { Icon, type IconName } from "../../components/ui/icons";
import { formatDuration, formatRelative } from "../../lib/format";
import { SmartText } from "../generic";
import {
	Badge,
	type CellTone,
	CodeLines,
	EmptyNote,
	ErrorBlock,
	FileHead,
	InvalidArg,
	Kv,
	KvGrid,
	MarkdownBlock,
	MetaCount,
	Note,
	Output,
	PathText,
	ProgressCells,
	ResultImages,
} from "../parts";
import type { ToolRenderer, ToolRenderProps, ToolResultLike } from "../types";
import {
	detailsRecord,
	isRecord,
	languageFromPath,
	normalizeWs,
	num,
	resultImagesOf,
	resultTextOf,
	shortenPath,
	str,
	truncate,
} from "../util";

const OPS = [
	"repo_view",
	"file_read",
	"pr_create",
	"pr_checkout",
	"pr_push",
	"search_issues",
	"search_prs",
	"search_code",
	"search_commits",
	"search_repos",
	"run_watch",
] as const;
type GhOp = (typeof OPS)[number];

function opOf(args: Record<string, unknown>): GhOp | null {
	const op = str(args.op);
	return op && (OPS as readonly string[]).includes(op) ? (op as GhOp) : null;
}

const SUCCESS_CONCLUSIONS: Record<string, true> = {
	success: true,
	neutral: true,
	skipped: true,
};
const FAILURE_CONCLUSIONS: Record<string, true> = {
	failure: true,
	timed_out: true,
	cancelled: true,
	action_required: true,
	startup_failure: true,
};
const RUNNING_STATUSES: Record<string, true> = { in_progress: true };

/** `123`, a PR/issue URL, or a branch name → `#123` or a truncated literal. */
function issueId(value: string): string | null {
	const trimmed = value.trim();
	if (!trimmed) return null;
	if (/^\d+$/.test(trimmed)) return `#${trimmed}`;
	const match = trimmed.match(/\/(?:issues|pull)\/(\d+)/);
	if (match) return `#${match[1]}`;
	return truncate(trimmed, 40);
}

function formatPr(pr: unknown): string | null {
	if (typeof pr === "string") return issueId(pr);
	if (!Array.isArray(pr)) return null;
	const parts: string[] = [];
	for (const item of pr) {
		if (typeof item !== "string") continue;
		const id = issueId(item);
		if (id) parts.push(id);
	}
	if (parts.length === 0) return null;
	if (parts.length > 3)
		return `${parts.slice(0, 3).join(", ")}, +${parts.length - 3}`;
	return parts.join(", ");
}

function shortSha(sha: string): string {
	return /^[0-9a-f]{12,}$/i.test(sha) ? sha.slice(0, 7) : sha;
}

/** 字符串或字符串数组参数 → 字符串列表（其他类型为空）。 */
function strList(value: unknown): string[] {
	if (typeof value === "string") return value.trim() ? [value] : [];
	if (!Array.isArray(value)) return [];
	return value.filter((v): v is string => typeof v === "string" && v !== "");
}

/** ISO 时间 → 相对时间（「3 天前」）；解析不了时原样。 */
function when(iso: string | undefined): string | null {
	if (!iso) return null;
	const ts = Date.parse(iso);
	return Number.isFinite(ts) ? formatRelative(ts) : iso;
}

/** 外链：新窗口打开，不带来源。分享页同样可点（指向 GitHub，不依赖宿主）。 */
function ExtLink({
	href,
	className,
	children,
}: {
	href: string | null | undefined;
	className?: string;
	children: ReactNode;
}): ReactNode {
	if (!href || !/^https?:\/\//.test(href))
		return <span className={className}>{children}</span>;
	return (
		<a
			className={`tv-gh-link${className ? ` ${className}` : ""}`}
			href={href}
			target="_blank"
			rel="noreferrer noopener"
		>
			{children}
		</a>
	);
}

// ---- 头部 ----

function Salient({
	args,
	result,
}: Pick<ToolRenderProps, "args" | "result">): ReactNode {
	const { t } = useTranslation();
	const op = opOf(args);
	const repo = str(args.repo);
	if (op?.startsWith("search_")) {
		const query = str(args.query);
		return (
			<>
				{query && (
					<span className="tv-pattern">{truncate(normalizeWs(query), 60)}</span>
				)}
				{repo && <span className="tv-faint">{repo}</span>}
			</>
		);
	}
	if (op === "file_read") {
		const path = str(args.path);
		return (
			<>
				{path ? <PathText path={path} /> : <InvalidArg what="path" />}
				{repo && <span className="tv-faint">{repo}</span>}
			</>
		);
	}
	if (op === "pr_checkout" || op === "pr_push") {
		const target = formatPr(args.pr) ?? str(args.branch);
		return (
			<>
				{target && <span>{target}</span>}
				{repo && <span className="tv-faint">{repo}</span>}
			</>
		);
	}
	if (op === "pr_create") {
		const title = str(args.title);
		if (title) return <span>{truncate(normalizeWs(title), 60)}</span>;
		// `xd://github` 派发只回传 {op, repo}（标题与正文不进 details）：改用结果里的 PR 链接
		const created = createdPr(result);
		if (created)
			return (
				<>
					<span>#{created.number}</span>
					{repo && <span className="tv-faint">{repo}</span>}
				</>
			);
		return args.fill === true ? (
			<span className="tv-faint">{t("toolRender.github.fill")}</span>
		) : repo ? (
			<span className="tv-faint">{repo}</span>
		) : null;
	}
	if (op === "run_watch") {
		const run = str(args.run);
		const branch = str(args.branch);
		if (run)
			return (
				<span>{t("toolRender.github.run", { run: truncate(run, 50) })}</span>
			);
		return (
			<span className="tv-faint">
				{t("toolRender.github.runsOf", { target: branch ?? "HEAD" })}
			</span>
		);
	}
	const branch = str(args.branch);
	return (
		<>
			{repo && <span>{repo}</span>}
			{branch && <span className="tv-faint">{branch}</span>}
		</>
	);
}

function Summary(props: ToolRenderProps): ReactNode {
	const { t } = useTranslation();
	const raw = str(props.args.op);
	const op = opOf(props.args);
	return (
		<>
			{raw ? (
				<Badge>{op ? t(`toolRender.github.op.${op}`) : raw}</Badge>
			) : (
				<Badge tone="warn">{t("toolRender.github.noOp")}</Badge>
			)}
			<Salient args={props.args} result={props.result} />
		</>
	);
}

/** 盯运行的全部作业（单个运行或这个提交的全部运行）。 */
function watchJobs(watch: Record<string, unknown>): Record<string, unknown>[] {
	const runs = isRecord(watch.run)
		? [watch.run]
		: Array.isArray(watch.runs)
			? watch.runs.filter(isRecord)
			: [];
	const jobs: Record<string, unknown>[] = [];
	for (const run of runs)
		if (Array.isArray(run.jobs)) jobs.push(...run.jobs.filter(isRecord));
	return jobs;
}

function jobTone(job: Record<string, unknown>): CellTone {
	const conclusion = str(job.conclusion);
	const status = str(job.status);
	if (conclusion && SUCCESS_CONCLUSIONS[conclusion]) return "ok";
	if (conclusion && FAILURE_CONCLUSIONS[conclusion]) return "err";
	if (status && RUNNING_STATUSES[status]) return "accent";
	return "idle";
}

function watchOf(
	result: ToolResultLike | undefined,
): Record<string, unknown> | null {
	const details = detailsRecord(result);
	return details && isRecord(details.watch) ? details.watch : null;
}

/** 右侧读数：搜索的结果数、读到的文件行数、CI 作业的进度格与结论、新建 PR 的编号。 */
function Meta({ args, result, partial }: ToolRenderProps): ReactNode {
	const { t } = useTranslation();
	const op = opOf(args);
	const watch = watchOf(result ?? partial);
	if (op === "run_watch" && watch) {
		const tones = watchJobs(watch).map(jobTone);
		if (tones.length === 0) return null;
		const ok = tones.filter((x) => x === "ok").length;
		const failed = tones.filter((x) => x === "err").length;
		return (
			<>
				<ProgressCells
					tones={tones}
					label={t("toolRender.github.jobsDone", {
						done: ok,
						total: tones.length,
					})}
				/>
				<span>
					{ok}/{tones.length}
				</span>
				{failed > 0 && (
					<span className="tv-meta-err">
						{t("toolRender.github.jobsFailed", { count: failed })}
					</span>
				)}
			</>
		);
	}
	if (!result || result.isError) return null;
	const text = resultTextOf(result);
	if (op?.startsWith("search_")) {
		const listing = parseListing(text);
		if (!listing) return null;
		return (
			<span className={listing.items.length === 0 ? "tv-meta-warn" : undefined}>
				{t("toolRender.github.results", { count: listing.items.length })}
			</span>
		);
	}
	if (op === "file_read" && resultImagesOf(result).length === 0) {
		if (!text.trim() || NO_BYTES_RE.test(text)) return null;
		return (
			<MetaCount
				unit="lines"
				count={text.replace(/\n$/, "").split("\n").length}
			/>
		);
	}
	if (op === "pr_create") {
		const created = createdPr(result);
		return created ? (
			<span className="tv-meta-ok">#{created.number}</span>
		) : null;
	}
	return null;
}

// ---- 输入段 ----

function argValue(value: unknown): ReactNode {
	if (typeof value === "string") return value;
	if (typeof value === "number" || typeof value === "boolean")
		return String(value);
	if (Array.isArray(value)) {
		const parts: string[] = [];
		for (const item of value) {
			if (typeof item === "string" || typeof item === "number")
				parts.push(String(item));
		}
		return parts.join(", ");
	}
	return <InvalidArg />;
}

/**
 * 其余参数的键值表：op（头部徽标）、body（下面另有 Markdown）与上方专门排版过的参数不重复。
 * 折叠行里只是摘要（会被截断、PR 超过 3 个写成 +N），这里始终给出全文。
 */
function ArgsGrid({
	args,
	skip,
}: {
	args: Record<string, unknown>;
	skip: readonly string[];
}): ReactNode {
	const rows: ReactNode[] = [];
	for (const key in args) {
		if (key === "op" || key === "body" || skip.includes(key)) continue;
		const value = args[key];
		if (value === undefined || value === null) continue;
		rows.push(
			<Kv k={key} key={key}>
				{argValue(value)}
			</Kv>,
		);
	}
	return rows.length > 0 ? <KvGrid>{rows}</KvGrid> : null;
}

/** 搜索的查询栏：放大镜 + 查询原文 + 范围（仓库、时间窗）。 */
function QueryBar({ args }: { args: Record<string, unknown> }): ReactNode {
	const query = str(args.query);
	const repo = str(args.repo);
	const since = str(args.since);
	const until = str(args.until);
	const window =
		since || until
			? `${str(args.dateField) ?? ""}${str(args.dateField) ? " " : ""}${since ?? "…"} → ${until ?? "…"}`
			: null;
	return (
		<div className="tv-gh-query">
			<span className="tv-gh-query-icon" aria-hidden="true">
				<Icon name="search" size={12} />
			</span>
			{query !== null ? (
				<span className="tv-gh-query-text">{query}</span>
			) : (
				<InvalidArg what="query" />
			)}
			{repo && <Badge>{repo}</Badge>}
			{window && <Badge>{window}</Badge>}
		</div>
	);
}

/** 文件引用：`owner/repo @ branch : path`。 */
function FileRef({ args }: { args: Record<string, unknown> }): ReactNode {
	const repo = str(args.repo);
	const branch = str(args.branch);
	const path = str(args.path);
	return (
		<div className="tv-gh-ref">
			<Icon name="github" size={12} />
			{repo && <span className="tv-muted">{repo}</span>}
			{branch && (
				<span className="tv-gh-branch">
					<Icon name="branch" size={11} />
					{branch}
				</span>
			)}
			{path ? <PathText path={path} /> : <InvalidArg what="path" />}
		</div>
	);
}

/** 新 PR：标题、head → base 分支流向、草稿与评审人等徽标。 */
function PrDraft({ args }: { args: Record<string, unknown> }): ReactNode {
	const { t } = useTranslation();
	const title = str(args.title);
	const head = str(args.head);
	const base = str(args.base);
	const people = [
		...strList(args.reviewer).map((r) => `@${r.replace(/^@/, "")}`),
		...strList(args.assignee).map((a) => `→ @${a.replace(/^@/, "")}`),
	];
	const labels = strList(args.label);
	return (
		<div className="tv-gh-pr">
			{title ? (
				<div className="tv-gh-pr-title">{title}</div>
			) : (
				args.fill === true && (
					<div className="tv-faint">{t("toolRender.github.fill")}</div>
				)
			)}
			<div className="tv-gh-pr-line">
				{(head || base) && (
					<span className="tv-gh-branch">
						<Icon name="branch" size={11} />
						{head ?? "HEAD"}
						<span className="tv-faint"> → </span>
						{base ?? t("toolRender.github.defaultBranch")}
					</span>
				)}
				{args.draft === true && <Badge>{t("toolRender.github.draft")}</Badge>}
				{people.map((p) => (
					<Badge key={p}>{p}</Badge>
				))}
				{labels.map((l) => (
					<Badge key={`label:${l}`}>{l}</Badge>
				))}
			</div>
		</div>
	);
}

function Input({ args }: ToolRenderProps): ReactNode {
	const op = opOf(args);
	const body = str(args.body);
	let lead: ReactNode = null;
	let skip: string[] = [];
	if (op?.startsWith("search_")) {
		lead = <QueryBar args={args} />;
		skip = ["query", "repo", "since", "until", "dateField"];
	} else if (op === "file_read") {
		lead = <FileRef args={args} />;
		skip = ["repo", "branch", "path"];
	} else if (op === "pr_create") {
		lead = <PrDraft args={args} />;
		skip = [
			"title",
			"head",
			"base",
			"draft",
			"reviewer",
			"assignee",
			"label",
			...(str(args.title) ? [] : ["fill"]),
		];
	}
	return (
		<>
			{lead}
			<ArgsGrid args={args} skip={skip} />
			{body && <MarkdownBlock text={body} maxHeight={200} />}
		</>
	);
}

// ---- 输出段：搜索结果 ----

interface ListingItem {
	head: string;
	fields: Record<string, string>;
}

interface Listing {
	items: ListingItem[];
	/** 没有结果时 Core 写的那句（`No issues found.`）。 */
	empty: string | null;
}

/** `- #12 Title` 之后两格缩进的 `Key: value` 行。 */
const FIELD_RE = /^ {2}([A-Za-z][A-Za-z ]*): (.*)$/;
/** 旧格式 / 简写：`#412 title (open)`。 */
const SHORT_ITEM_RE = /^#(\d+) (.+?)(?: \((open|closed|merged)\))?$/;

/** 解析 Core `formatSearch*Results` 的文本；不是这个形状时为 null。 */
function parseListing(text: string): Listing | null {
	const items: ListingItem[] = [];
	let empty: string | null = null;
	let header = false;
	let current: ListingItem | null = null;
	for (const line of text.split("\n")) {
		if (/^# GitHub .+ search$/.test(line)) {
			header = true;
			continue;
		}
		if (line.startsWith("- ")) {
			current = { head: line.slice(2), fields: {} };
			items.push(current);
			continue;
		}
		const field = current ? FIELD_RE.exec(line) : null;
		if (field && current) {
			current.fields[field[1] ?? ""] = field[2] ?? "";
			continue;
		}
		const short = SHORT_ITEM_RE.exec(line.trim());
		if (short && !header) {
			current = {
				head: `#${short[1]} ${short[2]}`,
				fields: short[3] ? { State: short[3] } : {},
			};
			items.push(current);
			continue;
		}
		if (/^No .+ found\.$/.test(line.trim())) empty = line.trim();
		if (!line.trim()) current = null;
	}
	if (!header && items.length === 0) return null;
	return { items, empty };
}

const ISSUE_HEAD_RE = /^#(\S+) (.*)$/;

function StateIcon({
	state,
	pr,
}: {
	state: string | undefined;
	pr: boolean;
}): ReactNode {
	const { t } = useTranslation();
	const raw = (state ?? "").toLowerCase();
	const s = raw === "open" || raw === "closed" || raw === "merged" ? raw : null;
	const icon: IconName =
		s === "merged"
			? "merge"
			: pr
				? "pullRequest"
				: s === "closed"
					? "checkCircle"
					: "circle";
	return (
		<span
			className={`tv-gh-state tv-gh-state--${s ?? "unknown"}`}
			title={state}
			role="img"
			aria-label={s ? t(`toolRender.github.state.${s}`) : (state ?? "?")}
		>
			<Icon name={icon} size={13} />
		</span>
	);
}

/** 一行次要信息：各段以「·」分隔，空段跳过。 */
function Dots({ parts }: { parts: ReactNode[] }): ReactNode {
	const shown = parts.filter((p) => p != null && p !== "" && p !== false);
	if (shown.length === 0) return null;
	return (
		<div className="tv-gh-sub">
			{shown.map((p, i) => (
				<span key={i}>
					{i > 0 && <span className="tv-gh-dot">·</span>}
					{p}
				</span>
			))}
		</div>
	);
}

function IssueRow({ item, pr }: { item: ListingItem; pr: boolean }): ReactNode {
	const m = ISSUE_HEAD_RE.exec(item.head);
	const number = m?.[1] ?? "?";
	const title = m?.[2] ?? item.head;
	const f = item.fields;
	const labels = (f.Labels ?? "")
		.split(",")
		.map((l) => l.trim())
		.filter(Boolean);
	return (
		<li className="tv-gh-item">
			<StateIcon state={f.State} pr={pr} />
			<div className="tv-gh-item-main">
				<div className="tv-gh-item-head">
					<ExtLink href={f.URL} className="tv-gh-title">
						{title}
					</ExtLink>
					<span className="tv-gh-num">#{number}</span>
				</div>
				<Dots
					parts={[
						f.Repo,
						f.Author,
						when(f.Updated ?? f.Created),
						labels.length > 0 && (
							<span className="tv-gh-labels">
								{labels.map((l) => (
									<Badge key={l}>{l}</Badge>
								))}
							</span>
						),
					]}
				/>
			</div>
		</li>
	);
}

function CodeRow({ item }: { item: ListingItem }): ReactNode {
	const f = item.fields;
	const path = item.head;
	return (
		<li className="tv-gh-item">
			<span className="tv-gh-state" aria-hidden="true">
				<Icon name="fileCode" size={13} />
			</span>
			<div className="tv-gh-item-main">
				<div className="tv-gh-item-head">
					<ExtLink href={f.URL} className="tv-gh-title tv-gh-mono">
						{shortenPath(path)}
					</ExtLink>
				</div>
				<Dots parts={[f.Repo, f.Commit && shortSha(f.Commit)]} />
				{f.Match && (
					<div className="tv-gh-frag">
						<Output
							text={f.Match}
							lang={languageFromPath(path)}
							variant="code"
							maxLines={3}
						/>
					</div>
				)}
			</div>
		</li>
	);
}

function CommitRow({ item }: { item: ListingItem }): ReactNode {
	const f = item.fields;
	const sp = item.head.indexOf(" ");
	const sha = sp > 0 ? item.head.slice(0, sp) : item.head;
	const subject = sp > 0 ? item.head.slice(sp + 1) : "";
	return (
		<li className="tv-gh-item">
			<span className="tv-gh-state" aria-hidden="true">
				<Icon name="commit" size={13} />
			</span>
			<div className="tv-gh-item-main">
				<div className="tv-gh-item-head">
					<ExtLink href={f.URL} className="tv-gh-sha">
						{shortSha(sha)}
					</ExtLink>
					<span className="tv-gh-title">{subject}</span>
				</div>
				<Dots parts={[f.Repo, f.Author, when(f.Date)]} />
			</div>
		</li>
	);
}

function RepoRow({ item }: { item: ListingItem }): ReactNode {
	const { t } = useTranslation();
	const f = item.fields;
	const stars = f.Stars ? Number(f.Stars) : null;
	return (
		<li className="tv-gh-item">
			<span className="tv-gh-state" aria-hidden="true">
				<Icon name="hostedRepo" size={13} />
			</span>
			<div className="tv-gh-item-main">
				<div className="tv-gh-item-head">
					<ExtLink href={f.URL} className="tv-gh-title">
						{item.head}
					</ExtLink>
					{f.Archived === "true" && (
						<Badge tone="warn">{t("toolRender.github.archived")}</Badge>
					)}
					{f.Fork === "true" && <Badge>{t("toolRender.github.fork")}</Badge>}
				</div>
				{f.Description && <div className="tv-gh-desc">{f.Description}</div>}
				<Dots
					parts={[
						f.Language,
						stars !== null &&
							Number.isFinite(stars) &&
							`★ ${stars.toLocaleString()}`,
						f.Forks && t("toolRender.github.forks", { count: Number(f.Forks) }),
						when(f.Updated),
					]}
				/>
			</div>
		</li>
	);
}

function SearchResult({ op, text }: { op: GhOp; text: string }): ReactNode {
	const { t } = useTranslation();
	const listing = parseListing(text);
	if (!listing) return <SmartText text={text} />;
	if (listing.items.length === 0)
		return <EmptyNote>{t("toolRender.github.noResults")}</EmptyNote>;
	return (
		<ul className="tv-gh-list">
			{listing.items.map((item, i) =>
				op === "search_code" ? (
					<CodeRow key={i} item={item} />
				) : op === "search_commits" ? (
					<CommitRow key={i} item={item} />
				) : op === "search_repos" ? (
					<RepoRow key={i} item={item} />
				) : (
					<IssueRow key={i} item={item} pr={op === "search_prs"} />
				),
			)}
		</ul>
	);
}

// ---- 输出段：盯运行 ----

function conclusionTone(
	conclusion: string | null,
	status: string | null,
): "ok" | "err" | "accent" | undefined {
	if (conclusion && SUCCESS_CONCLUSIONS[conclusion]) return "ok";
	if (conclusion && FAILURE_CONCLUSIONS[conclusion]) return "err";
	if (status && RUNNING_STATUSES[status]) return "accent";
	return undefined;
}

/** 有中文名的运行状态 / 结论（GitHub 的取值）；其余原样显示。 */
const STATUS_KEYS = [
	"success",
	"failure",
	"cancelled",
	"skipped",
	"timed_out",
	"neutral",
	"action_required",
	"startup_failure",
	"in_progress",
	"queued",
	"waiting",
	"pending",
	"completed",
] as const;

function isStatusKey(key: string): key is (typeof STATUS_KEYS)[number] {
	return (STATUS_KEYS as readonly string[]).includes(key);
}

function StatusLabel({
	conclusion,
	status,
}: {
	conclusion: string | null;
	status: string | null;
}): ReactNode {
	const { t } = useTranslation();
	const key = conclusion ?? status;
	if (!key) return null;
	return (
		<Badge tone={conclusionTone(conclusion, status)}>
			{isStatusKey(key) ? t(`toolRender.github.status.${key}`) : key}
		</Badge>
	);
}

const JOB_ICON: Record<CellTone, IconName> = {
	ok: "checkCircle",
	err: "xCircle",
	accent: "circleHalf",
	warn: "alert",
	idle: "circle",
};

function JobRow({ job }: { job: Record<string, unknown> }): ReactNode {
	const tone = jobTone(job);
	const duration = num(job.durationSeconds);
	const steps = Array.isArray(job.steps) ? job.steps.filter(isRecord) : [];
	// 运行中的作业写当前步骤，失败的作业写失败的那一步
	const step =
		tone === "accent"
			? steps.find((s) => str(s.status) === "in_progress")
			: tone === "err"
				? steps.find((s) => {
						const c = str(s.conclusion);
						return c !== null && FAILURE_CONCLUSIONS[c] === true;
					})
				: undefined;
	// 跳过的作业算通过（不拖累结论），但图标与文字弱化，和真正跑过的区分开
	const skipped = str(job.conclusion) === "skipped";
	return (
		<li className={`tv-gh-job tv-gh-job--${skipped ? "skipped" : tone}`}>
			<span className="tv-gh-job-icon" aria-hidden="true">
				<Icon name={skipped ? "minusCircle" : JOB_ICON[tone]} size={12} />
			</span>
			<ExtLink href={str(job.url)} className="tv-gh-job-name">
				{str(job.name) ?? "job"}
			</ExtLink>
			{step && <span className="tv-gh-job-step">{str(step.name)}</span>}
			{duration !== null && (
				<span className="tv-gh-job-time">
					{formatDuration(duration * 1000)}
				</span>
			)}
		</li>
	);
}

function RunBlock({ run }: { run: Record<string, unknown> }): ReactNode {
	const { t } = useTranslation();
	const label =
		str(run.workflowName) ?? str(run.displayTitle) ?? "GitHub Actions";
	const branch = str(run.branch);
	const sha = str(run.headSha);
	const id = num(run.id);
	const jobs = Array.isArray(run.jobs) ? run.jobs.filter(isRecord) : [];
	return (
		<div className="tv-gh-run">
			<div className="tv-gh-run-head">
				<ExtLink href={str(run.url)} className="tv-gh-run-name">
					{label}
				</ExtLink>
				{(branch || sha) && (
					<span className="tv-gh-branch">
						<Icon name={branch ? "branch" : "commit"} size={11} />
						{branch ?? shortSha(sha ?? "")}
					</span>
				)}
				{id !== null && <span className="tv-gh-num">#{id}</span>}
				<StatusLabel
					conclusion={str(run.conclusion)}
					status={str(run.status)}
				/>
			</div>
			{jobs.length === 0 ? (
				<div className="tv-gh-wait">{t("toolRender.github.waitingJobs")}</div>
			) : (
				<ul className="tv-gh-jobs">
					{jobs.map((job, index) => (
						<JobRow key={num(job.id) ?? index} job={job} />
					))}
				</ul>
			)}
		</div>
	);
}

function WatchView({ watch }: { watch: Record<string, unknown> }): ReactNode {
	const { t } = useTranslation();
	const note = str(watch.note);
	const runs: Record<string, unknown>[] = isRecord(watch.run)
		? [watch.run]
		: Array.isArray(watch.runs)
			? watch.runs.filter(isRecord)
			: [];
	const failedLogs = Array.isArray(watch.failedLogs)
		? watch.failedLogs.filter(isRecord)
		: [];
	return (
		<>
			{note && <div className="tv-gh-wait">{note}</div>}
			{runs.length === 0 && (
				<div className="tv-gh-wait">{t("toolRender.github.waitingRuns")}</div>
			)}
			{runs.map((item, index) => (
				<RunBlock run={item} key={num(item.id) ?? index} />
			))}
			{failedLogs.map((entry, index) => {
				const jobName = str(entry.jobName) ?? "job";
				const workflow = str(entry.workflowName);
				const failedRunId = num(entry.runId);
				const title = [
					jobName,
					workflow,
					failedRunId !== null ? `#${failedRunId}` : null,
				]
					.filter(Boolean)
					.join(" · ");
				const tail = str(entry.tail);
				if (!tail || entry.available === false) {
					return (
						<Note tone="warn" key={index}>
							{t("toolRender.github.logUnavailable", { title })}
							{str(entry.unavailableReason) &&
								`: ${str(entry.unavailableReason)}`}
						</Note>
					);
				}
				return (
					<Output text={tail} maxLines={12} error title={title} key={index} />
				);
			})}
		</>
	);
}

// ---- 输出段：其余 op ----

function CheckoutRows({
	checkouts,
}: {
	checkouts: readonly Record<string, unknown>[];
}): ReactNode {
	const { t } = useTranslation();
	return (
		<ul className="tv-gh-list">
			{checkouts.map((entry, index) => {
				const prNumber = num(entry.prNumber);
				const worktree = str(entry.worktreePath);
				return (
					<li className="tv-gh-item" key={prNumber ?? index}>
						<span className="tv-gh-state tv-gh-state--open" aria-hidden="true">
							<Icon name="pullRequest" size={13} />
						</span>
						<div className="tv-gh-item-main">
							<div className="tv-gh-item-head">
								<ExtLink href={str(entry.url)} className="tv-gh-num">
									{prNumber !== null ? `#${prNumber}` : "PR"}
								</ExtLink>
								<span className="tv-gh-branch">
									<Icon name="branch" size={11} />
									{str(entry.branch) ?? ""}
								</span>
								{entry.reused === true && (
									<Badge>{t("toolRender.github.reused")}</Badge>
								)}
							</div>
							{worktree && (
								<div className="tv-gh-sub">
									<PathText path={worktree} />
								</div>
							)}
						</div>
					</li>
				);
			})}
		</ul>
	);
}

const DETAIL_KEYS = [
	"branch",
	"worktreePath",
	"remote",
	"remoteBranch",
	"headSha",
	"runId",
	"status",
	"conclusion",
];

/** 推送等操作回报的仓库状态；与调用参数相同的不重复。 */
function DetailsGrid({
	details,
	args,
}: {
	details: Record<string, unknown>;
	args: Record<string, unknown>;
}): ReactNode {
	const rows: ReactNode[] = [];
	for (const key of DETAIL_KEYS) {
		const value = details[key];
		if (value === args[key]) continue;
		if (typeof value === "number") {
			rows.push(
				<Kv k={key} key={key}>
					{String(value)}
				</Kv>,
			);
		} else if (typeof value === "string" && value) {
			const text =
				key === "worktreePath"
					? shortenPath(value)
					: key === "headSha"
						? shortSha(value)
						: value;
			rows.push(
				<Kv k={key} key={key}>
					{text}
				</Kv>,
			);
		}
	}
	const ids = Array.isArray(details.runIds)
		? details.runIds.filter((id): id is number => typeof id === "number")
		: [];
	if (ids.length > 0)
		rows.push(
			<Kv k="runs" key="runs">
				{ids.map((id) => `#${id}`).join(", ")}
			</Kv>,
		);
	const failed = strList(details.failedJobs);
	if (failed.length > 0)
		rows.push(
			<Kv k="failedJobs" key="failedJobs">
				<span className="tv-err-text">{failed.join(", ")}</span>
			</Kv>,
		);
	return rows.length > 0 ? <KvGrid>{rows}</KvGrid> : null;
}

const PR_URL_RE = /https:\/\/\S+\/pull\/(\d+)/;

function createdPr(
	result: ToolResultLike | undefined,
): { url: string; number: string } | null {
	if (!result || result.isError) return null;
	const m = PR_URL_RE.exec(resultTextOf(result));
	return m ? { url: m[0], number: m[1] ?? "" } : null;
}

/** GitHub 不给文件字节（大文件、子模块）时 Core 的说明。 */
const NO_BYTES_RE = /^\[GitHub did not return file bytes/;

/** 结果的来源链接（`details.meta.source`，读文件时是 blob 地址）。 */
function sourceUrl(details: Record<string, unknown> | null): string | null {
	const meta = details && isRecord(details.meta) ? details.meta : null;
	const source = meta && isRecord(meta.source) ? meta.source : null;
	return source && str(source.type) === "url" ? str(source.value) : null;
}

/** 读到的仓库文件：文件头条（右侧是 GitHub 上的原文链接）+ 带行号、按路径高亮的代码；图片与二进制另行说明。 */
function FileResult({
	args,
	result,
	link,
}: {
	args: Record<string, unknown>;
	result: ToolResultLike;
	link: string | null;
}): ReactNode {
	const { t } = useTranslation();
	const path = str(args.path) ?? "";
	const text = resultTextOf(result);
	let body: ReactNode;
	if (resultImagesOf(result).length > 0)
		body = (
			<>
				<ResultImages result={result} />
				<div className="tv-gh-sub">{text.split("\n").join(" · ")}</div>
			</>
		);
	else if (NO_BYTES_RE.test(text) || /^\[.*binary/i.test(text))
		body = <Note tone="warn">{text.replace(/^\[|\]$/g, "")}</Note>;
	else
		body = (
			<CodeLines
				rows={text
					.replace(/\n$/, "")
					.split("\n")
					.map((line, i) => ({ kind: "ctx" as const, no: i + 1, text: line }))}
				lang={languageFromPath(path)}
				maxLines={24}
			/>
		);
	return (
		<>
			{path && (
				<FileHead path={path}>
					{link && (
						<ExtLink href={link} className="tv-gh-open">
							<Icon name="external" size={11} />
							{t("toolRender.github.openOnGithub")}
						</ExtLink>
					)}
				</FileHead>
			)}
			{body}
		</>
	);
}

function Result({ args, result, partial }: ToolRenderProps): ReactNode {
	const { t } = useTranslation();
	const op = opOf(args);
	const watch = watchOf(result ?? partial);
	if (op === "run_watch" && watch)
		return (
			<>
				{result?.isError && (
					<ErrorBlock
						text={resultTextOf(result).trim() || t("toolRender.generic.failed")}
					/>
				)}
				<WatchView watch={watch} />
			</>
		);
	if (!result) return null;
	const text = resultTextOf(result).trim();
	if (result.isError)
		return <ErrorBlock text={text || t("toolRender.generic.failed")} />;
	const details = detailsRecord(result);
	const link = sourceUrl(details);
	const checkouts =
		details && Array.isArray(details.checkouts)
			? details.checkouts.filter(isRecord)
			: [];
	let body: ReactNode;
	if (op?.startsWith("search_")) body = <SearchResult op={op} text={text} />;
	else if (op === "file_read")
		body = <FileResult args={args} result={result} link={link} />;
	else if (op === "pr_create") {
		const created = createdPr(result);
		const rest = created ? text.replace(created.url, "").trim() : text;
		body = (
			<>
				{created && (
					<div className="tv-gh-created">
						<span className="tv-gh-state tv-gh-state--open" aria-hidden="true">
							<Icon name="pullRequest" size={14} />
						</span>
						<span>{t("toolRender.github.created", { n: created.number })}</span>
						<ExtLink href={created.url} className="tv-gh-created-url">
							{created.url.replace(/^https:\/\//, "")}
						</ExtLink>
					</div>
				)}
				{rest && <SmartText text={rest} />}
			</>
		);
	} else if (!text && resultImagesOf(result).length === 0)
		body = <EmptyNote>{t("toolRender.generic.empty")}</EmptyNote>;
	else
		body = (
			<>
				<ResultImages result={result} />
				{text && <SmartText text={text} />}
			</>
		);
	return (
		<>
			{checkouts.length > 0 && <CheckoutRows checkouts={checkouts} />}
			{details &&
				op !== "file_read" &&
				op !== "pr_create" &&
				!op?.startsWith("search_") && (
					<DetailsGrid details={details} args={args} />
				)}
			{body}
			{link && op !== "file_read" && (
				<ExtLink href={link} className="tv-gh-open">
					<Icon name="external" size={11} />
					{t("toolRender.github.openOnGithub")}
				</ExtLink>
			)}
		</>
	);
}

export const githubRenderer: ToolRenderer = {
	Summary,
	Meta,
	Input,
	Result,
	livePartial: true,
};
