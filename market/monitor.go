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
		log.Fatalf("❌ 初始化币种: %v", err)
		return
	}

	// 先建立WebSocket连接
	err = m.combinedClient.Connect()
	if err != nil {
		log.Fatalf("❌ 建立WebSocket连接失败: %v", err)
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
		log.Printf("⚠️ 订阅币种交易对失败: %v", err)
		// 不致命，继续运行
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
// subscribeAll 订阅所有交易对
func (m *WSMonitor) subscribeAll() error {
	// 执行批量订阅
	log.Println("开始订阅所有交易对...")
	for _, symbol := range m.symbols {
		for _, st := range subKlineTime {
			m.subscribeSymbol(symbol, st)
		}
	}
	for _, st := range subKlineTime {
		err := m.combinedClient.BatchSubscribeKlines(m.symbols, st)
		if err != nil {
			log.Printf("❌ 订阅%v K线: %v", st, err) // 修改为log.Printf，避免程序退出
			// 不立即返回错误，继续尝试订阅其他时间周期
		}
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

// 获取当前K线数据
func (m *WSMonitor) GetCurrentKlines(symbol, timeFrame string) ([]Kline, error) {
	// 检查是否已有该时间周期的数据
	if dataMap, exists := m.klineDataMap[timeFrame]; exists {
		if klines, ok := dataMap.Load(symbol); ok {
			if klineList, ok := klines.([]Kline); ok && len(klineList) > 0 {
				return klineList, nil
			}
		}
	}
	
	// 如果没有3m数据，尝试动态订阅
	if timeFrame == "3m" {
		log.Printf("动态订阅 %s %s K线数据", symbol, timeFrame)
		
		// 创建单独的WebSocket客户端进行动态订阅
		wsClient := NewWSClient()
		err := wsClient.Connect()
		if err != nil {
			log.Printf("动态订阅连接失败: %v", err)
		} else {
			// 订阅特定交易对和时间周期
			err = wsClient.SubscribeKline(symbol, timeFrame)
			if err != nil {
				log.Printf("动态订阅失败: %v", err)
			} else {
				// 等待一下让数据到达
				time.Sleep(200 * time.Millisecond)
				// 再次检查数据
				if dataMap, exists := m.klineDataMap[timeFrame]; exists {
					if klines, ok := dataMap.Load(symbol); ok {
						if klineList, ok := klines.([]Kline); ok && len(klineList) > 0 {
							wsClient.Close()
							return klineList, nil
						}
					}
				}
			}
			wsClient.Close()
		}
	}
	
	// 如果WebSocket数据不可用，回退到API
	log.Printf("WebSocket数据不可用，使用API获取 %s %s 数据", symbol, timeFrame)
	apiClient := NewAPIClient()
	klines, err := apiClient.GetKlines(symbol, timeFrame, 200)
	if err != nil {
		return nil, fmt.Errorf("获取K线数据失败: %v", err)
	}
	
	// 缓存API数据
	if dataMap, exists := m.klineDataMap[timeFrame]; exists {
		dataMap.Store(symbol, klines)
	}
	
	return klines, nil
}

func (m *WSMonitor) Close() {
	m.wsClient.Close()
	close(m.alertsChan)
}

// 订阅K线流
func (m *WSMonitor) subscribeToStreams() error {
	// 只订阅3m时间周期以减少WebSocket负载
	timeFrame := "3m"
	
	log.Printf("订阅 %d 个交易对的 %s K线流", len(m.symbols), timeFrame)
	
	// 使用现有的批量订阅方法
	err := m.combinedClient.BatchSubscribeKlines(m.symbols, timeFrame)
	if err != nil {
		return fmt.Errorf("批量订阅 %s K线失败: %v", timeFrame, err)
	}
	
	log.Printf("成功订阅 %d 个交易对的 %s K线流", len(m.symbols), timeFrame)
	return nil
}
