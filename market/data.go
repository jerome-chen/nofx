package market

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// getCurrentPriceFromWebSocket 获取WebSocket实时价格
func getCurrentPriceFromWebSocket(monitor *WSMonitor, symbol string) (float64, error) {
	// 从WSMonitor获取最新的3分钟K线收盘价
	klines, err := monitor.GetCurrentKlines(symbol, "3m")
	if err != nil {
		return 0, fmt.Errorf("获取WebSocket价格失败: %v", err)
	}
	if len(klines) == 0 {
		return 0, fmt.Errorf("无K线数据")
	}
	
	// 获取最新K线的收盘价
	latestPrice := klines[len(klines)-1].Close
	log.Printf("🔍 [DEBUG] %s WebSocket价格: %.6f (K线数量: %d, 最新K线时间: %d)", 
		symbol, latestPrice, len(klines), klines[len(klines)-1].CloseTime)
	return latestPrice, nil
}

// Get 获取指定代币的市场数据
func Get(monitor *WSMonitor, symbol string) (*Data, error) {
	var klines3m, klines15m, klines1h, klines4h []Kline
	var err error
	// 标准化symbol
	symbol = Normalize(symbol)
	// 获取3分钟K线数据 (最近10个)
	klines3m, err = monitor.GetCurrentKlines(symbol, "3m") // 多获取一些用于计算
	if err != nil {
		return nil, fmt.Errorf("获取3分钟K线失败: %v", err)
	}

	// 获取15分钟K线数据
	klines15m, err = monitor.GetCurrentKlines(symbol, "15m")
	if err != nil {
		return nil, fmt.Errorf("获取15分钟K线失败: %v", err)
	}

	// 获取1小时K线数据
	klines1h, err = monitor.GetCurrentKlines(symbol, "1h")
	if err != nil {
		return nil, fmt.Errorf("获取1小时K线失败: %v", err)
	}

	// 获取4小时K线数据 (最近10个)
	klines4h, err = monitor.GetCurrentKlines(symbol, "4h") // 多获取用于计算指标
	if err != nil {
		return nil, fmt.Errorf("获取4小时K线失败: %v", err)
	}

	// 优先使用WebSocket实时价格，失败时降级到K线收盘价
	currentPrice, err := getCurrentPriceFromWebSocket(monitor, symbol)
	if err != nil {
		// 降级到3分钟K线收盘价
		currentPrice = klines3m[len(klines3m)-1].Close
		log.Printf("⚠️ [DEBUG] %s 使用K线收盘价: %.6f (原因: %v)", symbol, currentPrice, err)
	} else {
		log.Printf("✅ [DEBUG] %s 使用WebSocket价格: %.6f", symbol, currentPrice)
	}

	// 验证价格数据的新鲜度
	currentTime := time.Now().Unix() * 1000 // 转换为毫秒
	lastKlineTime := klines3m[len(klines3m)-1].CloseTime
	priceAgeMinutes := (currentTime - lastKlineTime) / (60 * 1000)
	log.Printf("🔍 [DEBUG] %s 价格年龄: %d分钟 (当前时间: %d, K线时间: %d)", 
		symbol, priceAgeMinutes, currentTime, lastKlineTime)

	// 计算当前指标 (基于3分钟最新数据)
	currentEMA20 := calculateEMA(klines3m, 20)
	currentMACD := calculateMACD(klines3m)
	currentRSI7 := calculateRSI(klines3m, 7)

	// 调试日志：输出当前价格和数据源
	log.Printf("🔍 [DEBUG] %s 最终使用价格: %.6f (数据源: WebSocket, 价格年龄: %d分钟)", 
		symbol, currentPrice, priceAgeMinutes)

	// 计算价格变化百分比
	// 1小时价格变化 - 优先使用1小时K线数据
	priceChange1h := 0.0
	if len(klines1h) >= 2 {
		price1hAgo := klines1h[len(klines1h)-2].Close
		if price1hAgo > 0 {
			priceChange1h = ((currentPrice - price1hAgo) / price1hAgo) * 100
		}
	} else if len(klines3m) >= 21 { // 备用：使用3分钟K线数据
		price1hAgo := klines3m[len(klines3m)-21].Close
		if price1hAgo > 0 {
			priceChange1h = ((currentPrice - price1hAgo) / price1hAgo) * 100
		}
	}

	// 4小时价格变化 = 1个4小时K线前的价格
	priceChange4h := 0.0
	if len(klines4h) >= 2 {
		price4hAgo := klines4h[len(klines4h)-2].Close
		if price4hAgo > 0 {
			priceChange4h = ((currentPrice - price4hAgo) / price4hAgo) * 100
		}
	}

	// 获取OI数据
	oiData, err := getOpenInterestData(symbol)
	if err != nil {
		// OI失败不影响整体,使用默认值
		oiData = &OIData{Latest: 0, Average: 0}
	}

	// 获取Funding Rate
	fundingRate, _ := getFundingRate(symbol)

	// 计算日内系列数据
	intradayData := calculateIntradaySeries(klines3m)

	// 计算长期数据
	longerTermData := calculateLongerTermData(klines4h)

	// 计算15分钟和1小时周期的指标
	currentEMA20_15m := calculateEMA(klines15m, 20)
	currentMACD_15m := calculateMACD(klines15m)
	currentRSI7_15m := calculateRSI(klines15m, 7)
	currentEMA20_1h := calculateEMA(klines1h, 20)
	currentMACD_1h := calculateMACD(klines1h)
	currentRSI7_1h := calculateRSI(klines1h, 7)

	return &Data{
		Symbol:            symbol,
		CurrentPrice:      currentPrice,
		PriceChange1h:     priceChange1h,
		PriceChange4h:     priceChange4h,
		CurrentEMA20:      currentEMA20,
		CurrentMACD:       currentMACD,
		CurrentRSI7:       currentRSI7,
		CurrentEMA20_15m:  currentEMA20_15m,
		CurrentMACD_15m:   currentMACD_15m,
		CurrentRSI7_15m:   currentRSI7_15m,
		CurrentEMA20_1h:   currentEMA20_1h,
		CurrentMACD_1h:    currentMACD_1h,
		CurrentRSI7_1h:    currentRSI7_1h,
		OpenInterest:      oiData,
		FundingRate:       fundingRate,
		IntradaySeries:    intradayData,
		LongerTermContext: longerTermData,
	}, nil
}

