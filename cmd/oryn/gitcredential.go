package main

import (
	"bufio"
	"fmt"
	"io"
	"net/url"
	"strings"

	"github.com/spf13/cobra"
)

// M24 阶段 ③：`oryn git-credential <操作>` 是 git 的凭据助手（由 oryn clone / oryn project create --push 登记）。
// 只对 oryn login 的那台服务器应答设备令牌，别的主机一律不给。

var gitCredentialCmd = &cobra.Command{
	Use:   "git-credential <get|store|erase>",
	Short: "git 凭据助手（由 oryn clone 登记）",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		cred, err := loadCredentials()
		return gitCredential(args[0], cmd.InOrStdin(), cmd.OutOrStdout(), cred, err)
	},
}

func init() { root.AddCommand(gitCredentialCmd) }

// gitCredential 实现 git 凭据助手协议（gitcredentials(7)）：get 时 protocol 与 host 都与登录的服务器相同才输出
// username / password；其它情况（含读凭据失败）什么都不输出，git 会去问别的助手或报认证失败。store / erase 与未知操作只读完输入。
func gitCredential(op string, in io.Reader, out io.Writer, cred credentials, credErr error) error {
	attrs := map[string]string{}
	sc := bufio.NewScanner(in)
	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			break
		}
		if k, v, ok := strings.Cut(line, "="); ok {
			attrs[k] = v
		}
	}
	if err := sc.Err(); err != nil {
		return err
	}
	if op != "get" || credErr != nil || cred.Token == "" || strings.ContainsAny(cred.Token, "\r\n\x00") {
		return nil
	}
	u, err := url.Parse(cred.URL)
	if err != nil || u.Host == "" {
		return nil
	}
	if !strings.EqualFold(attrs["protocol"], u.Scheme) || !strings.EqualFold(attrs["host"], u.Host) {
		return nil
	}
	_, err = fmt.Fprintf(out, "username=oryn\npassword=%s\n", cred.Token)
	return err
}
