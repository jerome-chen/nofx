package main

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"path/filepath"
)

// 配置结构体定义
type Config struct {
	Leverage *LeverageConfig `json:"leverage,omitempty"`
	Traders  []TraderConfig  `json:"traders,omitempty"`
}

type LeverageConfig struct {
	BTCEthLeverage  int            `json:"btc_eth_leverage,omitempty"`
	AltcoinLeverage int            `json:"altcoin_leverage,omitempty"`
	PairLeverage    map[string]int `json:"pair_leverage,omitempty"`
}

type TraderConfig struct {
	ID                  string            `json:"id"`
	Name                string            `json:"name"`
	BTCETHLeverage      int               `json:"btc_eth_leverage,omitempty"`
	AltcoinLeverage     int               `json:"altcoin_leverage,omitempty"`
	PairLeverage        map[string]int    `json:"pair_leverage,omitempty"`
}

type Context struct {
	BTCETHLeverage  int            `json:"btc_eth_leverage"`
	AltcoinLeverage int            `json:"altcoin_leverage"`
	PairLeverage    map[string]int `json:"pair_leverage"`
}

// 模拟validateDecision函数中的杠杆选择逻辑
func getMaxLeverage(pair string, ctx *Context) int {
	// 1. 优先使用交易对特定配置
	if leverage, ok := ctx.PairLeverage[pair]; ok {
		log.Printf("✅ %s 交易对使用特定杠杆配置: %dx", pair, leverage)
		return leverage
	}

	// 2. 否则使用山寨币杠杆
	maxLeverage := ctx.AltcoinLeverage

	// 3. 如果是BTCUSDT或ETHUSDT，使用BTC/ETH杠杆
	if pair == "BTCUSDT" || pair == "ETHUSDT" {
		maxLeverage = ctx.BTCETHLeverage
		log.Printf("✅ %s 使用BTC/ETH默认杠杆: %dx", pair, maxLeverage)
	} else {
		log.Printf("✅ %s 使用山寨币默认杠杆: %dx", pair, maxLeverage)
	}

	return maxLeverage
}

func main1() {
	// 读取配置文件
	configPath := filepath.Join(".", "config.json")
	configData, err := ioutil.ReadFile(configPath)
	if err != nil {
		log.Fatalf("❌ 读取配置文件失败: %v", err)
	}

	var config Config
	if err := json.Unmarshal(configData, &config); err != nil {
		log.Fatalf("❌ 解析配置文件失败: %v", err)
	}

	// 查找deepseek_aster_trader交易员
	var targetTrader *TraderConfig
	for i := range config.Traders {
		if config.Traders[i].ID == "deepseek_aster_trader" {
			targetTrader = &config.Traders[i]
			break
		}
	}

	if targetTrader == nil {
		log.Fatalf("❌ 未找到deepseek_aster_trader交易员")
	}

	log.Printf("\n🔍 开始验证 deepseek_aster_trader 的杠杆配置")

	// 模拟从trader_manager加载配置到Context
	ctx := &Context{}

	// 杠杆配置优先级: 交易员级别 > 根级别
	// 设置根级别的默认值
	ctx.BTCETHLeverage = config.Leverage.BTCEthLeverage
	ctx.AltcoinLeverage = config.Leverage.AltcoinLeverage
	ctx.PairLeverage = config.Leverage.PairLeverage
	log.Printf("ℹ️ 初始使用根级别的杠杆配置")

	// 检查交易员级别的覆盖
	if targetTrader.PairLeverage != nil && len(targetTrader.PairLeverage) > 0 {
		ctx.PairLeverage = targetTrader.PairLeverage
		log.Printf("ℹ️ %s 使用交易员级别的交易对杠杆配置", targetTrader.Name)
	}

	// 打印Context中的配置
	log.Printf("\n📊 Context配置:")
	log.Printf("- BTCETHLeverage: %d", ctx.BTCETHLeverage)
	log.Printf("- AltcoinLeverage: %d", ctx.AltcoinLeverage)
	log.Printf("- PairLeverage: %+v", ctx.PairLeverage)

	// 测试不同交易对的杠杆选择
	log.Printf("\n🔧 测试交易对杠杆选择逻辑:")
	pairs := []string{"ZECUSDT"}

	for _, pair := range pairs {
		maxLeverage := getMaxLeverage(pair, ctx)
		fmt.Printf("📈 %s 的最大杠杆: %d\n", pair, maxLeverage)
	}

	// 特别强调ZECUSDT的配置
	if zecLeverage, ok := ctx.PairLeverage["ZECUSDT"]; ok {
		fmt.Printf("\n🎉 验证完成: ZECUSDT 的交易对特定杠杆配置 %dx 已正确加载!\n", zecLeverage)
	} else {
		fmt.Printf("\n❌ 警告: ZECUSDT 未配置交易对特定杠杆，将使用 %dx (山寨币默认)\n", ctx.AltcoinLeverage)
	}
}