// calculateEMA 计算EMA
func calculateEMA(klines []Kline, period int) float64 {
	if len(klines) < period {
		return 0
	}

	// 计算SMA作为初始EMA
	sum := 0.0
	for i := 0; i < period; i++ {
		sum += klines[i].Close
	}
	ema := sum / float64(period)

	// 计算EMA
	multiplier := 2.0 / float64(period+1)
	for i := period; i < len(klines); i++ {
		ema = (klines[i].Close-ema)*multiplier + ema
	}

	return ema
}

// calculateMACD 计算MACD
func calculateMACD(klines []Kline) float64 {
	if len(klines) < 26 {
		return 0
	}

	// 计算12期和26期EMA
	ema12 := calculateEMA(klines, 12)
	ema26 := calculateEMA(klines, 26)

	// MACD = EMA12 - EMA26
	return ema12 - ema26
}

// calculateRSI 计算RSI
func calculateRSI(klines []Kline, period int) float64 {
	if len(klines) <= period {
		return 0
	}

	gains := 0.0
	losses := 0.0

	// 计算初始平均涨跌幅
	for i := 1; i <= period; i++ {
		change := klines[i].Close - klines[i-1].Close
		if change > 0 {
			gains += change
		} else {
			losses += -change
		}
	}

	avgGain := gains / float64(period)
	avgLoss := losses / float64(period)

	// 使用Wilder平滑方法计算后续RSI
	for i := period + 1; i < len(klines); i++ {
		change := klines[i].Close - klines[i-1].Close
		if change > 0 {
			avgGain = (avgGain*float64(period-1) + change) / float64(period)
			avgLoss = (avgLoss * float64(period-1)) / float64(period)
		} else {
			avgGain = (avgGain * float64(period-1)) / float64(period)
			avgLoss = (avgLoss*float64(period-1) + (-change)) / float64(period)
		}
	}

	if avgLoss == 0 {
		return 100
	}

	rs := avgGain / avgLoss
	rsi := 100 - (100 / (1 + rs))

	return rsi
}

