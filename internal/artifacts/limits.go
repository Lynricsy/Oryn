package artifacts

import "time"

// 上限（与 Kettu 同值）。字符数一律按 Unicode 码点计。
const (
	// MaxFileBytes 是单个文件的上限。
	MaxFileBytes = 20 << 20
	// MaxInlineBytes 是 agent 工具里内联 content 的上限（更大的文件用 source）。
	MaxInlineBytes = 5 << 20
	// MaxVersionBytes 是单个版本全部文件的总字节上限。
	MaxVersionBytes = 64 << 20
	// MaxVersionFiles 是单个版本的文件数上限。
	MaxVersionFiles = 256
	// MaxTitleRunes 是标题上限。
	MaxTitleRunes = 120
	// MaxNoteRunes 是版本备注上限。
	MaxNoteRunes = 200
	// MaxPathBytes 是文件路径上限。
	MaxPathBytes = 512
	// ReadDefaultChars / ReadMaxChars 是 read_artifact 每页的缺省与最大字符数。
	ReadDefaultChars = 100_000
	ReadMaxChars     = 1_000_000
	// MaxEdits 是一次 update_artifact 的文本替换条数上限。
	MaxEdits = 50
	// WritesPerMinute 是每个调用线程每分钟成功写入的上限。
	WritesPerMinute = 60
	// KeepDeleted 是「最近删除」的保留时长。
	KeepDeleted = 30 * 24 * time.Hour
	// PurgeEvery 是清理周期。
	PurgeEvery = 24 * time.Hour
	// MaxCommentRunes 是单条评论的上限。
	MaxCommentRunes = 4000
	// MaxThreadsPerArtifact / MaxCommentsPerThread 是评论串与每串评论数的上限。
	MaxThreadsPerArtifact = 1000
	MaxCommentsPerThread  = 200
	// MaxEditTextRunes 是元素直接编辑的文字上限。
	MaxEditTextRunes = 10_000
	// MaxParams 是一个页面能声明的调节参数个数上限。
	MaxParams = 24
)

// orphanGrace：没有任何行引用的内容文件与临时文件只在早于该时长时删除，避开「先落内容再插行」的窗口。
const orphanGrace = time.Hour
