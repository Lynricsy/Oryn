// Command fakegithub 是验收脚本用的假 GitHub（M14 步骤 9.1）：REST / GraphQL / OAuth / App 端点、
// git smart-HTTP（git http-backend），以及 /__fake/ 控制端点。同一地址同时作为 API 与网页基址。
package main

import (
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/Lynricsy/Oryn/internal/github/githubtest"
)

func main() {
	listen := flag.String("listen", "127.0.0.1:7630", "监听地址")
	gitRoot := flag.String("git-root", "", "裸仓库根目录（空 = 不提供 git smart-HTTP）")
	flag.Parse()

	ln, err := net.Listen("tcp", *listen)
	if err != nil {
		log.Fatal(err)
	}
	s := githubtest.NewHandlerOnly("http://" + ln.Addr().String())
	if *gitRoot != "" {
		abs, err := filepath.Abs(*gitRoot)
		if err != nil {
			log.Fatal(err)
		}
		if err := os.MkdirAll(abs, 0o755); err != nil {
			log.Fatal(err)
		}
		s.GitRoot = abs
	}
	srv := &http.Server{Handler: s.Handler(), ReadHeaderTimeout: 30 * time.Second}
	fmt.Printf("fakegithub listening on %s\n", ln.Addr())
	if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}
