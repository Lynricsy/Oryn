// fakeapp 是 box app（M23）端到端测试用的小 HTTP 服务：
//
//   - 监听 127.0.0.1:$PORT；当前目录有名为 refuse 的文件时启动即以退出码 1 结束（模拟起不来的 release）。
//   - GET /：当前目录有 preview.html 时回它，否则回 version.txt 的内容；响应头带 X-Frame-Options: DENY。
//   - GET /env?k=NAME：回该环境变量。
//   - GET /data：回 $ORYN_DEPLOYMENT_DATA_DIRECTORY 是否存在（yes / no）。
//   - GET /pid：回自己的 PID。
package main

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
)

func main() {
	if _, err := os.Stat("refuse"); err == nil {
		fmt.Fprintln(os.Stderr, "fakeapp: refuse file present, exiting")
		os.Exit(1)
	}
	port := os.Getenv("PORT")
	if port == "" {
		log.Fatal("fakeapp: PORT is not set")
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-Frame-Options", "DENY")
		if b, err := os.ReadFile("preview.html"); err == nil {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write(b)
			return
		}
		b, err := os.ReadFile("version.txt")
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		_, _ = w.Write(b)
	})
	mux.HandleFunc("GET /env", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(os.Getenv(r.URL.Query().Get("k"))))
	})
	mux.HandleFunc("GET /data", func(w http.ResponseWriter, _ *http.Request) {
		ans := "no"
		if st, err := os.Stat(os.Getenv("ORYN_DEPLOYMENT_DATA_DIRECTORY")); err == nil && st.IsDir() {
			ans = "yes"
		}
		_, _ = w.Write([]byte(ans))
	})
	mux.HandleFunc("GET /pid", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(strconv.Itoa(os.Getpid())))
	})
	log.Fatal(http.ListenAndServe("127.0.0.1:"+port, mux))
}
