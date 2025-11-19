package market

import (
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"
)

type WSMonitor struct {
	wsClient        *WSClient
	combinedClient  *CombinedStreamsClient
	symbols         []string
	featuresMap     sync.Map
	alertsChan      chan Alert
	klineDataMap    map[string]*sync.Map // 存储每个时间周期的K线历史数据
	tickerDataMap   sync.Map // 存储每个交易对的ticker数据
	batchSize       int
	filterSymbols   sync.Map // 使用sync.Map来存储需要监控的币种和其状态
	symbolStats     sync.Map // 存储币种统计信息
	FilterSymbol    []string //经过筛选的币种
}
type SymbolStats struct {
	LastActiveTime   time.Time
	AlertCount       int
	VolumeSpikeCount int
	LastAlertTime    time.Time
	Score            float64 // 综合评分
}

var subKlineTime = []string{"3m", "15m", "1h", "4h"} // 管理订阅流的K线周期

func NewWSMonitor(batchSize int) *WSMonitor {
	// 初始化klineDataMap
	klineDataMap := make(map[string]*sync.Map)
	for _, timeFrame := range subKlineTime {
		klineDataMap[timeFrame] = &sync.Map{}
	}
	
	return &WSMonitor{
		wsClient:       NewWSClient(),
		combinedClient: NewCombinedStreamsClient(batchSize),
		alertsChan:     make(chan Alert, 1000),
		batchSize:      batchSize,
		klineDataMap:   klineDataMap,
	}
}

// 初始化WSMonitor，加载历史数据
func (m *WSMonitor) Initialize(coins []string) error {
	// 如果没有指定coins，则获取所有交易对
	if len(coins) == 0 {
		apiClient := NewAPIClient()
		exchangeInfo, err := apiClient.GetExchangeInfo()
		if err != nil {
			return fmt.Errorf("获取交易对信息失败: %v", err)
		}
		// 从SymbolInfo中提取symbol字符串
		symbols := make([]string, len(exchangeInfo.Symbols))
		for i, symbolInfo := range exchangeInfo.Symbols {
			symbols[i] = symbolInfo.Symbol
		}
		m.symbols = symbols
	} else {
		m.symbols = coins
	}
	
	// 限制交易对数量以避免WebSocket订阅速率限制
	maxPairs := 10
	if len(m.symbols) > maxPairs {
		m.symbols = m.symbols[:maxPairs]
	}
	
	log.Printf("找到 %d 个交易对 (限制为最多10个以避免WebSocket限制)", len(m.symbols))
	if len(m.symbols) > 0 {
		log.Printf("订阅的交易对: %v", m.symbols)
	}
	
	// 初始化所有时间周期的klineDataMap
	for _, timeFrame := range subKlineTime {
		m.klineDataMap[timeFrame] = &sync.Map{}
	}
	
	// 加载历史数据
	err := m.loadHistoricalData()
	if err != nil {
		return fmt.Errorf("加载历史数据失败: %v", err)
	}
	
	// 注意：不在这里订阅WebSocket流，等待连接建立后再订阅
	return nil
}

func (m *WSMonitor) loadHistoricalData() error {
	apiClient := NewAPIClient()

	var wg sync.WaitGroup
	semaphore := make(chan struct{}, 5) // 限制并发数

	for _, symbol := range m.symbols {
		wg.Add(1)
		semaphore <- struct{}{}

		go func(s string) {
			defer wg.Done()
			defer func() { <-semaphore }()

			// 获取所有时间周期的历史K线数据
			for _, timeFrame := range subKlineTime {
				klines, err := apiClient.GetKlines(s, timeFrame, 100)
				if err != nil {
					log.Printf("获取 %s %s历史数据失败: %v", s, timeFrame, err)
				} else if len(klines) > 0 {
					if dataMap, exists := m.klineDataMap[timeFrame]; exists {
						dataMap.Store(s, klines)
						log.Printf("已加载 %s 的历史K线数据-%s: %d 条", s, timeFrame, len(klines))
					}
				}
			}
		}(symbol)
	}

	wg.Wait()
	return nil
}

func (m *WSMonitor) Start(coins []string) {
	log.Printf("启动WebSocket实时监控...")
	// 初始化交易对
	err := m.Initialize(coins)
	if err != nil {
		log.Printf("❌ 初始化币种失败: %v", err)
		return
	}

	// 先建立WebSocket连接
	err = m.combinedClient.Connect()
	if err != nil {
		log.Printf("❌ 批量订阅流失败: %v", err)
		return
	}

	// 连接建立后再订阅流
	err = m.subscribeToStreams()
	if err != nil {
		log.Fatalf("❌ 订阅WebSocket流失败: %v", err)
		return
	}

	// 订阅所有交易对（用于兼容性）
	err = m.subscribeAll()
	if err != nil {
		log.Printf("❌ 订阅币种交易对失败: %v", err)
		return
	}
}

