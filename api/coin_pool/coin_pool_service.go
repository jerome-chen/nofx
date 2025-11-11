package coin_pool

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

// CoinPoolService 提供coin_pool相关的数据服务
type CoinPoolService struct {
	binanceAPIURL   string
	coingeckoAPIURL string
	coinCache       map[string]string
	cacheMutex      sync.RWMutex
	cacheExpiry     time.Time
	// OI数据缓存
	openInterestCache  map[string]float64
	openInterestMutex  sync.RWMutex
	openInterestExpiry time.Time
}

// NewCoinPoolService 创建新的CoinPoolService实例
func NewCoinPoolService() *CoinPoolService {
	return &CoinPoolService{
		binanceAPIURL:      "https://api.binance.com",
		coingeckoAPIURL:    "https://api.coingecko.com",
		coinCache:          make(map[string]string),
		cacheExpiry:        time.Now().Add(-time.Hour), // 初始设置为已过期，确保第一次调用会加载缓存
		openInterestCache:  make(map[string]float64),
		openInterestExpiry: time.Now().Add(-time.Minute), // 初始设置为已过期，确保第一次调用会加载缓存
	}
}

// BinanceTicker24h 定义Binance 24小时行情响应结构
type BinanceTicker24h struct {
	Symbol             string `json:"symbol"`
	PriceChange        string `json:"priceChange"`
	PriceChangePercent string `json:"priceChangePercent"`
	WeightedAvgPrice   string `json:"weightedAvgPrice"`
	PrevClosePrice     string `json:"prevClosePrice"`
	LastPrice          string `json:"lastPrice"`
	LastQty            string `json:"lastQty"`
	BidPrice           string `json:"bidPrice"`
	BidQty             string `json:"bidQty"`
	AskPrice           string `json:"askPrice"`
	AskQty             string `json:"askQty"`
	OpenPrice          string `json:"openPrice"`
	HighPrice          string `json:"highPrice"`
	LowPrice           string `json:"lowPrice"`
	Volume             string `json:"volume"`
	QuoteVolume        string `json:"quoteVolume"`
	OpenTime           int64  `json:"openTime"`
	CloseTime          int64  `json:"closeTime"`
	FirstId            int64  `json:"firstId"`
	LastId             int64  `json:"lastId"`
	Count              int64  `json:"count"`
}

// BinanceExchangeInfo 定义Binance交易所信息响应结构
type BinanceExchangeInfo struct {
	Timezone   string `json:"timezone"`
	ServerTime int64  `json:"serverTime"`
	RateLimits []struct {
		RateLimitType string `json:"rateLimitType"`
		Interval      string `json:"interval"`
		Limit         int    `json:"limit"`
	} `json:"rateLimits"`
	ExchangeFilters []interface{} `json:"exchangeFilters"`
	Symbols         []struct {
		Symbol                     string   `json:"symbol"`
		Status                     string   `json:"status"`
		BaseAsset                  string   `json:"baseAsset"`
		BaseAssetPrecision         int      `json:"baseAssetPrecision"`
		QuoteAsset                 string   `json:"quoteAsset"`
		QuotePrecision             int      `json:"quotePrecision"`
		OrderTypes                 []string `json:"orderTypes"`
		IcebergAllowed             bool     `json:"icebergAllowed"`
		OcoAllowed                 bool     `json:"ocoAllowed"`
		QuoteOrderQtyMarketAllowed bool     `json:"quoteOrderQtyMarketAllowed"`
		AllowTrailingStop          bool     `json:"allowTrailingStop"`
		IsSpotTradingAllowed       bool     `json:"isSpotTradingAllowed"`
		IsMarginTradingAllowed     bool     `json:"isMarginTradingAllowed"`
		Filters                    []struct {
			FilterType  string `json:"filterType"`
			MinPrice    string `json:"minPrice,omitempty"`
			MaxPrice    string `json:"maxPrice,omitempty"`
			TickSize    string `json:"tickSize,omitempty"`
			MinQty      string `json:"minQty,omitempty"`
			MaxQty      string `json:"maxQty,omitempty"`
			StepSize    string `json:"stepSize,omitempty"`
			MinNotional string `json:"minNotional,omitempty"`
		} `json:"filters"`
	} `json:"symbols"`
}