// calculateATR 计算ATR
func calculateATR(klines []Kline, period int) float64 {
	if len(klines) <= period {
		return 0
	}

	trs := make([]float64, len(klines))
	for i := 1; i < len(klines); i++ {
		high := klines[i].High
		low := klines[i].Low
		prevClose := klines[i-1].Close

		tr1 := high - low
		tr2 := math.Abs(high - prevClose)
		tr3 := math.Abs(low - prevClose)

		trs[i] = math.Max(tr1, math.Max(tr2, tr3))
	}

	// 计算初始ATR
	sum := 0.0
	for i := 1; i <= period; i++ {
		sum += trs[i]
	}
	atr := sum / float64(period)

	// Wilder平滑
	for i := period + 1; i < len(klines); i++ {
		atr = (atr*float64(period-1) + trs[i]) / float64(period)
	}

	return atr
}

// calculateIntradaySeries 计算日内系列数据
func calculateIntradaySeries(klines []Kline) *IntradayData {
	data := &IntradayData{
		MidPrices:   make([]float64, 0, 10),
		EMA20Values: make([]float64, 0, 10),
		MACDValues:  make([]float64, 0, 10),
		RSI7Values:  make([]float64, 0, 10),
		RSI14Values: make([]float64, 0, 10),
	}

	// 获取最近10个数据点
	start := len(klines) - 10
	if start < 0 {
		start = 0
	}

	for i := start; i < len(klines); i++ {
		data.MidPrices = append(data.MidPrices, klines[i].Close)

		// 计算每个点的EMA20
		if i >= 19 {
			ema20 := calculateEMA(klines[:i+1], 20)
			data.EMA20Values = append(data.EMA20Values, ema20)
		}

		// 计算每个点的MACD
		if i >= 25 {
			macd := calculateMACD(klines[:i+1])
			data.MACDValues = append(data.MACDValues, macd)
		}

		// 计算每个点的RSI
		if i >= 7 {
			rsi7 := calculateRSI(klines[:i+1], 7)
			data.RSI7Values = append(data.RSI7Values, rsi7)
		}
		if i >= 14 {
			rsi14 := calculateRSI(klines[:i+1], 14)
			data.RSI14Values = append(data.RSI14Values, rsi14)
		}
	}

	return data
}

// calculateLongerTermData 计算长期数据
func calculateLongerTermData(klines []Kline) *LongerTermData {
	data := &LongerTermData{
		MACDValues:  make([]float64, 0, 10),
		RSI14Values: make([]float64, 0, 10),
	}

	// 计算EMA
	data.EMA20 = calculateEMA(klines, 20)
	data.EMA50 = calculateEMA(klines, 50)

	// 计算ATR
	data.ATR3 = calculateATR(klines, 3)
	data.ATR14 = calculateATR(klines, 14)

	// 计算成交量
	if len(klines) > 0 {
		data.CurrentVolume = klines[len(klines)-1].Volume
		// 计算平均成交量
		sum := 0.0
		for _, k := range klines {
			sum += k.Volume
		}
		data.AverageVolume = sum / float64(len(klines))
	}

	// 计算MACD和RSI序列
	start := len(klines) - 10
	if start < 0 {
		start = 0
	}

	for i := start; i < len(klines); i++ {
		if i >= 25 {
			macd := calculateMACD(klines[:i+1])
			data.MACDValues = append(data.MACDValues, macd)
		}
		if i >= 14 {
			rsi14 := calculateRSI(klines[:i+1], 14)
			data.RSI14Values = append(data.RSI14Values, rsi14)
		}
	}

	return data
}

// getOpenInterestData 获取OI数据
func getOpenInterestData(symbol string) (*OIData, error) {
	url := fmt.Sprintf("https://fapi.binance.com/fapi/v1/openInterest?symbol=%s", symbol)

	resp, err := http.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var result struct {
		OpenInterest string `json:"openInterest"`
		Symbol       string `json:"symbol"`
		Time         int64  `json:"time"`
	}

	if err := json.Unmarshal(body, &result); err != nil {
		return nil, err
	}

	oi, _ := strconv.ParseFloat(result.OpenInterest, 64)

	return &OIData{
		Latest:  oi,
		Average: oi * 0.999, // 近似平均值
	}, nil
}