// subscribeSymbol 注册监听
func (m *WSMonitor) subscribeSymbol(symbol, st string) []string {
	var streams []string
	stream := fmt.Sprintf("%s@kline_%s", strings.ToLower(symbol), st)
	ch := m.combinedClient.AddSubscriber(stream, 100)
	streams = append(streams, stream)
	go m.handleKlineData(symbol, ch, st)

	return streams
}

// subscribeTicker 注册ticker监听
func (m *WSMonitor) subscribeTicker(symbol string) string {
	stream := fmt.Sprintf("%s@ticker", strings.ToLower(symbol))
	ch := m.combinedClient.AddSubscriber(stream, 100)
	go m.handleTickerData(symbol, ch)
	return stream
}
// subscribeAll 订阅所有交易对
func (m *WSMonitor) subscribeAll() error {
	// 执行批量订阅（不再使用单独的subscribeSymbol以避免重复订阅）
	log.Println("开始订阅所有交易对...")
	for _, st := range subKlineTime {
		err := m.combinedClient.BatchSubscribeKlines(m.symbols, st)
		if err != nil {
			log.Printf("❌ 订阅 %s K线失败: %v", st, err)
			return err
		}
	}
	
	// 批量订阅ticker数据
	err := m.combinedClient.BatchSubscribeTickers(m.symbols)
	if err != nil {
		log.Printf("❌ 订阅ticker: %v", err)
		// 不致命，继续运行
	}
	
	// 为每个symbol注册ticker订阅者以接收数据
	for _, symbol := range m.symbols {
		m.subscribeTicker(symbol)
	}
	
	log.Println("所有交易对订阅完成")
	return nil
}

func (m *WSMonitor) handleKlineMessage(data []byte) {
	var klineData KlineWSData
	if err := json.Unmarshal(data, &klineData); err != nil {
		log.Printf("解析Kline数据失败: %v", err)
		return
	}
	
	// 从stream中提取symbol和timeFrame
	stream := klineData.Stream
	parts := strings.Split(stream, "@")
	if len(parts) != 2 {
		log.Printf("无效的stream格式: %s", stream)
		return
	}
	
	symbol := strings.ToUpper(parts[0])
	timeFrameParts := strings.Split(parts[1], "_")
	if len(timeFrameParts) != 2 {
		log.Printf("无效的时间周期格式: %s", parts[1])
		return
	}
	
	timeFrame := timeFrameParts[1]
	m.processKlineUpdate(symbol, klineData, timeFrame)
}

func (m *WSMonitor) handleTickerMessage(data []byte) {
	var tickerData TickerWSData
	if err := json.Unmarshal(data, &tickerData); err != nil {
		log.Printf("解析Ticker数据失败: %v", err)
		return
	}
	
	symbol := strings.ToUpper(tickerData.Symbol)
	
	// 解析价格
	price, err := parseFloat(tickerData.LastPrice)
	if err != nil {
		log.Printf("解析ticker价格失败: %v", err)
		return
	}
	
	// 存储到ticker数据映射
	m.tickerDataMap.Store(symbol, price)
	
	// 调试日志
	log.Printf("🔍 [DEBUG] Ticker %s 实时价格更新: %.6f", symbol, price)
}

func (m *WSMonitor) handleKlineData(symbol string, ch <-chan []byte, _time string) {
	for data := range ch {
		var klineData KlineWSData
		if err := json.Unmarshal(data, &klineData); err != nil {
			log.Printf("解析Kline数据失败: %v", err)
			continue
		}
		m.processKlineUpdate(symbol, klineData, _time)
	}
}

func (m *WSMonitor) handleTickerData(symbol string, ch <-chan []byte) {
	for data := range ch {
		var tickerData TickerWSData
		if err := json.Unmarshal(data, &tickerData); err != nil {
			log.Printf("解析Ticker数据失败: %v", err)
			continue
		}
		
		// 解析价格
		price, err := parseFloat(tickerData.LastPrice)
		if err != nil {
			log.Printf("解析ticker价格失败: %v", err)
			continue
		}
		
		// 存储到ticker数据映射
		m.tickerDataMap.Store(symbol, price)
		
		// 调试日志
		log.Printf("🔍 [DEBUG] Ticker %s 实时价格更新: %.6f", symbol, price)
	}
}