// BinanceFuturesTicker 定义Binance期货行情响应结构
type BinanceFuturesTicker struct {
	Symbol             string `json:"symbol"`
	PriceChange        string `json:"priceChange"`
	PriceChangePercent string `json:"priceChangePercent"`
	WeightedAvgPrice   string `json:"weightedAvgPrice"`
	LastPrice          string `json:"lastPrice"`
	LastQty            string `json:"lastQty"`
	OpenPrice          string `json:"openPrice"`
	HighPrice          string `json:"highPrice"`
	LowPrice           string `json:"lowPrice"`
	Volume             string `json:"volume"`
	QuoteVolume        string `json:"quoteVolume"`
	OpenTime           int64  `json:"openTime"`
	CloseTime          int64  `json:"closeTime"`
	FirstId            int64  `json:"firstId"`
	LastId             int64  `json:"lastId"`
	Count              int64  `json:"count"`
	OpenInterest       string `json:"openInterest"`
	OpenInterestValue  string `json:"openInterestValue"`
}

// BinanceOpenInterestResponse 定义Binance持仓量API响应结构
type BinanceOpenInterestResponse struct {
	Symbol       string `json:"symbol"`
	OpenInterest string `json:"openInterest"`
	Time         int64  `json:"time"`
}

// GetAI500CoinPool 从上游数据源获取AI500币池数据
func (s *CoinPoolService) GetAI500CoinPool() ([]CoinPoolItem, error) {
	// 获取Binance 24小时行情数据
	tickers, err := s.fetchBinance24hTickers()
	if err != nil {
		return nil, fmt.Errorf("failed to fetch binance tickers: %v", err)
	}

	// 确保coin名称缓存已加载
	err = s.loadCoinNamesCache()
	if err != nil {
		fmt.Printf("Warning: Failed to load coin names cache: %v\n", err)
		// 继续执行，即使缓存加载失败也尝试使用符号作为名称
	}

	// 过滤USDT交易对并按交易量排序
	usdtTickers := make([]BinanceTicker24h, 0)
	for _, ticker := range tickers {
		if strings.HasSuffix(ticker.Symbol, "USDT") && len(ticker.Symbol) > 4 {
			usdtTickers = append(usdtTickers, ticker)
		}
	}

	// 按美元计价的交易量排序，而不是原始交易量
	sort.Slice(usdtTickers, func(i, j int) bool {
		var quoteVol1, quoteVol2 float64
		fmt.Sscanf(usdtTickers[i].QuoteVolume, "%f", &quoteVol1)
		fmt.Sscanf(usdtTickers[j].QuoteVolume, "%f", &quoteVol2)
		return quoteVol1 > quoteVol2
	})

	// 转换为CoinPoolItem格式
	coinList := make([]CoinPoolItem, 0, 20)
	for i, ticker := range usdtTickers {
		if i >= 20 {
			break
		}

		var price, changeRate, volume, quoteVolume float64
		symbol := strings.TrimSuffix(ticker.Symbol, "USDT")
		coinName := s.getCoinNameBySymbol(symbol)

		// 改进解析逻辑
		_, priceErr := fmt.Sscanf(ticker.LastPrice, "%f", &price)
		_, changeErr := fmt.Sscanf(ticker.PriceChangePercent, "%f", &changeRate)
		_, volErr := fmt.Sscanf(ticker.Volume, "%f", &volume)
		_, quoteVolErr := fmt.Sscanf(ticker.QuoteVolume, "%f", &quoteVolume)
		if quoteVolErr != nil {
			fmt.Printf("Failed to parse quote volume for %s: %v\n", ticker.Symbol, quoteVolErr)
		}

		// 记录解析错误但继续处理
		if priceErr != nil {
			fmt.Printf("Failed to parse price for %s: %v\n", ticker.Symbol, priceErr)
		}
		if changeErr != nil {
			fmt.Printf("Failed to parse change rate for %s: %v\n", ticker.Symbol, changeErr)
		}
		if volErr != nil {
			fmt.Printf("Failed to parse volume for %s: %v\n", ticker.Symbol, volErr)
		}

		coinList = append(coinList, CoinPoolItem{
			CoinName:    coinName,
			Symbol:      symbol,
			Price:       price,
			ChangeRate:  changeRate,
			Volume:      volume,      // 保留原始交易量字段
			QuoteVolume: quoteVolume, // 添加美元计价交易量
			Rank:        i + 1,
		})
	}

	// 如果没有数据，返回错误
	if len(coinList) == 0 {
		return nil, fmt.Errorf("no valid AI500 data available")
	}

	return coinList, nil
}

