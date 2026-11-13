package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"

	"github.com/Lynricsy/Oryn/internal/auth"
	"github.com/Lynricsy/Oryn/internal/config"
	"github.com/Lynricsy/Oryn/internal/store"
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
		return errors.New("用法：orynd --config <file> admin create-device --name <名称> --kind cli|desktop|mobile [--platform <平台>] | admin create-web-session [--user-agent <UA>]")
	}
	switch args[0] {
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
