package main

import (
	"fmt"
	"log"
	"os"
	"testing"

	// 使用相对导入路径
	"github.com/joho/godotenv"
)

// TestExchangeSupport 测试交易所支持功能
func TestExchangeSupport(t *testing.T) {
	// 加载环境变量
	err := godotenv.Load()
	if err != nil {
		log.Printf("警告: 无法加载.env文件: %v，将使用默认配置", err)
	}

	fmt.Println("=== 交易所支持测试脚本 ===")
	fmt.Println("注意: 此脚本目前仅作为配置检查使用")

	// 检查环境变量配置
	checkEnvVars()

	// 打印支持的交易所列表
	printSupportedExchanges()

	fmt.Println("\n✅ 基本配置检查完成!")
	fmt.Println("提示: 要进行完整功能测试，请使用项目的集成测试框架")
}

// checkEnvVars 检查必要的环境变量
func checkEnvVars() {
	fmt.Println("\n📋 环境变量检查:")

	exchangeVars := map[string]string{
		"binance":     "BINANCE_API_KEY",
		"aster":       "ASTER_API_KEY",
		"hyperliquid": "HYPERLIQUID_API_KEY",
	}

	for exchange, envVar := range exchangeVars {
		if os.Getenv(envVar) == "" {
			fmt.Printf("⚠️ %s 交易所的 %s 未设置\n", exchange, envVar)
		} else {
			fmt.Printf("✅ %s 交易所的 %s 已设置\n", exchange, envVar)
		}
	}
}

// printSupportedExchanges 打印支持的交易所列表
func printSupportedExchanges() {
	fmt.Println("\n🌍 支持的交易所:")
	exchanges := []string{"binance", "aster", "hyperliquid"}

	for i, exchange := range exchanges {
		fmt.Printf("  %d. %s\n", i+1, exchange)
	}

	fmt.Println("\n💡 功能说明:")
	fmt.Println("  - 每个交易所实现了Trader接口")
	fmt.Println("  - 支持GetCoinPool()和GetOITopSymbols()方法")
	fmt.Println("  - 具有错误处理和回退机制")
}