// GetOITopCoinPool 从上游数据源获取OI排行榜数据
func (s *CoinPoolService) GetOITopCoinPool() ([]OITopItem, error) {
	// 首先尝试从Binance获取真实数据
	var coinList []OITopItem
	hasRealData := false

	// 1. 获取Binance期货交易对列表
	futuresTickers, err := s.fetchBinanceFuturesTickers()
	if err != nil || len(futuresTickers) == 0 {
		fmt.Printf("Error fetching futures tickers or empty result: %v\n", err)
		return s.getSampleOITopData(), nil
	}

	// 确保coin名称缓存已加载
	err = s.loadCoinNamesCache()
	if err != nil {
		fmt.Printf("Warning: Failed to load coin names cache: %v\n", err)
		// 继续执行，即使缓存加载失败也尝试使用符号作为名称
	}

	// 2. 过滤USDT永续合约交易对
	usdtSymbols := make([]string, 0)
	priceMap := make(map[string]float64)
	changeRateMap := make(map[string]float64)

	for _, ticker := range futuresTickers {
		// 只处理主流币种的USDT永续合约（不包含杠杆和期权合约）
		if strings.HasSuffix(ticker.Symbol, "USDT") && !strings.Contains(ticker.Symbol, "_") && len(ticker.Symbol) > 4 {
			usdtSymbols = append(usdtSymbols, ticker.Symbol)

			// 解析价格和涨跌幅并存储
			var price, changeRate float64
			if _, err := fmt.Sscanf(ticker.LastPrice, "%f", &price); err == nil {
				priceMap[ticker.Symbol] = price
			}
			if _, err := fmt.Sscanf(ticker.PriceChangePercent, "%f", &changeRate); err == nil {
				changeRateMap[ticker.Symbol] = changeRate
			}
		}
	}

	fmt.Printf("Debug: Filtered %d USDT futures symbols\n", len(usdtSymbols))

	// 3. 并发获取每个交易对的持仓量数据
	// 使用goroutine池控制并发数量，避免超出API限制
	type oiResult struct {
		symbol       string
		openInterest float64
		err          error
	}

	resultChan := make(chan oiResult, len(usdtSymbols))
	var wg sync.WaitGroup

	// 检查OITop数据缓存是否有效
	type cachedOITopResult struct {
		Items  []OITopItem
		Expiry time.Time
	}

	var cachedResult cachedOITopResult
	var cacheFile = "/tmp/oi_top_cache.json"

	// 尝试加载缓存文件
	if data, err := os.ReadFile(cacheFile); err == nil {
		if err := json.Unmarshal(data, &cachedResult); err == nil {
			if time.Now().Before(cachedResult.Expiry) {
				fmt.Printf("Debug: Using cached OI top data with %d items\n", len(cachedResult.Items))
				hasRealData = true
				coinList = cachedResult.Items
				return coinList, nil
			}
		}
	}

	// 限制并发请求数为10，避免触发Binance API限流
	semaphore := make(chan struct{}, 10)

	for _, symbol := range usdtSymbols {
		wg.Add(1)
		go func(sym string) {
			defer wg.Done()

			// 获取信号量
			semaphore <- struct{}{}
			defer func() { <-semaphore }()

			// 获取持仓量数据
			openInterest, err := s.fetchBinanceOpenInterest(sym)
			resultChan <- oiResult{symbol: sym, openInterest: openInterest, err: err}

			// 避免API请求过于频繁
			time.Sleep(100 * time.Millisecond)
		}(symbol)
	}

	// 等待所有goroutine完成
	go func() {
		wg.Wait()
		close(resultChan)
	}()

	// 4. 收集结果
	oiMap := make(map[string]float64)
	errorCount := 0
	for result := range resultChan {
		if result.err != nil {
			// 检查是否是币种不可用的错误
			if strings.Contains(result.err.Error(), "is not available for trading") {
				// 对于已下架或结算中的币种，完全静默处理
				continue
			}
			// 只记录有限数量的其他错误，避免日志过于冗长
			if errorCount < 5 {
				fmt.Printf("Error fetching open interest for %s: %v\n", result.symbol, result.err)
			}
			errorCount++
		} else if result.openInterest > 0 {
			oiMap[result.symbol] = result.openInterest
			// 减少调试日志输出
			// fmt.Printf("Debug: Got open interest for %s: %.2f\n", result.symbol, result.openInterest)
		}
	}

	// 如果有超过5个错误，打印一个汇总信息
	if errorCount > 5 {
		fmt.Printf("... and %d more errors (suppressed)\n", errorCount-5)
	}

	// 5. 转换为OITopItem并按持仓量排序
	sortedItems := make([]OITopItem, 0)
	for symbol, openInterest := range oiMap {
		symbolWithoutUSDT := strings.TrimSuffix(symbol, "USDT")
		coinName := s.getCoinNameBySymbol(symbolWithoutUSDT)
		price := priceMap[symbol]
		changeRate := changeRateMap[symbol]

		sortedItems = append(sortedItems, OITopItem{
			CoinName:   coinName,
			Symbol:     symbolWithoutUSDT,
			Price:      price,
			ChangeRate: changeRate,
			OI:         openInterest,
		})
	}

	// 按持仓量降序排序
	sort.Slice(sortedItems, func(i, j int) bool {
		return sortedItems[i].OI > sortedItems[j].OI
	})

	// 设置排名
	for i := range sortedItems {
		sortedItems[i].OIRank = i + 1
	}

	// 限制返回数量为前20个
	if len(sortedItems) > 20 {
		coinList = sortedItems[:20]
	} else {
		coinList = sortedItems
	}

	hasRealData = len(coinList) > 0
	fmt.Printf("Debug: Final coinList length: %d, hasRealData: %v\n", len(coinList), hasRealData)

	// 如果没有获取到真实数据，使用样本数据作为回退
	if !hasRealData {
		fmt.Printf("Using sample OI data as fallback since no real data available\n")
		coinList = s.getSampleOITopData()
	} else if len(coinList) > 0 {
		// 缓存结果到文件，有效期5分钟
		cachedResult := cachedOITopResult{
			Items:  coinList,
			Expiry: time.Now().Add(5 * time.Minute),
		}
		if data, err := json.Marshal(cachedResult); err == nil {
			// 忽略写入错误，不影响主流程
			err := os.WriteFile(cacheFile, data, 0644)
			if err != nil {
				fmt.Printf("Warning: Failed to cache OI top data: %v\n", err)
			} else {
				fmt.Printf("Debug: Cached OI top data to file, expiry: %v\n", cachedResult.Expiry)
			}
		}
	}

	return coinList, nil
}