func (m *WSMonitor) processKlineUpdate(symbol string, wsData KlineWSData, _time string) {
	// 转换WebSocket数据为Kline结构
	kline := Kline{
		OpenTime:  wsData.Kline.StartTime,
		CloseTime: wsData.Kline.CloseTime,
		Trades:    wsData.Kline.NumberOfTrades,
	}
	kline.Open, _ = parseFloat(wsData.Kline.OpenPrice)
	kline.High, _ = parseFloat(wsData.Kline.HighPrice)
	kline.Low, _ = parseFloat(wsData.Kline.LowPrice)
	kline.Close, _ = parseFloat(wsData.Kline.ClosePrice)
	kline.Volume, _ = parseFloat(wsData.Kline.Volume)
	kline.High, _ = parseFloat(wsData.Kline.HighPrice)
	kline.QuoteVolume, _ = parseFloat(wsData.Kline.QuoteVolume)
	kline.TakerBuyBaseVolume, _ = parseFloat(wsData.Kline.TakerBuyBaseVolume)
	kline.TakerBuyQuoteVolume, _ = parseFloat(wsData.Kline.TakerBuyQuoteVolume)

	// 调试日志：输出WebSocket价格更新
	if _time == "3m" {
		log.Printf("🔍 [DEBUG] WebSocket %s %s 价格更新: %.6f (是否完成: %v)", 
			symbol, _time, kline.Close, wsData.Kline.IsFinal)
	}
	
	// 更新K线数据
	if dataMap, exists := m.klineDataMap[_time]; exists {
		value, exists := dataMap.Load(symbol)
		var klines []Kline
		if exists {
			klines = value.([]Kline)

			// 检查是否是新的K线
			if len(klines) > 0 && klines[len(klines)-1].OpenTime == kline.OpenTime {
				// 更新当前K线
				klines[len(klines)-1] = kline
			} else {
				// 添加新K线
				klines = append(klines, kline)

				// 保持数据长度
				if len(klines) > 100 {
					klines = klines[1:]
				}
			}
		} else {
			klines = []Kline{kline}
		}

		dataMap.Store(symbol, klines)
	}
}

func (m *WSMonitor) GetCurrentKlines(symbol string, duration string) ([]Kline, error) {
	// 对每一个进来的symbol检测是否存在内类 是否的话就订阅它
	value, exists := m.getKlineDataMap(duration).Load(symbol)
	if !exists {
		// 如果Ws数据未初始化完成时,单独使用api获取 - 兼容性代码 (防止在未初始化完成是,已经有交易员运行)
		apiClient := NewAPIClient()
		klines, err := apiClient.GetKlines(symbol, duration, 100)
		if err != nil {
			return nil, fmt.Errorf("获取%v分钟K线失败: %v", duration, err)
		}

		// 动态缓存进缓存
		m.getKlineDataMap(duration).Store(strings.ToUpper(symbol), klines)

		// 订阅 WebSocket 流
		subStr := m.subscribeSymbol(symbol, duration)
		subErr := m.combinedClient.subscribeStreams(subStr)
		log.Printf("动态订阅流: %v", subStr)
		if subErr != nil {
			log.Printf("警告: 动态订阅%v分钟K线失败: %v (使用API数据)", duration, subErr)
		}

		// ✅ FIX: 返回深拷贝而非引用
		result := make([]Kline, len(klines))
		copy(result, klines)
		return result, nil
	}

	// ✅ FIX: 返回深拷贝而非引用，避免并发竞态条件
	klines := value.([]Kline)
	result := make([]Kline, len(klines))
	copy(result, klines)
	return result, nil
}

func (m *WSMonitor) Close() {
	m.wsClient.Close()
	close(m.alertsChan)
}

// 订阅K线流
func (m *WSMonitor) subscribeToStreams() error {
	// 订阅3m时间周期K线流
	timeFrame := "3m"
	
	log.Printf("订阅 %d 个交易对的 %s K线流", len(m.symbols), timeFrame)
	
	// 使用现有的批量订阅方法
	err := m.combinedClient.BatchSubscribeKlines(m.symbols, timeFrame)
	if err != nil {
		return fmt.Errorf("批量订阅 %s K线失败: %v", timeFrame, err)
	}
	
	log.Printf("成功订阅 %d 个交易对的 %s K线流", len(m.symbols), timeFrame)
	
	// 订阅ticker流以获取实时价格更新
	log.Printf("订阅 %d 个交易对的 ticker 流", len(m.symbols))
	
	err = m.combinedClient.BatchSubscribeTickers(m.symbols)
	if err != nil {
		log.Printf("⚠️ 批量订阅 ticker 失败: %v", err)
		// 不致命，继续运行
	} else {
		log.Printf("成功订阅 %d 个交易对的 ticker 流", len(m.symbols))
	}
	
	return nil
}
