package main

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/gitident"
)

const gitIdentityPath = "/api/v1/settings/git-identity"

var gitIdentityCmd = &cobra.Command{
	Use:   "git-identity",
	Short: "orb 内 git 提交身份的全局默认值（项目可单独覆盖）",
}

var gitIdentityGetJSON bool

var gitIdentityGetCmd = &cobra.Command{
	Use:   "get",
	Short: "查看全局默认身份",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		c, err := newClient()
		if err != nil {
			return err
		}
		var s apigen.GitIdentitySettings
		if err := c.do(cmd.Context(), http.MethodGet, gitIdentityPath, nil, &s); err != nil {
			return err
		}
		if gitIdentityGetJSON {
			return printJSON(s)
		}
		if s.Identity == nil {
			fmt.Fprintln(os.Stderr, "未设置")
			return nil
		}
		fmt.Printf("%s <%s>\n", s.Identity.Name, s.Identity.Email)
		return nil
	},
}

var gitIdentitySetOpts struct{ name, email string }

var gitIdentitySetCmd = &cobra.Command{
	Use:   "set --name <名字> --email <邮箱>",
	Short: "设置全局默认身份（下次 setup / 唤醒时写入 orb）",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		id, err := gitIdentityFlags(gitIdentitySetOpts.name, gitIdentitySetOpts.email)
		if err != nil {
			return err
		}
		if id == nil {
			return errors.New("需要 --name 与 --email")
		}
		c, err := newClient()
		if err != nil {
			return err
		}
		var s apigen.GitIdentitySettings
		if err := c.do(cmd.Context(), http.MethodPut, gitIdentityPath, id, &s); err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "已设置 %s <%s>\n", id.Name, id.Email)
		return nil
	},
}

var gitIdentityClearCmd = &cobra.Command{
	Use:   "clear",
	Short: "清除全局默认身份（已有 orb 的 git 配置保持不变）",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		c, err := newClient()
		if err != nil {
			return err
		}
		if err := c.do(cmd.Context(), http.MethodDelete, gitIdentityPath, nil, nil); err != nil {
			return err
		}
		fmt.Fprintln(os.Stderr, "已清除")
		return nil
	},
}

// gitIdentityFlags 校验成对的 --git-name / --git-email（或 --name / --email）：都缺省返回 nil，只给一个报错。
func gitIdentityFlags(name, email string) (*apigen.GitIdentity, error) {
	name, email = strings.TrimSpace(name), strings.TrimSpace(email)
	if name == "" && email == "" {
		return nil, nil
	}
	if name == "" || email == "" {
		return nil, errors.New("git 名字与邮箱须同时给出")
	}
	if err := gitident.Validate(name, email); err != nil {
		return nil, err
	}
	return &apigen.GitIdentity{Name: name, Email: email}, nil
}

func init() {
	gitIdentityGetCmd.Flags().BoolVar(&gitIdentityGetJSON, "json", false, "输出 JSON")
	f := gitIdentitySetCmd.Flags()
	f.StringVar(&gitIdentitySetOpts.name, "name", "", "git user.name")
	f.StringVar(&gitIdentitySetOpts.email, "email", "", "git user.email")
	gitIdentityCmd.AddCommand(gitIdentityGetCmd, gitIdentitySetCmd, gitIdentityClearCmd)
	root.AddCommand(gitIdentityCmd)
}
