package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"

	"github.com/Lynricsy/Oryn/internal/auth"
	"github.com/Lynricsy/Oryn/internal/config"
	"github.com/Lynricsy/Oryn/internal/store"
	"github.com/Lynricsy/Oryn/internal/store/db"
	"github.com/Lynricsy/Oryn/internal/threads"
)

func init() {
	subcommands["setup-token"] = cmdSetupToken
	subcommands["admin"] = cmdAdmin
}

// cmdSetupToken 重新生成初始化令牌并打印 setup 链接（已完成初始化时报错）。
func cmdSetupToken(ctx context.Context, cfg config.Orynd, _ []string) error {
	st, err := store.Open(ctx, cfg.DataDir)
	if err != nil {
		return err
	}
	defer st.Close()
	u, created, err := auth.EnsureSetupToken(ctx, st, cfg.DataDir, cfg.PublicURL)
	if err != nil {
		return err
	}
	if !created {
		return auth.ErrAlreadyInitialized
	}
	fmt.Println(u)
	return nil
}

// cmdAdmin 是本机管理命令：直接打开同一 SQLite 库（WAL + busy_timeout 允许与运行中的 orynd 并存）。
func cmdAdmin(ctx context.Context, cfg config.Orynd, args []string) error {
	if len(args) == 0 {
		return errors.New("用法：orynd --config <file> admin create-device --name <名称> --kind cli|desktop|mobile [--platform <平台>] | admin create-web-session [--user-agent <UA>] | admin rebuild-stats [--thread <id>]")
	}
	switch args[0] {
	case "rebuild-stats":
		return adminRebuildStats(ctx, cfg, args[1:], os.Stdout)
	case "create-device":
		fs := flag.NewFlagSet("admin create-device", flag.ContinueOnError)
		name := fs.String("name", "", "设备名（必填）")
		kind := fs.String("kind", "cli", "设备类型：cli | desktop | mobile")
		platform := fs.String("platform", "", "平台，如 linux-x64")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if *name == "" {
			return errors.New("--name 必填")
		}
		st, err := store.Open(ctx, cfg.DataDir)
		if err != nil {
			return err
		}
		defer st.Close()
		_, tok, err := auth.CreateDevice(ctx, st, *name, *kind, *platform)
		if err != nil {
			return err
		}
		// stdout 只输出令牌，便于 TOKEN=$(orynd … admin create-device …)
		_, err = fmt.Fprintln(os.Stdout, tok)
		return err
	case "create-web-session":
		fs := flag.NewFlagSet("admin create-web-session", flag.ContinueOnError)
		ua := fs.String("user-agent", "orynd admin", "记录在会话上的 User-Agent")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		st, err := store.Open(ctx, cfg.DataDir)
		if err != nil {
			return err
		}
		defer st.Close()
		tok, err := auth.CreateWebSession(ctx, st, *ua)
		if err != nil {
			return err
		}
		// stdout 只输出 Cookie（__Host-oryn_session）的值，便于 SESSION=$(orynd … admin create-web-session)
		_, err = fmt.Fprintln(os.Stdout, tok)
		return err
	default:
		return fmt.Errorf("未知 admin 子命令 %q", args[0])
	}
}

// adminRebuildStats 实现 `admin rebuild-stats [--thread <id>]`：按当前条目重算统计事实行。给了 --thread 只重算该线程；
// 否则把 facts_version 置 0、删除回填进度键后全量回填（purge：先清空全部活线程的事实行）。每个事务很短，
// 可在 orynd 运行时执行（靠 busy_timeout 排队）。stdout 打印 `rebuilt <n> threads`。
func adminRebuildStats(ctx context.Context, cfg config.Orynd, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("admin rebuild-stats", flag.ContinueOnError)
	thread := fs.String("thread", "", "只重算这个线程")
	if err := fs.Parse(args); err != nil {
		return err
	}
	st, err := store.Open(ctx, cfg.DataDir)
	if err != nil {
		return err
	}
	defer st.Close()
	if *thread != "" {
		if _, err := st.Q.GetThread(ctx, *thread); err != nil {
			return fmt.Errorf("线程 %s: %w", *thread, err)
		}
		if err := threads.RebuildThreadFacts(ctx, st, *thread, nil); err != nil {
			return err
		}
		_, err = fmt.Fprintln(out, "rebuilt 1 threads")
		return err
	}
	rows, err := st.Q.ListThreads(ctx, db.ListThreadsParams{})
	if err != nil {
		return err
	}
	err = st.Tx(ctx, func(q *db.Queries) error {
		if err := q.SetStatsState(ctx, db.SetStatsStateParams{Key: "facts_version", Value: "0"}); err != nil {
			return err
		}
		for _, k := range []string{"backfill_cursor", "backfill_done", "backfill_total"} {
			if err := q.DeleteStatsState(ctx, k); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	if err := threads.BackfillFacts(ctx, st, nil, true); err != nil {
		return err
	}
	_, err = fmt.Fprintln(out, "rebuilt "+strconv.Itoa(len(rows))+" threads")
	return err
}