// getSampleOITopData 提供样本持仓量数据作为API回退
func (s *CoinPoolService) getSampleOITopData() []OITopItem {
	// 返回空列表，不再使用模拟数据
	return []OITopItem{}
}

// fetchBinance24hTickers 从Binance API获取24小时行情数据
func (s *CoinPoolService) fetchBinance24hTickers() ([]BinanceTicker24h, error) {
	url := fmt.Sprintf("%s/api/v3/ticker/24hr", s.binanceAPIURL)
	resp, err := http.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var tickers []BinanceTicker24h
	err = json.Unmarshal(body, &tickers)
	if err != nil {
		return nil, err
	}

	return tickers, nil
}

// fetchBinanceFuturesTickers 从Binance API获取期货行情数据（用于获取交易对列表和价格信息）
func (s *CoinPoolService) fetchBinanceFuturesTickers() ([]BinanceFuturesTicker, error) {
	// 使用正确的永续合约API端点
	url := "https://fapi.binance.com/fapi/v1/ticker/24hr"

	// 创建HTTP客户端并设置超时
	client := &http.Client{
		Timeout: 15 * time.Second, // 增加超时时间
	}

	// 创建请求并添加User-Agent头以避免被拒绝
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/91.0.4472.124 Safari/537.36")

	// 重试机制
	maxRetries := 2
	var lastErr error

	for attempt := 0; attempt <= maxRetries; attempt++ {
		if attempt > 0 {
			// 指数退避
			time.Sleep(time.Duration(attempt*1000) * time.Millisecond)
		}

		resp, err := client.Do(req)
		if err != nil {
			// 继续尝试，不设置错误变量
			continue
		}

		defer resp.Body.Close()

		if resp.StatusCode == http.StatusOK {
			body, err := io.ReadAll(resp.Body)
			if err != nil {
				// 继续尝试
				continue
			}

			var tickers []BinanceFuturesTicker
			err = json.Unmarshal(body, &tickers)
			if err != nil {
				// 继续尝试
				continue
			}

			// 注意：根据用户反馈和实际测试，ticker数据中通常不包含有效的持仓量信息
			// 但我们仍然尝试提取，以备API在未来返回这些数据
			// ⚠️ 重要：ticker.OpenInterest 是合约数量，不是持仓价值！
			// 不应在此处缓存，因为持仓价值 = 合约数量 × 价格
			// 正确的持仓价值计算在 fetchBinanceOpenInterest 函数中
			// s.openInterestMutex.Lock()
			// hasOIData := false
			// for _, ticker := range tickers {
			// 	// 检查是否有有效的持仓量数据
			// 	if ticker.OpenInterest != "" && ticker.OpenInterest != "0" {
			// 		if oi, err := strconv.ParseFloat(ticker.OpenInterest, 64); err == nil {
			// 			s.openInterestCache[ticker.Symbol] = oi
			// 			hasOIData = true
			// 		}
			// 	}
			// }
			//
			// // 只在找到OI数据时更新过期时间
			// if hasOIData {
			// 	s.openInterestExpiry = time.Now().Add(5 * time.Minute)
			// }
			// s.openInterestMutex.Unlock()

			fmt.Printf("Successfully fetched %d futures tickers\n", len(tickers))
			return tickers, nil
		}

		// 读取错误响应体以便调试
		errorBody, _ := io.ReadAll(resp.Body)
		lastErr = fmt.Errorf("unexpected status code: %d, response: %s", resp.StatusCode, string(errorBody))
	}

	return nil, lastErr
}

