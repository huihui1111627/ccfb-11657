// Command bfdsim 启动 BFD 缆车控制网仿真的 Web 仪表盘。
package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"

	"bfdsim/internal/web"
)

func main() {
	addr := flag.String("addr", ":8080", "HTTP 监听地址")
	flag.Parse()
	log.Printf("BFD 仿真仪表盘已启动: http://localhost%s", *addr)
	if err := http.ListenAndServe(*addr, web.Handler()); err != nil {
		log.Fatal(fmt.Errorf("服务退出: %w", err))
	}
}