// getFundingRate 获取资金费率
func getFundingRate(symbol string) (float64, error) {
	url := fmt.Sprintf("https://fapi.binance.com/fapi/v1/premiumIndex?symbol=%s", symbol)

	resp, err := http.Get(url)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, err
	}

	var result struct {
		Symbol          string `json:"symbol"`
		MarkPrice       string `json:"markPrice"`
		IndexPrice      string `json:"indexPrice"`
		LastFundingRate string `json:"lastFundingRate"`
		NextFundingTime int64  `json:"nextFundingTime"`
		InterestRate    string `json:"interestRate"`
		Time            int64  `json:"time"`
	}

	if err := json.Unmarshal(body, &result); err != nil {
		return 0, err
	}

	rate, _ := strconv.ParseFloat(result.LastFundingRate, 64)
	return rate, nil
}

// Format 格式化输出市场数据
func Format(data *Data) string {
	var sb strings.Builder

	// 3分钟周期指标 - 简化标题和冗余文本
	sb.WriteString("3m: ")
	sb.WriteString(fmt.Sprintf("price=%.2f, ema20=%.3f, macd=%.3f, rsi7=%.3f\n",
		data.CurrentPrice, data.CurrentEMA20, data.CurrentMACD, data.CurrentRSI7))

	// 15分钟周期指标
	sb.WriteString("15m: ")
	sb.WriteString(fmt.Sprintf("ema20=%.3f, macd=%.3f, rsi7=%.3f\n",
		data.CurrentEMA20_15m, data.CurrentMACD_15m, data.CurrentRSI7_15m))

	// 1小时周期指标
	sb.WriteString("1h: ")
	sb.WriteString(fmt.Sprintf("ema20=%.3f, macd=%.3f, rsi7=%.3f\n",
		data.CurrentEMA20_1h, data.CurrentMACD_1h, data.CurrentRSI7_1h))
	
	// 4小时周期指标 - 简化标题和冗余文本
	if data.LongerTermContext != nil && len(data.LongerTermContext.MACDValues) > 0 {
		sb.WriteString("4h: ")
		// 获取最新的MACD值
		latestMACD4h := data.LongerTermContext.MACDValues[len(data.LongerTermContext.MACDValues)-1]
		// 获取最新的RSI14值
		latestRSI144h := 0.0
		if len(data.LongerTermContext.RSI14Values) > 0 {
			latestRSI144h = data.LongerTermContext.RSI14Values[len(data.LongerTermContext.RSI14Values)-1]
		}
		sb.WriteString(fmt.Sprintf("ema20=%.3f, macd=%.3f, rsi14=%.3f\n",
			data.LongerTermContext.EMA20, latestMACD4h, latestRSI144h))
	}

	// 简化合约信息文本
	sb.WriteString(fmt.Sprintf("%s: ", data.Symbol))
	if data.OpenInterest != nil {
		sb.WriteString(fmt.Sprintf("oi=%.2f(avg=%.2f), ",
			data.OpenInterest.Latest, data.OpenInterest.Average))
	}
	sb.WriteString(fmt.Sprintf("funding=%.2e\n", data.FundingRate))

	// 简化并合并短期指标数组输出
	if data.IntradaySeries != nil {
		hasIntradayData := false
		
		// 只在有数据时添加标题
		if len(data.IntradaySeries.MidPrices) > 0 || 
		   len(data.IntradaySeries.EMA20Values) > 0 || 
		   len(data.IntradaySeries.MACDValues) > 0 || 
		   len(data.IntradaySeries.RSI7Values) > 0 || 
		   len(data.IntradaySeries.RSI14Values) > 0 {
			sb.WriteString("Intraday [old→new]: ")
			hasIntradayData = true
		}

		// 合并多个数组输出，减少换行
		if len(data.IntradaySeries.MidPrices) > 0 {
			sb.WriteString(fmt.Sprintf("prices=%s ", formatFloatSlice(data.IntradaySeries.MidPrices)))
		}

		if len(data.IntradaySeries.EMA20Values) > 0 {
			sb.WriteString(fmt.Sprintf("ema20=%s ", formatFloatSlice(data.IntradaySeries.EMA20Values)))
		}

		if len(data.IntradaySeries.MACDValues) > 0 {
			sb.WriteString(fmt.Sprintf("macd=%s ", formatFloatSlice(data.IntradaySeries.MACDValues)))
		}

		if len(data.IntradaySeries.RSI7Values) > 0 {
			sb.WriteString(fmt.Sprintf("rsi7=%s ", formatFloatSlice(data.IntradaySeries.RSI7Values)))
		}

		if len(data.IntradaySeries.RSI14Values) > 0 {
			sb.WriteString(fmt.Sprintf("rsi14=%s ", formatFloatSlice(data.IntradaySeries.RSI14Values)))
		}
		
		if hasIntradayData {
			sb.WriteString("\n")
		}
	}

	// 简化长期指标输出
	if data.LongerTermContext != nil {
		hasLongTermData := false
		
		// 只在有数据时添加标题
		if len(data.LongerTermContext.MACDValues) > 0 || 
		   len(data.LongerTermContext.RSI14Values) > 0 {
			sb.WriteString("LongTerm [4h]: ")
			hasLongTermData = true
		}

		// 合并EMA和ATR比较
		sb.WriteString(fmt.Sprintf("ema20=%.3f/ema50=%.3f, atr3=%.3f/atr14=%.3f, vol=%.3f(avg=%.3f) ",
			data.LongerTermContext.EMA20, data.LongerTermContext.EMA50,
			data.LongerTermContext.ATR3, data.LongerTermContext.ATR14,
			data.LongerTermContext.CurrentVolume, data.LongerTermContext.AverageVolume))

		// 合并MACD和RSI数组输出
		if len(data.LongerTermContext.MACDValues) > 0 {
			sb.WriteString(fmt.Sprintf("macd=%s ", formatFloatSlice(data.LongerTermContext.MACDValues)))
		}

		if len(data.LongerTermContext.RSI14Values) > 0 {
			sb.WriteString(fmt.Sprintf("rsi14=%s ", formatFloatSlice(data.LongerTermContext.RSI14Values)))
		}
		
		if hasLongTermData {
			sb.WriteString("\n")
		}
	}

	return sb.String()
}

