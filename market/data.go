package market

import (
	"fmt"
	"time"
)

// OpenInterestData 持仓量数据结构
type OpenInterestData struct {
	Latest float64
}

// MarketData 市场数据结构
type MarketData struct {
	Symbol          string
	CurrentPrice    float64
	PriceChange1h   float64 // 1小时价格变化
	PriceChange4h   float64 // 4小时价格变化
	CurrentMACD     float64 // 当前MACD值
	CurrentRSI7     float64 // 当前RSI(7)值
	OpenInterest    *OpenInterestData // 持仓量数据
	
	// 3分钟K线数据
	Minute3EMA20    float64
	Minute3MACD     float64
	Minute3MACD_Signal float64
	Minute3RSI      float64
	
	// 4小时K线数据
	Hour4EMA20      float64
	Hour4EMA50      float64
	Hour4ATR        float64
	Hour4RSI        float64
	
	// 交易量和流动性
	Volume          float64
	PositionValue   float64 // 持仓价值（USD）
	
	// 时间戳
	UpdateTime      time.Time
}

// FetchMarketData 获取市场数据（占位函数，实际实现需要从Binance获取数据）
func FetchMarketData(symbol string) (*MarketData, error) {
	// 这里应该实现从Binance API获取K线数据并计算技术指标的逻辑
	// 目前返回一个空的MarketData作为占位符
	return &MarketData{
		Symbol: symbol,
		CurrentPrice: 0,
		PriceChange1h: 0,
		PriceChange4h: 0,
		CurrentMACD: 0,
		CurrentRSI7: 0,
		OpenInterest: &OpenInterestData{Latest: 0},
		UpdateTime: time.Now(),
	}, nil
}

// GetMarketData 获取单个币种的市场数据（别名函数，为了兼容engine.go）
func GetMarketData(symbol string) (*MarketData, error) {
	return FetchMarketData(symbol)
}

// BatchFetchMarketData 批量获取多个币种的市场数据
func BatchFetchMarketData(symbols []string) (map[string]*MarketData, error) {
	result := make(map[string]*MarketData)
	for _, symbol := range symbols {
		md, err := FetchMarketData(symbol)
		if err != nil {
			// 忽略单个币种的错误，继续获取其他币种
			continue
		}
		result[symbol] = md
	}
	return result, nil
}

// FormatMarketData 格式化市场数据为字符串（用于AI提示）
func FormatMarketData(md *MarketData) string {
	if md == nil {
		return "数据不可用"
	}
	return fmt.Sprintf("%s: 价格=%.2f, 1h变化=%.2f%%, 4h变化=%.2f%%, MACD=%.4f, RSI7=%.2f",
		md.Symbol, md.CurrentPrice, md.PriceChange1h, md.PriceChange4h, md.CurrentMACD, md.CurrentRSI7)
}