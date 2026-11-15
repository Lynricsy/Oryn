package agentcfg

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
)

// DeliveryBudget 是全局 omp-home 与一个项目层合起来、按下发清单（EntrySize）计的上限。两层随同一条
// setup.run / resume.run / omphome.apply 整份下发，单行 ctl 消息上限 96 MiB（link.MaxCtlLineBytes）；余下的
// 8 MiB 留给 env 密钥、git 主机、脚本、models.yml 渲染后的增量与 JSON-RPC 信封。各层自己的大小配额（MaxTotalSize、
// ProjectMaxTotalSize）照旧，写入时另按本预算检查两层合计，存得进来的配置就一定发得出去。
const DeliveryBudget = 88 << 20

// entryOverhead 是下发清单里一个文件除正文与路径外的 JSON 开销上界（`{"contentB64":"","mode":511,"path":""},`）。
const entryOverhead = 64

// deliveryMu 串行化所有配置根（全局与各项目）的写入：预算跨层计算，检查与落盘之间另一层不能变大。
// 锁序：deliveryMu → Home.mu；ProjectHomes.mu 不与它嵌套。写入少，进程级一把锁足够。
var deliveryMu sync.Mutex

// EntrySize 返回一个文件在下发清单里占用字节数的上界：base64 正文 + JSON 转义后的路径 + 字段开销。
// 路径里需要转义或非 ASCII 的字节按最坏的 6 字节（\u00XX）计。
func EntrySize(path string, size int64) int64 {
	n := (size+2)/3*4 + entryOverhead
	for i := range len(path) {
		switch c := path[i]; {
		case c < 0x20, c >= 0x80, c == '"', c == '\\', c == '<', c == '>', c == '&':
			n += 6
		default:
			n++
		}
	}
	return n
}

// layerSize 返回 dir 下全部普通文件按 EntrySize 计的合计（跳过相对路径 skip）；目录不存在为 0。
// 读的是别的配置根时不持它的锁（写入都在 deliveryMu 下），与并发删除竞争而消失的条目不计。
func layerSize(dir, skip string) (int64, error) {
	var total int64
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		if !d.Type().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if rel == skip {
			return nil
		}
		info, err := d.Info()
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		total += EntrySize(rel, info.Size())
		return nil
	})
	return total, err
}

// checkDelivery 检查本层写入后（size 为 layerSize 口径）与另一层合计仍在 DeliveryBudget 内：项目层加全局层；
// 全局层加最大的项目层（已删项目残留的目录也计入，偏保守）。持 deliveryMu 调用。
func (h *Home) checkDelivery(size int64) error {
	if h.project {
		global, err := layerSize(filepath.Join(filepath.Dir(h.parent), HomeDirName), "")
		if err != nil {
			return err
		}
		if total := size + global; total > DeliveryBudget {
			return fmt.Errorf("%w（全局与本项目的 agent 配置按下发编码合计 %.1f MiB，超过单次下发上限 %d MiB；请精简全局或本项目的配置）",
				ErrTooLarge, mib(total), DeliveryBudget>>20)
		}
		return nil
	}
	root := filepath.Join(h.parent, ProjectsDirName)
	entries, err := os.ReadDir(root)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	var largest int64
	largestID := ""
	for _, e := range entries {
		// 只看项目目录：`<id>.import-*`、`<id>.old-*` 临时目录含 `.`，不匹配 projectIDRe
		if !e.IsDir() || !projectIDRe.MatchString(e.Name()) {
			continue
		}
		n, err := layerSize(filepath.Join(root, e.Name()), "")
		if err != nil {
			return err
		}
		if n > largest {
			largest, largestID = n, e.Name()
		}
	}
	if total := size + largest; total > DeliveryBudget {
		if largestID == "" {
			return fmt.Errorf("%w（全局 agent 配置按下发编码 %.1f MiB，超过单次下发上限 %d MiB）", ErrTooLarge, mib(total), DeliveryBudget>>20)
		}
		return fmt.Errorf("%w（全局与项目 %s 的 agent 配置按下发编码合计 %.1f MiB，超过单次下发上限 %d MiB；请精简全局或该项目的配置）",
			ErrTooLarge, largestID, mib(total), DeliveryBudget>>20)
	}
	return nil
}

func mib(n int64) float64 { return float64(n) / (1 << 20) }