// formatFloatSlice 格式化float64切片为字符串，输出更紧凑
func formatFloatSlice(values []float64) string {
	// 限制输出元素数量，避免过长的数组表示
	maxDisplay := 10
	displayValues := values
	if len(values) > maxDisplay {
		// 只显示前几个和最后几个元素
		displayValues = append(values[:maxDisplay/2], values[len(values)-maxDisplay/2:]...)
	}
	
	strValues := make([]string, len(displayValues))
	for i, v := range displayValues {
		// 根据数值大小动态调整精度，确保memecoins等小价值数字的精度
		if math.Abs(v) < 0.01 && v != 0 {
			// 对于非常小的值（接近0但非0），使用科学计数法以保留精度
			strValues[i] = fmt.Sprintf("%.6g", v)
		} else if math.Abs(v) < 1.0 {
			// 对于小于1的值，使用更高的小数精度
			strValues[i] = fmt.Sprintf("%.6f", v)
		} else if math.Abs(v) < 100.0 {
			// 对于中等大小的值，保持中等精度
			strValues[i] = fmt.Sprintf("%.4f", v)
		} else {
			// 对于较大的值，可以使用较少的小数位
			strValues[i] = fmt.Sprintf("%.2f", v)
		}
	}
	
	// 使用更紧凑的分隔符
	result := "[" + strings.Join(strValues, ",") + "]"
	
	// 如果截断了元素，添加指示
	if len(values) > maxDisplay {
		result += fmt.Sprintf("(%d total)", len(values))
	}
	
	return result
}

// Normalize 标准化symbol,确保是USDT交易对
func Normalize(symbol string) string {
	symbol = strings.ToUpper(symbol)
	if strings.HasSuffix(symbol, "USDT") {
		return symbol
	}
	return symbol + "USDT"
}

// parseFloat 解析float值
func parseFloat(v interface{}) (float64, error) {
	switch val := v.(type) {
	case string:
		return strconv.ParseFloat(val, 64)
	case float64:
		return val, nil
	case int:
		return float64(val), nil
	case int64:
		return float64(val), nil
	default:
		return 0, fmt.Errorf("unsupported type: %T", v)
	}
}