// fetchBinanceOpenInterest 从Binance API获取特定交易对的持仓量数据
func (s *CoinPoolService) fetchBinanceOpenInterest(symbol string) (float64, error) {
	// 检查缓存是否有效
	s.openInterestMutex.RLock()
	if time.Now().Before(s.openInterestExpiry) {
		if oi, exists := s.openInterestCache[symbol]; exists {
			s.openInterestMutex.RUnlock()
			// 不打印每条缓存命中的日志，减少日志噪声
			return oi, nil
		}
	}
	s.openInterestMutex.RUnlock()

	// 缓存无效或不存在，从API获取
	// fmt.Printf("Debug: Fetching open interest from API for %s\n", symbol)

	// 使用Binance专门的持仓量API端点
	url := fmt.Sprintf("https://fapi.binance.com/fapi/v1/openInterest?symbol=%s", symbol)

	// 创建HTTP客户端并设置超时
	client := &http.Client{
		Timeout: 15 * time.Second, // 增加超时时间
	}

	// 创建请求并添加User-Agent头以避免被拒绝
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/91.0.4472.124 Safari/537.36")

	// 重试机制
	maxRetries := 2

	for attempt := 0; attempt <= maxRetries; attempt++ {
		if attempt > 0 {
			// 指数退避
			time.Sleep(time.Duration(attempt*1000) * time.Millisecond)
		}

		resp, err := client.Do(req)
		if err != nil {
			continue
		}

		// 读取响应体
		errorBody, _ := io.ReadAll(resp.Body)
		resp.Body.Close() // 立即关闭，避免defer导致的资源泄漏

		if resp.StatusCode == http.StatusOK {
			var oiResp BinanceOpenInterestResponse
			err = json.Unmarshal(errorBody, &oiResp)
			if err != nil {
				continue
			}

			// 解析持仓量
			var openInterest float64
			if _, err := fmt.Sscanf(oiResp.OpenInterest, "%f", &openInterest); err != nil {
				continue
			}

			// 持仓量API返回的是合约数量，需要转换为价值
			// 首先获取当前价格
			priceUrl := fmt.Sprintf("https://fapi.binance.com/fapi/v1/ticker/price?symbol=%s", symbol)
			priceReq, _ := http.NewRequest("GET", priceUrl, nil)
			priceReq.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/91.0.4472.124 Safari/537.36")

			priceResp, priceErr := client.Do(priceReq)
			if priceErr != nil {
				// 价格获取失败，返回错误，不应该返回合约数量
				return 0, fmt.Errorf("failed to get price for %s: %v", symbol, priceErr)
			}
			defer priceResp.Body.Close()

			if priceResp.StatusCode == http.StatusOK {
				priceBody, _ := io.ReadAll(priceResp.Body)

				type priceResponse struct {
					Symbol string `json:"symbol"`
					Price  string `json:"price"` // Binance返回的是字符串
				}

				var pResp priceResponse
				if err := json.Unmarshal(priceBody, &pResp); err != nil {
					// 如果解析价格失败，返回错误
					return 0, fmt.Errorf("failed to parse price response for %s: %v", symbol, err)
				}

				// 解析价格字符串为float64
				var price float64
				if _, err := fmt.Sscanf(pResp.Price, "%f", &price); err != nil {
					return 0, fmt.Errorf("failed to parse price value for %s: %v", symbol, err)
				}

				// 计算持仓价值（持仓量 * 价格）
				openInterestValue := openInterest * price

				// 更新缓存
				s.openInterestMutex.Lock()
				if time.Now().After(s.openInterestExpiry) {
					s.openInterestExpiry = time.Now().Add(5 * time.Minute)
				}
				s.openInterestCache[symbol] = openInterestValue
				s.openInterestMutex.Unlock()

				// fmt.Printf("Debug: Got open interest for %s: %.2f (count: %.2f, price: %.4f)\n", symbol, openInterestValue, openInterest, price)
				return openInterestValue, nil
			} else {
				// 价格API返回错误状态码
				return 0, fmt.Errorf("price API returned status %d for %s", priceResp.StatusCode, symbol)
			}
		} else if resp.StatusCode == http.StatusBadRequest {
			// 特殊处理400错误，检查是否是币种已下架或结算中的情况
			errorStr := string(errorBody)
			if strings.Contains(errorStr, "delivering or delivered or settling or closed or pre-trading") {
				// 这是预期的错误，币种可能已下架或处于特殊状态
				// 不返回错误，而是返回0并跳过这个币种
				return 0, fmt.Errorf("symbol %s is not available for trading", symbol)
			}
			// 记录错误但继续尝试
		} else {
			// 记录错误但继续尝试
		}
	}

	// 减少错误日志的详细程度，避免日志过于冗长
	return 0, fmt.Errorf("failed to fetch open interest for %s", symbol)
}

