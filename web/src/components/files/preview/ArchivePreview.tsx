/**
 * 归档预览：列出 zip / jar / tar / tgz 等归档的条目（只读目录，不解压内容）。
 * 平铺列表可按路径或大小排序，条目多时用 @tanstack/react-virtual 虚拟滚动；顶部汇总条目数与解压后总大小。
 */
import { useVirtualizer } from "@tanstack/react-virtual";
import { type ReactNode, useEffect, useMemo, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import {
	type ArchiveEntry,
	type ArchiveListing,
	listArchive,
	MAX_ARCHIVE_ENTRIES,
} from "../../../lib/archive-list";
import { formatBytes, formatDateTime } from "../../../lib/format";
import { Icon } from "../../ui/icons";
import { ErrorText, Spinner } from "../../ui/primitives";
import type { BlobPreviewProps } from "./types";

const ROW_PX = 28;

type SortKey = "path" | "size";

type State =
	| { status: "loading" }
	| { status: "error"; error: unknown }
	| { status: "ready"; listing: ArchiveListing };

const collator = new Intl.Collator(undefined, { numeric: true });

export default function ArchivePreview({
	blob,
	name,
}: BlobPreviewProps): ReactNode {
	const { t } = useTranslation();
	const [state, setState] = useState<State>({ status: "loading" });
	const [sort, setSort] = useState<{ key: SortKey; desc: boolean }>({
		key: "path",
		desc: false,
	});
	const scrollRef = useRef<HTMLDivElement>(null);

	useEffect(() => {
		let cancelled = false;
		setState({ status: "loading" });
		blob
			.arrayBuffer()
			.then((buf) => listArchive(name, new Uint8Array(buf)))
			.then(
				(listing) => {
					if (!cancelled) setState({ status: "ready", listing });
				},
				(error: unknown) => {
					if (!cancelled) setState({ status: "error", error });
				},
			);
		return () => {
			cancelled = true;
		};
	}, [blob, name]);

	const listing = state.status === "ready" ? state.listing : null;

	const sorted = useMemo(() => {
		if (!listing) return [];
		const list = [...listing.entries];
		const sign = sort.desc ? -1 : 1;
		list.sort((a, b) =>
			sort.key === "size"
				? sign * (a.size - b.size) || collator.compare(a.path, b.path)
				: sign * collator.compare(a.path, b.path),
		);
		return list;
	}, [listing, sort]);

	const summary = useMemo(() => {
		let files = 0;
		let dirs = 0;
		let bytes = 0;
		for (const e of listing?.entries ?? []) {
			if (e.dir) dirs++;
			else {
				files++;
				bytes += e.size;
			}
		}
		return { files, dirs, bytes };
	}, [listing]);

	const virtualizer = useVirtualizer({
		count: sorted.length,
		getScrollElement: () => scrollRef.current,
		estimateSize: () => ROW_PX,
		overscan: 12,
	});

	if (state.status === "loading") {
		return (
			<div className="flex h-full items-center justify-center text-faint">
				<Spinner />
			</div>
		);
	}
	if (state.status === "error") {
		return (
			<div className="flex flex-col gap-1.5 p-3">
				<p className="text-muted text-sm">
					{t("files.preview.archive.failed")}
				</p>
				<ErrorText error={state.error} />
			</div>
		);
	}

	const toggleSort = (key: SortKey) =>
		setSort((s) =>
			s.key === key ? { key, desc: !s.desc } : { key, desc: false },
		);

	const sortHeader = (key: SortKey, label: string, className: string) => (
		<button
			type="button"
			className={`flex min-w-0 items-center gap-1 hover:text-fg ${className}`}
			onClick={() => toggleSort(key)}
		>
			<span className="truncate">{label}</span>
			{sort.key === key && (
				<Icon name={sort.desc ? "chevronDown" : "chevronUp"} size={11} />
			)}
		</button>
	);

	return (
		<div className="@container flex h-full min-h-0 flex-col">
			<div className="flex shrink-0 items-center gap-1.5 border-border border-b px-3 py-1.5 text-muted text-xs">
				<Icon name="archive" size={13} className="shrink-0 text-faint" />
				<span className="truncate">
					{t("files.preview.archive.summary", {
						files: summary.files,
						dirs: summary.dirs,
						size: formatBytes(summary.bytes),
					})}
				</span>
			</div>
			{state.listing.truncated && (
				<p className="flex shrink-0 items-center gap-1.5 bg-warn/8 px-3 py-1.5 text-warn text-xs">
					<Icon name="info" size={13} className="shrink-0" />
					{t("files.preview.archive.truncated", { max: MAX_ARCHIVE_ENTRIES })}
				</p>
			)}
			{sorted.length === 0 ? (
				<div className="flex flex-1 items-center justify-center p-6 text-faint text-sm">
					{t("files.preview.archive.empty")}
				</div>
			) : (
				<>
					<div className="grid shrink-0 grid-cols-[minmax(0,1fr)_5.5rem] gap-3 border-border border-b px-3 py-1 text-faint text-xs @lg:grid-cols-[minmax(0,1fr)_5.5rem_9.5rem]">
						{sortHeader("path", t("files.preview.archive.path"), "")}
						{sortHeader("size", t("files.preview.archive.size"), "justify-end")}
						<span className="hidden truncate text-right @lg:block">
							{t("files.preview.archive.modified")}
						</span>
					</div>
					<div ref={scrollRef} className="min-h-0 flex-1 overflow-auto">
						<div
							className="relative w-full"
							style={{ height: virtualizer.getTotalSize() }}
						>
							{virtualizer.getVirtualItems().map((v) => {
								const e = sorted[v.index];
								return e ? <Row key={v.key} entry={e} top={v.start} /> : null;
							})}
						</div>
					</div>
				</>
			)}
		</div>
	);
}

function Row({ entry, top }: { entry: ArchiveEntry; top: number }) {
	return (
		<div
			className="absolute inset-x-0 grid grid-cols-[minmax(0,1fr)_5.5rem] items-center gap-3 px-3 text-xs hover:bg-hover @lg:grid-cols-[minmax(0,1fr)_5.5rem_9.5rem]"
			style={{ height: ROW_PX, transform: `translateY(${top}px)` }}
		>
			<span className="flex min-w-0 items-center gap-1.5" title={entry.path}>
				<Icon
					name={entry.dir ? "folder" : "file"}
					size={13}
					className="shrink-0 text-faint"
				/>
				<span className="truncate font-mono text-fg">{entry.path}</span>
			</span>
			<span className="text-right text-muted tabular-nums">
				{entry.dir ? "" : formatBytes(entry.size)}
			</span>
			<span className="hidden truncate text-right text-faint tabular-nums @lg:block">
				{entry.mtime !== undefined ? formatDateTime(entry.mtime) : ""}
			</span>
		</div>
	);
}
