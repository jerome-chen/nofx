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
	klineDataMap3m  sync.Map // 存储每个交易对的K线历史数据
	klineDataMap15m sync.Map // 存储每个交易对的K线历史数据
	klineDataMap1h  sync.Map // 存储每个交易对的K线历史数据
	klineDataMap4h  sync.Map // 存储每个交易对的K线历史数据
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
	return &WSMonitor{
		wsClient:       NewWSClient(),
		combinedClient: NewCombinedStreamsClient(batchSize),
		alertsChan:     make(chan Alert, 1000),
		batchSize:      batchSize,
	}
}

func (m *WSMonitor) Initialize(coins []string) error {
	log.Println("初始化WebSocket监控器...")
	// 获取交易对信息
	apiClient := NewAPIClient()
	// 如果不指定交易对，则使用market市场的所有交易对币种
	if len(coins) == 0 {
		exchangeInfo, err := apiClient.GetExchangeInfo()
		if err != nil {
			return err
		}
		log.Printf("传入币种为空，使用交易所的所有交易对，共 %d 个。", len(exchangeInfo.Symbols))
		// 筛选永续合约交易对 --仅测试时使用
		//exchangeInfo.Symbols = exchangeInfo.Symbols[0:2]
		for _, symbol := range exchangeInfo.Symbols {
			if symbol.Status == "TRADING" && symbol.ContractType == "PERPETUAL" && strings.ToUpper(symbol.Symbol[len(symbol.Symbol)-4:]) == "USDT" {
				m.symbols = append(m.symbols, symbol.Symbol)
				m.filterSymbols.Store(symbol.Symbol, true)
			}
		}
	} else {
		m.symbols = coins
	}

	log.Printf("找到 %d 个交易对", len(m.symbols))
	// 初始化历史数据
	if err := m.initializeHistoricalData(); err != nil {
		log.Printf("初始化历史数据失败: %v", err)
	}

	return nil
}

func (m *WSMonitor) initializeHistoricalData() error {
	apiClient := NewAPIClient()

	var wg sync.WaitGroup
	semaphore := make(chan struct{}, 5) // 限制并发数

	for _, symbol := range m.symbols {
		wg.Add(1)
		semaphore <- struct{}{}

		go func(s string) {
			defer wg.Done()
			defer func() { <-semaphore }()

			// 获取3m历史K线数据
			klines3m, err := apiClient.GetKlines(s, "3m", 100)
			if err != nil {
				log.Printf("获取 %s 3m历史数据失败: %v", s, err)
			} else if len(klines3m) > 0 {
				m.klineDataMap3m.Store(s, klines3m)
				log.Printf("已加载 %s 的历史K线数据-3m: %d 条", s, len(klines3m))
			}

			// 获取15m历史K线数据
			klines15m, err := apiClient.GetKlines(s, "15m", 100)
			if err != nil {
				log.Printf("获取 %s 15m历史数据失败: %v", s, err)
			} else if len(klines15m) > 0 {
				m.klineDataMap15m.Store(s, klines15m)
				log.Printf("已加载 %s 的历史K线数据-15m: %d 条", s, len(klines15m))
			}

			// 获取1h历史K线数据
			klines1h, err := apiClient.GetKlines(s, "1h", 100)
			if err != nil {
				log.Printf("获取 %s 1h历史数据失败: %v", s, err)
			} else if len(klines1h) > 0 {
				m.klineDataMap1h.Store(s, klines1h)
				log.Printf("已加载 %s 的历史K线数据-1h: %d 条", s, len(klines1h))
			}

			// 获取4h历史K线数据
			klines4h, err := apiClient.GetKlines(s, "4h", 100)
			if err != nil {
				log.Printf("获取 %s 4h历史数据失败: %v", s, err)
			} else if len(klines4h) > 0 {
				m.klineDataMap4h.Store(s, klines4h)
				log.Printf("已加载 %s 的历史K线数据-4h: %d 条", s, len(klines4h))
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

	err = m.combinedClient.Connect()
	if err != nil {
		log.Fatalf("❌ 批量订阅流: %v", err)
		return
	}
	// 订阅所有交易对
	err = m.subscribeAll()
	if err != nil {
		log.Fatalf("❌ 订阅币种交易对: %v", err)
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

func (m *WSMonitor) getKlineDataMap(_time string) *sync.Map {
	var klineDataMap *sync.Map
	switch _time {
	case "3m":
		klineDataMap = &m.klineDataMap3m
	case "15m":
		klineDataMap = &m.klineDataMap15m
	case "1h":
		klineDataMap = &m.klineDataMap1h
	case "4h":
		klineDataMap = &m.klineDataMap4h
	default:
		klineDataMap = &sync.Map{}
	}
	return klineDataMap
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
	var klineDataMap = m.getKlineDataMap(_time)
	value, exists := klineDataMap.Load(symbol)
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

	klineDataMap.Store(symbol, klines)
}

func (m *WSMonitor) GetCurrentKlines(symbol string, _time string) ([]Kline, error) {
	// 统一将symbol转换为大写
	upperSymbol := strings.ToUpper(symbol)
	// 对每一个进来的symbol检测是否存在内类 是否的话就订阅它
	value, exists := m.getKlineDataMap(_time).Load(upperSymbol)
	if !exists {
		// 如果Ws数据未初始化完成时,单独使用api获取 - 兼容性代码 (防止在未初始化完成是,已经有交易员运行)
		apiClient := NewAPIClient()
		klines, err := apiClient.GetKlines(upperSymbol, _time, 100)
		if err != nil {
			return nil, fmt.Errorf("获取%v分钟K线失败: %v", _time, err)
		}
		m.getKlineDataMap(_time).Store(upperSymbol, klines) //动态缓存进缓存
		subStr := m.subscribeSymbol(upperSymbol, _time)
		subErr := m.combinedClient.subscribeStreams(subStr)
		log.Printf("动态订阅流: %v", subStr)
		if subErr != nil {
			return nil, fmt.Errorf("动态订阅%v分钟K线失败: %v", _time, subErr)
		}
		return klines, nil
	}
	return value.([]Kline), nil
}

func (m *WSMonitor) Close() {
	m.wsClient.Close()
	close(m.alertsChan)
}