// loadCoinNamesCache 从CoinGecko API加载币种名称缓存
func (s *CoinPoolService) loadCoinNamesCache() error {
	// 检查缓存是否有效（1小时有效期）
	if time.Now().Before(s.cacheExpiry) {
		return nil
	}

	// 构建CoinGecko API URL
	url := fmt.Sprintf("%s/api/v3/coins/list", s.coingeckoAPIURL)

	// 创建HTTP客户端并设置超时
	client := &http.Client{
		Timeout: 15 * time.Second, // 增加超时时间
	}

	// 创建请求并添加User-Agent头
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/91.0.4472.124 Safari/537.36")

	// 添加重试机制
	maxRetries := 3
	var lastErr error
	var body []byte
	var resp *http.Response

	for attempt := 0; attempt <= maxRetries; attempt++ {
		if attempt > 0 {
			// 指数退避
			sleepTime := time.Duration(attempt*1000) * time.Millisecond
			fmt.Printf("Retrying CoinGecko API request after %v...\n", sleepTime)
			time.Sleep(sleepTime)
		}

		resp, err = client.Do(req)
		if err != nil {
			lastErr = err
			continue
		}

		body, err = io.ReadAll(resp.Body)
		resp.Body.Close() // 立即关闭，避免defer导致的资源泄漏

		if resp.StatusCode != http.StatusOK {
			lastErr = fmt.Errorf("unexpected status code: %d", resp.StatusCode)
			continue
		}

		if err != nil {
			lastErr = err
			continue
		}

		break // 成功获取响应
	}

	// 即使API调用失败，我们仍然可以使用预定义的币种名称
	s.cacheMutex.Lock()
	defer s.cacheMutex.Unlock()

	// 直接设置主流币种的官方名称（无论API调用是否成功）
	// 这种方法最可靠，确保主流币种始终显示其标准名称
	s.coinCache = map[string]string{
		"BTC":   "Bitcoin",
		"ETH":   "Ethereum",
		"BNB":   "BNB",
		"SOL":   "Solana",
		"XRP":   "XRP",
		"ADA":   "Cardano",
		"DOT":   "Polkadot",
		"DOGE":  "Dogecoin",
		"AVAX":  "Avalanche",
		"MATIC": "Polygon",
		"LINK":  "Chainlink",
		"UNI":   "Uniswap",
		"LTC":   "Litecoin",
		"BCH":   "Bitcoin Cash",
		"TRX":   "TRON",
		"XMR":   "Monero",
		"ATOM":  "Cosmos",
		"XTZ":   "Tezos",
		"EOS":   "EOS",
		"ALGO":  "Algorand",
		"ICP":   "Internet Computer",
		"FIL":   "Filecoin",
	}

	// 只有在API调用成功时才尝试解析和加载额外的币种名称
	if err == nil && resp != nil && resp.StatusCode == http.StatusOK {
		// 解析CoinGecko响应
		type coinInfo struct {
			ID     string `json:"id"`
			Symbol string `json:"symbol"`
			Name   string `json:"name"`
		}

		var coins []coinInfo
		if err := json.Unmarshal(body, &coins); err == nil {
			// 加载CoinGecko的数据，但跳过已经设置的主流币种
			for _, coin := range coins {
				symbol := strings.ToUpper(coin.Symbol)
				// 只有当该符号不在已设置的主流币种中时才添加
				if _, exists := s.coinCache[symbol]; !exists {
					s.coinCache[symbol] = coin.Name
				}
			}
		}
	}

	// 无论API调用是否成功，都更新缓存过期时间
	s.cacheExpiry = time.Now().Add(time.Hour)
	fmt.Printf("Successfully loaded %d coin names (API status: %v)\n", len(s.coinCache), lastErr == nil)

	// 如果API调用失败，返回错误但仍然使用预定义的币种名称
	return lastErr
}

// getCoinNameBySymbol 根据币种符号获取币种名称
func (s *CoinPoolService) getCoinNameBySymbol(symbol string) string {
	// 首先尝试从缓存获取
	s.cacheMutex.RLock()
	name, exists := s.coinCache[symbol]
	s.cacheMutex.RUnlock()

	if exists {
		return name
	}

	// 如果缓存中没有，尝试刷新缓存后再查询
	err := s.loadCoinNamesCache()
	if err != nil {
		fmt.Printf("Warning: Failed to refresh coin cache for symbol %s: %v\n", symbol, err)
		return symbol // 如果刷新失败，返回符号作为名称
	}

	// 再次尝试从更新后的缓存获取
	s.cacheMutex.RLock()
	name, exists = s.coinCache[symbol]
	s.cacheMutex.RUnlock()

	if exists {
		return name
	}

	// 如果仍然没有，返回符号作为名称
	return symbol
}
