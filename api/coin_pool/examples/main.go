package main

import (
	"fmt"
	"log"
	"net/http"
	"os"

	"nofx/api/coin_pool"
)

func main() {
	// 创建Handler
	handler := coin_pool.NewCoinPoolHandler()

	// 创建ServeMux
	mux := http.NewServeMux()

	// 注册路由
	coin_pool.RegisterRoutes(mux, handler)

	// 添加一个简单的健康检查端点
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"status":"ok","service":"coin_pool_api"}`))
	})

	// 获取端口，默认使用8080
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	// 启动服务器
	serverAddr := fmt.Sprintf(":%s", port)
	log.Printf("Starting coin_pool API server on %s", serverAddr)
	log.Printf("Available endpoints:")
	log.Printf("  GET http://localhost:%s/health", port)
	log.Printf("  GET http://localhost:%s/api/coin-pool/ai500", port)
	log.Printf("  GET http://localhost:%s/api/coin-pool/oi-top", port)

	err := http.ListenAndServe(serverAddr, mux)
	if err != nil {
		log.Fatalf("Failed to start server: %v", err)
	}
}