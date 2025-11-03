package trader

import (
	"encoding/json"
	"fmt"
	"log"
	"math"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"nofx/decision"
	"nofx/logger"
	"nofx/market"
	"nofx/mcp"
	"nofx/pool"
)

// LocalRecentTrade 本地交易记录结构（与decision.RecentTrade保持一致）
type LocalRecentTrade struct {
	Symbol     string  `json:"symbol"`
	Side       string  `json:"side"`
	EntryPrice float64 `json:"entry_price"`
	ClosePrice float64 `json:"close_price"`
	Duration   string  `json:"duration"`
	PnLPct     float64 `json:"pn_l_pct"`
	Reason     string  `json:"reason"`
	CloseTime  int64   `json:"close_time,omitempty"` // 平仓时间戳（毫秒）
}

// AutoTraderConfig 自动交易配置（简化版 - AI全权决策）
type AutoTraderConfig struct {
	// Trader标识
	ID      string // Trader唯一标识（用于日志目录等）
	Name    string // Trader显示名称
	AIModel string // AI模型: "qwen" 或 "deepseek"

	// 交易平台选择
	Exchange string // "binance", "hyperliquid" 或 "aster"

	// 币安API配置
	BinanceAPIKey    string
	BinanceSecretKey string

	// Hyperliquid配置
	HyperliquidPrivateKey string
	HyperliquidWalletAddr string
	HyperliquidTestnet    bool

	// Aster配置
	AsterUser       string // Aster主钱包地址
	AsterSigner     string // Aster API钱包地址
	AsterPrivateKey string // Aster API钱包私钥

	CoinPoolAPIURL string

	// AI配置
	UseQwen     bool
	DeepSeekKey string
	QwenKey     string

	// 自定义AI API配置
	CustomAPIURL    string
	CustomAPIKey    string
	CustomModelName string

	// 扫描配置
	ScanInterval time.Duration // 扫描间隔（建议3分钟）

	// 账户配置
	InitialBalance float64 // 初始金额（用于计算盈亏，需手动设置）

	// 杠杆配置
	BTCETHLeverage  int            // BTC和ETH的杠杆倍数
	AltcoinLeverage int            // 山寨币的杠杆倍数
	PairLeverage    map[string]int // 特定交易对的杠杆倍数

	// 风险控制（仅作为提示，AI可自主决定）
	MaxDailyLoss    float64       // 最大日亏损百分比（提示）
	MaxDrawdown     float64       // 最大回撤百分比（提示）
	StopTradingTime time.Duration // 触发风控后暂停时长
}

// 直接使用decision包中的RecentTrade类型，不再重复定义

// AutoTrader 自动交易器
type AutoTrader struct {
	id                    string // Trader唯一标识
	name                  string // Trader显示名称
	aiModel               string // AI模型名称
	exchange              string // 交易平台名称
	config                AutoTraderConfig
	trader                Trader // 使用Trader接口（支持多平台）
	mcpClient             *mcp.Client
	decisionLogger        *logger.DecisionLogger // 决策日志记录器
	initialBalance        float64
	dailyPnL              float64
	lastResetTime         time.Time
	stopUntil             time.Time
	isRunning             bool
	startTime             time.Time        // 系统启动时间
	callCount             int              // AI调用次数
	openTrades            map[string]interface{} // 当前未平仓交易 (symbol_side -> trade) - 使用interface{}以支持匿名结构体
	recentTrades          map[string]*LocalRecentTrade // 每个交易对的最近交易记录 (symbol -> trade)
}

// NewAutoTrader 创建自动交易器
func NewAutoTrader(config AutoTraderConfig) (*AutoTrader, error) {
	// 设置默认值
	if config.ID == "" {
		config.ID = "default_trader"
	}
	if config.Name == "" {
		config.Name = "Default Trader"
	}
	if config.AIModel == "" {
		if config.UseQwen {
			config.AIModel = "qwen"
		} else {
			config.AIModel = "deepseek"
		}
	}

	mcpClient := mcp.New()

	// 初始化AI
	if config.AIModel == "custom" {
		// 使用自定义API
		mcpClient.SetCustomAPI(config.CustomAPIURL, config.CustomAPIKey, config.CustomModelName)
		log.Printf("🤖 [%s] 使用自定义AI API: %s (模型: %s)", config.Name, config.CustomAPIURL, config.CustomModelName)
	} else if config.UseQwen || config.AIModel == "qwen" {
		// 使用Qwen
		mcpClient.SetQwenAPIKey(config.QwenKey, "")
		log.Printf("🤖 [%s] 使用阿里云Qwen AI", config.Name)
	} else {
		// 默认使用DeepSeek
		mcpClient.SetDeepSeekAPIKey(config.DeepSeekKey)
		log.Printf("🤖 [%s] 使用DeepSeek AI", config.Name)
	}

	// 初始化币种池API
	if config.CoinPoolAPIURL != "" {
		pool.SetCoinPoolAPI(config.CoinPoolAPIURL)
	}

	// 设置默认交易平台
	if config.Exchange == "" {
		config.Exchange = "binance"
	}

	// 根据配置创建对应的交易器
	var trader Trader
	var err error

	switch config.Exchange {
	case "binance":
		log.Printf("🏦 [%s] 使用币安合约交易", config.Name)
		trader = NewFuturesTrader(config.BinanceAPIKey, config.BinanceSecretKey)
	case "hyperliquid":
		log.Printf("🏦 [%s] 使用Hyperliquid交易", config.Name)
		trader, err = NewHyperliquidTrader(config.HyperliquidPrivateKey, config.HyperliquidWalletAddr, config.HyperliquidTestnet)
		if err != nil {
			return nil, fmt.Errorf("初始化Hyperliquid交易器失败: %w", err)
		}
	case "aster":
		log.Printf("🏦 [%s] 使用Aster交易", config.Name)
		trader, err = NewAsterTrader(config.AsterUser, config.AsterSigner, config.AsterPrivateKey)
		if err != nil {
			return nil, fmt.Errorf("初始化Aster交易器失败: %w", err)
		}
	default:
		return nil, fmt.Errorf("不支持的交易平台: %s", config.Exchange)
	}

	// 验证初始金额配置
	if config.InitialBalance <= 0 {
		return nil, fmt.Errorf("初始金额必须大于0，请在配置中设置InitialBalance")
	}

	// 初始化决策日志记录器（使用trader ID创建独立目录）
	logDir := fmt.Sprintf("decision_logs/%s", config.ID)
	decisionLogger := logger.NewDecisionLogger(logDir)

	// 创建自动交易器实例
	at := &AutoTrader{
		id:             config.ID,
		name:           config.Name,
		aiModel:        config.AIModel,
		exchange:       config.Exchange,
		config:         config,
		trader:         trader,
		mcpClient:      mcpClient,
		decisionLogger: decisionLogger,
		initialBalance: config.InitialBalance,
		lastResetTime:  time.Now(),
		startTime:      time.Now(),
		callCount:      0,
		isRunning:      false,
		openTrades:     make(map[string]interface{}), // 初始化未平仓交易map (symbol_side -> trade)
		recentTrades:   make(map[string]*LocalRecentTrade), // 初始化最近交易记录map (symbol -> trade)
	}
	
	// 尝试加载保存的最近交易记录
	err = at.loadRecentTrades()
	if err != nil {
		log.Printf("  ⚠ 加载最近交易记录失败: %v (这是正常的，如果是首次运行)", err)
	}
	
	return at, nil
}

// Run 运行自动交易主循环
func (at *AutoTrader) Run() error {
	at.isRunning = true
	log.Println("🚀 AI驱动自动交易系统启动")
	log.Printf("💰 初始余额: %.2f USDT", at.initialBalance)
	log.Printf("⚙️  扫描间隔: %v", at.config.ScanInterval)
	log.Println("🤖 AI将全权决定杠杆、仓位大小、止损止盈等参数")

	ticker := time.NewTicker(at.config.ScanInterval)
	defer ticker.Stop()

	// 首次立即执行
	if err := at.runCycle(); err != nil {
		log.Printf("❌ 执行失败: %v", err)
	}

	for at.isRunning {
		select {
		case <-ticker.C:
			if err := at.runCycle(); err != nil {
				log.Printf("❌ 执行失败: %v", err)
			}
		}
	}

	return nil
}

// Stop 停止自动交易
func (at *AutoTrader) Stop() {
	at.isRunning = false
	log.Println("⏹ 自动交易系统停止")
}

// runCycle 运行一个交易周期（使用AI全权决策）
func (at *AutoTrader) runCycle() error {
	at.callCount++

	log.Printf("\n%s", strings.Repeat("=", 70))
	log.Printf("⏰ %s - AI决策周期 #%d", time.Now().Format("2006-01-02 15:04:05"), at.callCount)
	log.Printf("%s", strings.Repeat("=", 70))

	// 创建决策记录
	record := &logger.DecisionRecord{
		ExecutionLog: []string{},
		Success:      true,
	}

	// 1. 检查是否需要停止交易
	if time.Now().Before(at.stopUntil) {
		remaining := at.stopUntil.Sub(time.Now())
		log.Printf("⏸ 风险控制：暂停交易中，剩余 %.0f 分钟", remaining.Minutes())
		record.Success = false
		record.ErrorMessage = fmt.Sprintf("风险控制暂停中，剩余 %.0f 分钟", remaining.Minutes())
		at.decisionLogger.LogDecision(record)
		return nil
	}

	// 2. 重置日盈亏（每天重置）
	if time.Since(at.lastResetTime) > 24*time.Hour {
		at.dailyPnL = 0
		at.lastResetTime = time.Now()
		log.Println("📅 日盈亏已重置")
	}

	// 3. 收集交易上下文
	ctx, err := at.buildTradingContext()
	if err != nil {
		record.Success = false
		record.ErrorMessage = fmt.Sprintf("构建交易上下文失败: %v", err)
		at.decisionLogger.LogDecision(record)
		return fmt.Errorf("构建交易上下文失败: %w", err)
	}

	// 保存账户状态快照
	record.AccountState = logger.AccountSnapshot{
		TotalBalance:          ctx.Account.TotalEquity,
		AvailableBalance:      ctx.Account.AvailableBalance,
		TotalUnrealizedProfit: ctx.Account.TotalPnL,
		PositionCount:         ctx.Account.PositionCount,
		MarginUsedPct:         ctx.Account.MarginUsedPct,
	}

	// 保存持仓快照
	for _, pos := range ctx.Positions {
		record.Positions = append(record.Positions, logger.PositionSnapshot{
			Symbol:           pos.Symbol,
			Side:             pos.Side,
			PositionAmt:      pos.Quantity,
			EntryPrice:       pos.EntryPrice,
			MarkPrice:        pos.MarkPrice,
			UnrealizedProfit: pos.UnrealizedPnL,
			Leverage:         float64(pos.Leverage),
			LiquidationPrice: pos.LiquidationPrice,
		})
	}

	// 保存候选币种列表
	for _, coin := range ctx.CandidateCoins {
		record.CandidateCoins = append(record.CandidateCoins, coin.Symbol)
	}

	// 保存最近交易记录
	for _, trade := range at.recentTrades {
		record.RecentTrades = append(record.RecentTrades, logger.RecentTrade{
			Symbol:     trade.Symbol,
			Side:       trade.Side,
			EntryPrice: trade.EntryPrice,
			ClosePrice: trade.ClosePrice,
			Duration:   trade.Duration,
			PnLPct:     trade.PnLPct,
			Reason:     trade.Reason,
		})
	}

	log.Printf("📊 账户净值: %.2f USDT | 可用: %.2f USDT | 持仓: %d",
		ctx.Account.TotalEquity, ctx.Account.AvailableBalance, ctx.Account.PositionCount)

	// 4. 调用AI获取完整决策
	log.Printf("🤖 [%s] 正在请求AI分析并决策...", at.name)
	decision, err := decision.GetFullDecision(ctx, at.mcpClient)

	// 即使有错误，也保存思维链、决策和输入prompt（用于debug）
	if decision != nil {
		record.InputPrompt = decision.UserPrompt
		record.CoTTrace = decision.CoTTrace
		if len(decision.Decisions) > 0 {
			decisionJSON, _ := json.MarshalIndent(decision.Decisions, "", "  ")
			record.DecisionJSON = string(decisionJSON)
		}
	}

	if err != nil {
		// 检查错误是否包含"使用默认等待决策"，这表明我们的增强逻辑已经介入
		hasDefaultDecision := strings.Contains(err.Error(), "使用默认等待决策")
		
		if hasDefaultDecision && decision != nil && len(decision.Decisions) > 0 {
			// 即使有错误，但我们已经生成了默认决策，可以继续执行
			log.Printf("⚠️ AI响应被截断，但已生成默认等待决策，将继续执行")
			record.Success = true // 标记为成功，因为我们有有效的决策可以执行
			record.WarningMessage = fmt.Sprintf("AI响应处理警告: %v", err)
		} else {
			// 真正的错误情况
			record.Success = false
			record.ErrorMessage = fmt.Sprintf("获取AI决策失败: %v", err)

			// 打印AI思维链（即使有错误）
			if decision != nil && decision.CoTTrace != "" {
				log.Printf("\n%s", strings.Repeat("-", 70))
				log.Println("💭 AI思维链分析（错误情况）:")
				log.Println(strings.Repeat("-", 70))
				log.Println(decision.CoTTrace)
				log.Printf("%s\n", strings.Repeat("-", 70))
			}

			at.decisionLogger.LogDecision(record)
			return fmt.Errorf("获取AI决策失败: %w", err)
		}
	}

	// 5. 打印AI思维链
	log.Printf("\n%s", strings.Repeat("-", 70))
	log.Println("💭 AI思维链分析:")
	log.Println(strings.Repeat("-", 70))
	log.Println(decision.CoTTrace)
	log.Printf("%s\n", strings.Repeat("-", 70))

	// 6. 打印AI决策
	log.Printf("📋 AI决策列表 (%d 个):\n", len(decision.Decisions))
	for i, d := range decision.Decisions {
		log.Printf("  [%d] %s: %s - %s", i+1, d.Symbol, d.Action, d.Reasoning)
		if d.Action == "open_long" || d.Action == "open_short" {
			log.Printf("      杠杆: %dx | 仓位: %.2f USDT | 止损: %.4f | 止盈: %.4f",
				d.Leverage, d.PositionSizeUSD, d.StopLoss, d.TakeProfit)
		}
	}
	log.Println()

	// 7. 对决策排序：确保先平仓后开仓（防止仓位叠加超限）
	sortedDecisions := sortDecisionsByPriority(decision.Decisions)

	log.Println("🔄 执行顺序（已优化）: 先平仓→后开仓")
	for i, d := range sortedDecisions {
		log.Printf("  [%d] %s %s", i+1, d.Symbol, d.Action)
	}
	log.Println()

	// 执行决策并记录结果
	for _, d := range sortedDecisions {
		actionRecord := logger.DecisionAction{
			Action:    d.Action,
			Symbol:    d.Symbol,
			Quantity:  0,
			Leverage:  d.Leverage,
			Price:     0,
			Timestamp: time.Now(),
			Success:   false,
		}

		if err := at.executeDecisionWithRecord(&d, &actionRecord); err != nil {
			log.Printf("❌ 执行决策失败 (%s %s): %v", d.Symbol, d.Action, err)
			actionRecord.Error = err.Error()
			record.ExecutionLog = append(record.ExecutionLog, fmt.Sprintf("❌ %s %s 失败: %v", d.Symbol, d.Action, err))
		} else {
			actionRecord.Success = true
			record.ExecutionLog = append(record.ExecutionLog, fmt.Sprintf("✓ %s %s 成功", d.Symbol, d.Action))
			// 成功执行后短暂延迟
			time.Sleep(1 * time.Second)
		}

		record.Decisions = append(record.Decisions, actionRecord)
	}

	// 8. 保存决策记录
	if err := at.decisionLogger.LogDecision(record); err != nil {
		log.Printf("⚠ 保存决策记录失败: %v", err)
	}

	return nil
}

// buildTradingContext 构建交易上下文
func (at *AutoTrader) buildTradingContext() (*decision.Context, error) {
	// 1. 获取账户信息
	balance, err := at.trader.GetBalance()
	if err != nil {
		return nil, fmt.Errorf("获取账户余额失败: %w", err)
	}

	// 获取账户字段
	totalWalletBalance := 0.0
	totalUnrealizedProfit := 0.0
	availableBalance := 0.0

	if wallet, ok := balance["totalWalletBalance"].(float64); ok {
		totalWalletBalance = wallet
	}
	if unrealized, ok := balance["totalUnrealizedProfit"].(float64); ok {
		totalUnrealizedProfit = unrealized
	}
	if avail, ok := balance["availableBalance"].(float64); ok {
		availableBalance = avail
	}

	// Total Equity = 钱包余额 + 未实现盈亏
	totalEquity := totalWalletBalance + totalUnrealizedProfit

	// 2. 获取持仓信息
	positions, err := at.trader.GetPositions()
	if err != nil {
		return nil, fmt.Errorf("获取持仓失败: %w", err)
	}

	var positionInfos []decision.PositionInfo
	totalMarginUsed := 0.0

	// 当前持仓的key集合（用于清理已平仓的记录）
	currentPositionKeys := make(map[string]bool)

	for _, pos := range positions {
		symbol := pos["symbol"].(string)
		side := pos["side"].(string)
		entryPrice := pos["entryPrice"].(float64)
		markPrice := pos["markPrice"].(float64)
		quantity := pos["positionAmt"].(float64)
		if quantity < 0 {
			quantity = -quantity // 空仓数量为负，转为正数
		}
		unrealizedPnl := pos["unRealizedProfit"].(float64)
		liquidationPrice := pos["liquidationPrice"].(float64)

		// 计算占用保证金（估算）
		leverage := 10 // 默认值，实际应该从持仓信息获取
		if lev, ok := pos["leverage"].(float64); ok {
			leverage = int(lev)
		}
		marginUsed := (quantity * markPrice) / float64(leverage)
		totalMarginUsed += marginUsed

		// 计算盈亏百分比
		pnlPct := 0.0
		if side == "long" {
			pnlPct = ((markPrice - entryPrice) / entryPrice) * float64(leverage) * 100
		} else {
			pnlPct = ((entryPrice - markPrice) / entryPrice) * float64(leverage) * 100
		}

		// 跟踪持仓
		posKey := symbol + "_" + side
		currentPositionKeys[posKey] = true
		
		// 优先使用API返回的updateTime值
		updateTime, ok := pos["updateTime"].(int64)
		if !ok || updateTime == 0 {
			// 如果API没有提供updateTime，使用当前时间
			updateTime = time.Now().UnixMilli()
		}
		
		// 检查是否有交易所有持仓但本地无记录的情况
		if _, exists := at.openTrades[posKey]; !exists {
			// 创建新的交易记录以同步交易所持仓
			now := time.Now()
			// 设置一个合理的OpenTime，使用updateTime或者当前时间
			openTime := now
			if updateTime > 0 {
				openTime = time.Unix(0, updateTime*int64(time.Millisecond))
			}
			
			// 创建本地交易记录
			tradeRecord := &LocalRecentTrade{
				Symbol:     symbol,
				Side:       strings.ToUpper(side),
				EntryPrice: entryPrice,
				ClosePrice: 0, // 未平仓
				Reason:     "系统同步持仓（程序重启或手动开仓）",
				CloseTime:  0,
			}
			
			// 创建交易包装器
			type tradeWrapper struct {
				RecentTrade *LocalRecentTrade
				OpenTime    time.Time
			}
			trade := tradeWrapper{
				RecentTrade: tradeRecord,
				OpenTime:    openTime,
			}
			
			// 添加到openTrades
			at.openTrades[posKey] = trade
			at.recentTrades[symbol] = tradeRecord
			
			log.Printf("  ⚠ 检测到交易所有持仓但本地无记录，已同步: %s %s | 入场%.4f", 
				symbol, side, entryPrice)
		}

		// 获取开仓理由（如果有未平仓交易记录）
		openingReason := ""
		if trade, exists := at.openTrades[posKey]; exists {
			// 类型断言获取嵌入的RecentTrade
	type tradeWrapper struct {
		RecentTrade *LocalRecentTrade
		OpenTime    time.Time
	}
	if tradeData, ok := trade.(tradeWrapper); ok && tradeData.RecentTrade != nil {
				openingReason = tradeData.RecentTrade.Reason
			}
		}

		// 确保使用正确的updateTime值
		positionInfo := decision.PositionInfo{
			Symbol:           symbol,
			Side:             side,
			EntryPrice:       entryPrice,
			MarkPrice:        markPrice,
			Quantity:         quantity,
			Leverage:         leverage,
			UnrealizedPnL:    unrealizedPnl,
			UnrealizedPnLPct: pnlPct,
			LiquidationPrice: liquidationPrice,
			MarginUsed:       marginUsed,
			UpdateTime:       updateTime,
			OpeningReason:    openingReason,
		}
		positionInfos = append(positionInfos, positionInfo)
	}

	// 清理已平仓的持仓记录并处理TP/SL自动平仓情况
	for key := range at.openTrades {
		if !currentPositionKeys[key] {
			// 持仓已不在交易所，但我们有本地记录，说明可能是TP/SL自动平仓
			trade, exists := at.openTrades[key]
			if exists {
				// 尝试获取交易详情
				type tradeWrapper struct {
					RecentTrade *LocalRecentTrade
					OpenTime    time.Time
				}
				if tradeData, ok := trade.(tradeWrapper); ok && tradeData.RecentTrade != nil {
					recentTrade := tradeData.RecentTrade
					symbol := recentTrade.Symbol
					side := recentTrade.Side
					openTime := tradeData.OpenTime
					
					// 获取当前市场价格作为平仓价格
					marketData, err := market.Get(symbol)
					if err == nil {
						// 计算持有时间
						now := time.Now()
						durationMs := now.UnixMilli() - openTime.UnixMilli()
						durationMin := durationMs / (1000 * 60) // 转换为分钟
						var durationStr string
						if durationMin < 60 {
							durationStr = fmt.Sprintf("%d分钟", durationMin)
						} else {
							durationHour := durationMin / 60
							durationMinRemainder := durationMin % 60
							durationStr = fmt.Sprintf("%d小时%d分钟", durationHour, durationMinRemainder)
						}
						
						// 更新交易记录
						recentTrade.CloseTime = now.UnixMilli()
						recentTrade.ClosePrice = marketData.CurrentPrice
						recentTrade.Duration = durationStr
						
						// 计算盈亏百分比
						if recentTrade.EntryPrice != 0 && math.Abs(marketData.CurrentPrice-recentTrade.EntryPrice) > 0.0001 {
							if side == "LONG" {
								recentTrade.PnLPct = ((marketData.CurrentPrice - recentTrade.EntryPrice) / recentTrade.EntryPrice) * 100
							} else {
								recentTrade.PnLPct = ((recentTrade.EntryPrice - marketData.CurrentPrice) / recentTrade.EntryPrice) * 100
							}
						} else {
							recentTrade.PnLPct = 0
						}
						
						// 如果没有平仓原因，设置为TP/SL自动平仓
						if recentTrade.Reason == "" {
							recentTrade.Reason = "止盈止损自动平仓"
						}
						
						log.Printf("  🔄 检测到TP/SL自动平仓: %s %s | 入场%.4f | 出场%.4f | 盈亏%+.2f%%", 
							symbol, side, recentTrade.EntryPrice, recentTrade.ClosePrice, recentTrade.PnLPct)
						
						// 保存更新后的交易记录
						if err := at.saveRecentTrades(); err != nil {
							log.Printf("  ⚠ 保存交易记录失败: %v", err)
						}
					}
				}
			}
			// 无论如何都从openTrades中删除
			delete(at.openTrades, key)
		}
	}

	// 3. 获取合并的候选币种池（AI500 + OI Top，去重）
	// 设置候选币种数量
	// 不再为任何模型设置数量限制
	ai500Limit := 20 // 所有模型使用相同的值

	// 获取合并后的币种池（AI500 + OI Top）
	mergedPool, err := pool.GetMergedCoinPool(ai500Limit)
	if err != nil {
		return nil, fmt.Errorf("获取合并币种池失败: %w", err)
	}

	// 构建候选币种列表（包含来源信息）
	var candidateCoins []decision.CandidateCoin
	for _, symbol := range mergedPool.AllSymbols {
		sources := mergedPool.SymbolSources[symbol]
		candidateCoins = append(candidateCoins, decision.CandidateCoin{
			Symbol:  symbol,
			Sources: sources, // "ai500" 和/或 "oi_top"
		})
	}

	log.Printf("📋 合并币种池: AI500前%d + OI_Top20 = 总计%d个候选币种",
		ai500Limit, len(candidateCoins))

	// 4. 计算总盈亏
	totalPnL := totalEquity - at.initialBalance
	totalPnLPct := 0.0
	if at.initialBalance > 0 {
		totalPnLPct = (totalPnL / at.initialBalance) * 100
	}

	marginUsedPct := 0.0
	if totalEquity > 0 {
		marginUsedPct = (totalMarginUsed / totalEquity) * 100
	}

	// 5. 分析历史表现（最近100个周期，避免长期持仓的交易记录丢失）
	// 假设每3分钟一个周期，100个周期 = 5小时，足够覆盖大部分交易
	performance, err := at.decisionLogger.AnalyzePerformance(100)
	if err != nil {
		log.Printf("⚠️  分析历史表现失败: %v", err)
		// 不影响主流程，继续执行（但设置performance为nil以避免传递错误数据）
		performance = nil
	}

	// 6. 构建上下文
	ctx := &decision.Context{
		CurrentTime:     time.Now().Format("2006-01-02 15:04:05"),
		RuntimeMinutes:  int(time.Since(at.startTime).Minutes()),
		CallCount:       at.callCount,
		BTCETHLeverage:  at.config.BTCETHLeverage,  // 使用配置的杠杆倍数
		AltcoinLeverage: at.config.AltcoinLeverage, // 使用配置的杠杆倍数
		PairLeverage:    at.config.PairLeverage,    // 使用配置的交易对特定杠杆
		Account: decision.AccountInfo{
			TotalEquity:      totalEquity,
			AvailableBalance: availableBalance,
			TotalPnL:         totalPnL,
			TotalPnLPct:      totalPnLPct,
			MarginUsed:       totalMarginUsed,
			MarginUsedPct:    marginUsedPct,
			PositionCount:    len(positionInfos),
		},
		Positions:      positionInfos,
		CandidateCoins: candidateCoins,
		RecentTrades:   make(map[string]*decision.RecentTrade), // 初始化最近交易记录map
		Performance:    performance, // 添加历史表现分析
	}

	// 将本地交易记录转换为decision包的格式
	for symbol, localTrade := range at.recentTrades {
		ctx.RecentTrades[symbol] = &decision.RecentTrade{
			Symbol:     localTrade.Symbol,
			Side:       localTrade.Side,
			EntryPrice: localTrade.EntryPrice,
			ClosePrice: localTrade.ClosePrice,
			Duration:   localTrade.Duration,
			PnLPct:     localTrade.PnLPct,
			Reason:     localTrade.Reason,
			CloseTime:  localTrade.CloseTime,
		}
	}
	


	return ctx, nil
}

// executeDecisionWithRecord 执行AI决策并记录详细信息
func (at *AutoTrader) executeDecisionWithRecord(decision *decision.Decision, actionRecord *logger.DecisionAction) error {
	switch decision.Action {
	case "open_long":
		return at.executeOpenLongWithRecord(decision, actionRecord)
	case "open_short":
		return at.executeOpenShortWithRecord(decision, actionRecord)
	case "close_long":
		return at.executeCloseLongWithRecord(decision, actionRecord)
	case "close_short":
		return at.executeCloseShortWithRecord(decision, actionRecord)
	case "hold", "wait":
		// 无需执行，仅记录
		return nil
	default:
		return fmt.Errorf("未知的action: %s", decision.Action)
	}
}

// executeOpenLongWithRecord 执行开多仓并记录详细信息
func (at *AutoTrader) executeOpenLongWithRecord(decision *decision.Decision, actionRecord *logger.DecisionAction) error {
	log.Printf("  📈 开多仓: %s", decision.Symbol)

	// ⚠️ 关键：检查是否已有同币种同方向持仓，如果有则拒绝开仓（防止仓位叠加超限）
	positions, err := at.trader.GetPositions()
	if err == nil {
		for _, pos := range positions {
			if pos["symbol"] == decision.Symbol && pos["side"] == "long" {
				return fmt.Errorf("❌ %s 已有多仓，拒绝开仓以防止仓位叠加超限。如需换仓，请先给出 close_long 决策", decision.Symbol)
			}
		}
	}

	// 获取当前价格
	marketData, err := market.Get(decision.Symbol)
	if err != nil {
		return err
	}

	// 计算数量
	quantity := decision.PositionSizeUSD / marketData.CurrentPrice
	actionRecord.Quantity = quantity
	actionRecord.Price = marketData.CurrentPrice

	// 开仓
	order, err := at.trader.OpenLong(decision.Symbol, quantity, decision.Leverage)
	if err != nil {
		return err
	}

	// 记录订单ID
	if orderID, ok := order["orderId"].(int64); ok {
		actionRecord.OrderID = orderID
	}

	log.Printf("  ✓ 开仓成功，订单ID: %v, 数量: %.4f", order["orderId"], quantity)

	// 创建并记录交易记录
	now := time.Now()
	// 创建交易记录
	tradeRecord := &LocalRecentTrade{
		Symbol:     decision.Symbol,
		Side:       "LONG",
		EntryPrice: marketData.CurrentPrice,
		ClosePrice: 0, // 未平仓，设置为0
		Reason:     decision.Reasoning,
		CloseTime:  now.UnixMilli(), // 临时值，平仓时更新
	}
	
	// 为内部使用添加OpenTime字段
	type tradeWrapper struct {
		RecentTrade *LocalRecentTrade
		OpenTime    time.Time
	}
	trade := tradeWrapper{
		RecentTrade: tradeRecord,
		OpenTime:    now,
	}

	// 存储到未平仓交易map中
	posKey := decision.Symbol + "_long"
	at.openTrades[posKey] = trade
	
	// 同时添加到recentTrades，这样可以立即保存
	at.recentTrades[decision.Symbol] = tradeRecord
	
	// 保存交易记录到文件
	if err := at.saveRecentTrades(); err != nil {
		log.Printf("  ⚠ 保存交易记录失败: %v", err)
	}

	// 设置止损止盈
	if err := at.trader.SetStopLoss(decision.Symbol, "LONG", quantity, decision.StopLoss); err != nil {
		log.Printf("  ⚠ 设置止损失败: %v", err)
	}
	if err := at.trader.SetTakeProfit(decision.Symbol, "LONG", quantity, decision.TakeProfit); err != nil {
		log.Printf("  ⚠ 设置止盈失败: %v", err)
	}

	return nil
}

// executeOpenShortWithRecord 执行开空仓并记录详细信息
func (at *AutoTrader) executeOpenShortWithRecord(decision *decision.Decision, actionRecord *logger.DecisionAction) error {
	log.Printf("  📉 开空仓: %s", decision.Symbol)

	// ⚠️ 关键：检查是否已有同币种同方向持仓，如果有则拒绝开仓（防止仓位叠加超限）
	positions, err := at.trader.GetPositions()
	if err == nil {
		for _, pos := range positions {
			if pos["symbol"] == decision.Symbol && pos["side"] == "short" {
				return fmt.Errorf("❌ %s 已有空仓，拒绝开仓以防止仓位叠加超限。如需换仓，请先给出 close_short 决策", decision.Symbol)
			}
		}
	}

	// 获取当前价格
	marketData, err := market.Get(decision.Symbol)
	if err != nil {
		return err
	}

	// 计算数量
	quantity := decision.PositionSizeUSD / marketData.CurrentPrice
	actionRecord.Quantity = quantity
	actionRecord.Price = marketData.CurrentPrice

	// 开仓
	order, err := at.trader.OpenShort(decision.Symbol, quantity, decision.Leverage)
	if err != nil {
		return err
	}

	// 记录订单ID
	if orderID, ok := order["orderId"].(int64); ok {
		actionRecord.OrderID = orderID
	}

	log.Printf("  ✓ 开仓成功，订单ID: %v, 数量: %.4f", order["orderId"], quantity)

	// 创建并记录交易记录
	now := time.Now()
	// 创建交易记录
	tradeRecord := &LocalRecentTrade{
		Symbol:     decision.Symbol,
		Side:       "SHORT",
		EntryPrice: marketData.CurrentPrice,
		ClosePrice: 0, // 未平仓，设置为0
		Reason:     decision.Reasoning,
		CloseTime:  now.UnixMilli(), // 临时值，平仓时更新
	}
	
	// 为内部使用添加OpenTime字段
	type tradeWrapper struct {
		RecentTrade *LocalRecentTrade
		OpenTime    time.Time
	}
	trade := tradeWrapper{
		RecentTrade: tradeRecord,
		OpenTime:    now,
	}

	// 存储到未平仓交易map中
	posKey := decision.Symbol + "_short"
	at.openTrades[posKey] = trade
	
	// 同时添加到recentTrades，这样可以立即保存
	at.recentTrades[decision.Symbol] = tradeRecord
	
	// 保存交易记录到文件
	if err := at.saveRecentTrades(); err != nil {
		log.Printf("  ⚠ 保存交易记录失败: %v", err)
	}

	// 设置止损止盈
	if err := at.trader.SetStopLoss(decision.Symbol, "SHORT", quantity, decision.StopLoss); err != nil {
		log.Printf("  ⚠ 设置止损失败: %v", err)
	}
	if err := at.trader.SetTakeProfit(decision.Symbol, "SHORT", quantity, decision.TakeProfit); err != nil {
		log.Printf("  ⚠ 设置止盈失败: %v", err)
	}

	return nil
}

// executeCloseLongWithRecord 执行平多仓并记录详细信息
func (at *AutoTrader) executeCloseLongWithRecord(decision *decision.Decision, actionRecord *logger.DecisionAction) error {
	log.Printf("  🔄 平多仓: %s", decision.Symbol)

	// 获取当前价格
	marketData, err := market.Get(decision.Symbol)
	if err != nil {
		return err
	}
	actionRecord.Price = marketData.CurrentPrice

	// 从未平仓交易map中获取交易记录
	posKey := decision.Symbol + "_LONG"
	trade, exists := at.openTrades[posKey]
	if !exists {
		return fmt.Errorf("❌ 未找到 %s 的未平仓记录", decision.Symbol)
	}
	
	// 使用更明确的类型断言方式
	type tradeWrapper struct {
		RecentTrade *LocalRecentTrade
		OpenTime    time.Time
	}
	tradeData, ok := trade.(tradeWrapper)
	if !ok {
		return fmt.Errorf("❌ 交易记录格式错误: %s", decision.Symbol)
	}
	recentTrade := tradeData.RecentTrade
	openTime := tradeData.OpenTime
	
	// 计算持有时间
	now := time.Now()
	durationMs := now.UnixMilli() - openTime.UnixMilli()
	durationMin := durationMs / (1000 * 60) // 转换为分钟
	var durationStr string
	if durationMin < 60 {
		durationStr = fmt.Sprintf("%d分钟", durationMin)
	} else {
		durationHour := durationMin / 60
		durationMinRemainder := durationMin % 60
		durationStr = fmt.Sprintf("%d小时%d分钟", durationHour, durationMinRemainder)
	}
	
	// 即使无法获取持仓详情，也要创建交易记录
	if durationStr == "" {
		durationStr = "未知"
	}
	
	// 如果没有入场价格，使用当前价格作为默认值
	if recentTrade.EntryPrice == 0 {
		recentTrade.EntryPrice = marketData.CurrentPrice
		log.Printf("  ⚠ 无法获取%s的入场价格，使用当前价格作为默认值", decision.Symbol)
		// 当使用默认价格时，修改Reason以避免显示不一致的盈亏信息
		modifiedReason := decision.Reasoning
		// 使用正则表达式移除任何格式的盈亏百分比信息
		profitRegex := regexp.MustCompile(`[亏损盈利][+-]?\d+(\.\d+)?%`)
		modifiedReason = profitRegex.ReplaceAllString(modifiedReason, "止损控制风险")
		decision.Reasoning = modifiedReason
	}
	
	// 计算盈亏百分比将在平仓成功后更新
	
	// 平仓
	order, err := at.trader.CloseLong(decision.Symbol, 0) // 0 = 全部平仓
	if err != nil {
		return err
	}

	// 记录订单ID
	if orderID, ok := order["orderId"].(int64); ok {
		actionRecord.OrderID = orderID
	}
	
	// 尝试获取实际成交价格
	actualClosePrice := marketData.CurrentPrice
	if avgPrice, ok := order["avgPrice"].(string); ok {
		if price, err := strconv.ParseFloat(avgPrice, 64); err == nil && price > 0 {
			actualClosePrice = price
		}
	} else if avgPrice, ok := order["avgPrice"].(float64); ok && avgPrice > 0 {
		actualClosePrice = avgPrice
	}

	// 移除对原始trade变量的直接访问

	// 计算盈亏百分比
	if recentTrade.EntryPrice != 0 && math.Abs(actualClosePrice-recentTrade.EntryPrice) > 0.0001 {
		recentTrade.PnLPct = ((actualClosePrice - recentTrade.EntryPrice) / recentTrade.EntryPrice) * 100
	} else {
		recentTrade.PnLPct = 0
	}

	// 移除重复的字段更新代码（已在前面更新过）

	// 保存交易记录到文件
	if err := at.saveRecentTrades(); err != nil {
		log.Printf("  ⚠ 保存交易记录失败: %v", err)
	}

	log.Printf("  ✅ 已更新交易记录: %s LONG | 入场%.4f | 出场%.4f | 盈亏%+.2f%%", 
		decision.Symbol, recentTrade.EntryPrice, actualClosePrice, recentTrade.PnLPct)
	

	// 清理已平仓的记录
	delete(at.openTrades, posKey)

	log.Printf("  ✓ 平仓成功")
	return nil
}

// executeCloseShortWithRecord 执行平空仓并记录详细信息
func (at *AutoTrader) executeCloseShortWithRecord(decision *decision.Decision, actionRecord *logger.DecisionAction) error {
	log.Printf("  🔄 平空仓: %s", decision.Symbol)

	// 获取当前价格
	marketData, err := market.Get(decision.Symbol)
	if err != nil {
		return err
	}
	actionRecord.Price = marketData.CurrentPrice

	// 从未平仓交易map中获取交易记录
	posKey := decision.Symbol + "_SHORT"
	trade, exists := at.openTrades[posKey]
	if !exists {
		return fmt.Errorf("❌ 未找到 %s 的未平仓记录", decision.Symbol)
	}
	
	// 使用更明确的类型断言方式
	type tradeWrapper struct {
		RecentTrade *LocalRecentTrade
		OpenTime    time.Time
	}
	tradeData, ok := trade.(tradeWrapper)
	if !ok {
		return fmt.Errorf("❌ 交易记录格式错误: %s", decision.Symbol)
	}
	recentTrade := tradeData.RecentTrade
	openTime := tradeData.OpenTime
	
	// 计算持有时间
	now := time.Now()
	durationMs := now.UnixMilli() - openTime.UnixMilli()
	durationMin := durationMs / (1000 * 60) // 转换为分钟
	var durationStr string
	if durationMin < 60 {
		durationStr = fmt.Sprintf("%d分钟", durationMin)
	} else {
		durationHour := durationMin / 60
		durationMinRemainder := durationMin % 60
		durationStr = fmt.Sprintf("%d小时%d分钟", durationHour, durationMinRemainder)
	}
	
	// 即使无法获取持仓详情，也要创建交易记录
	if durationStr == "" {
		durationStr = "未知"
	}
	
	// 如果没有入场价格，使用当前价格作为默认值
	if recentTrade.EntryPrice == 0 {
		recentTrade.EntryPrice = marketData.CurrentPrice
		log.Printf("  ⚠ 无法获取%s的入场价格，使用当前价格作为默认值", decision.Symbol)
		// 当使用默认价格时，修改Reason以避免显示不一致的盈亏信息
		modifiedReason := decision.Reasoning
		// 使用正则表达式移除任何格式的盈亏百分比信息
		profitRegex := regexp.MustCompile(`[亏损盈利][+-]?\d+(\.\d+)?%`)
		modifiedReason = profitRegex.ReplaceAllString(modifiedReason, "止损控制风险")
		decision.Reasoning = modifiedReason
	}
	
	// 计算盈亏百分比将在平仓成功后更新
	
	// 平仓
	order, err := at.trader.CloseShort(decision.Symbol, 0) // 0 = 全部平仓
	if err != nil {
		return err
	}

	// 记录订单ID
	if orderID, ok := order["orderId"].(int64); ok {
		actionRecord.OrderID = orderID
	}
	
	// 尝试获取实际成交价格
	actualClosePrice := marketData.CurrentPrice
	if avgPrice, ok := order["avgPrice"].(string); ok {
		if price, err := strconv.ParseFloat(avgPrice, 64); err == nil && price > 0 {
			actualClosePrice = price
		}
	} else if avgPrice, ok := order["avgPrice"].(float64); ok && avgPrice > 0 {
		actualClosePrice = avgPrice
	}

	// 移除对原始trade变量的直接访问

	// 计算盈亏百分比
	if recentTrade.EntryPrice != 0 && math.Abs(actualClosePrice-recentTrade.EntryPrice) > 0.0001 {
		recentTrade.PnLPct = ((recentTrade.EntryPrice - actualClosePrice) / recentTrade.EntryPrice) * 100
	} else {
		recentTrade.PnLPct = 0
	}

	// 更新交易记录
	recentTrade.CloseTime = time.Now().UnixMilli()
	recentTrade.ClosePrice = actualClosePrice
	recentTrade.Reason = decision.Reasoning
	recentTrade.Duration = durationStr

	// 保存交易记录到文件
	if err := at.saveRecentTrades(); err != nil {
		log.Printf("  ⚠ 保存交易记录失败: %v", err)
	}

	log.Printf("  ✅ 已更新交易记录: %s SHORT | 入场%.4f | 出场%.4f | 盈亏%+.2f%%", 
		decision.Symbol, recentTrade.EntryPrice, actualClosePrice, recentTrade.PnLPct)
	

	// 清理已平仓的记录
	delete(at.openTrades, posKey)

	log.Printf("  ✓ 平仓成功")
	return nil
}

// GetID 获取trader ID
func (at *AutoTrader) GetID() string {
	return at.id
}

// GetName 获取trader名称
func (at *AutoTrader) GetName() string {
	return at.name
}

// GetAIModel 获取AI模型
func (at *AutoTrader) GetAIModel() string {
	return at.aiModel
}

// GetDecisionLogger 获取决策日志记录器
func (at *AutoTrader) GetDecisionLogger() *logger.DecisionLogger {
	return at.decisionLogger
}

// saveRecentTrades 保存交易记录到文件
func (at *AutoTrader) saveRecentTrades() error {
	// 确保目录存在
	dir := "data"
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("创建数据目录失败: %w", err)
	}
	
	// 保存到文件
	filePath := fmt.Sprintf("%s/recent-trades.json", dir)
	file, err := os.Create(filePath)
	if err != nil {
		return fmt.Errorf("创建文件失败: %w", err)
	}
	defer file.Close()
	
	// 序列化到JSON
	encode := json.NewEncoder(file)
	encode.SetIndent("", "  ")
	if err := encode.Encode(at.recentTrades); err != nil {
		return fmt.Errorf("序列化交易记录失败: %w", err)
	}
	
	log.Printf("  ✅ 已保存 %d 条交易记录到 %s", len(at.recentTrades), filePath)
	return nil
}

// loadRecentTrades 从文件加载最近交易记录
func (at *AutoTrader) loadRecentTrades() error {
	filePath := fmt.Sprintf("data/%s/recent_trades.json", at.id)
	
	// 检查文件是否存在
	if _, err := os.Stat(filePath); os.IsNotExist(err) {
		return fmt.Errorf("交易记录文件不存在: %s", filePath)
	}
	
	// 打开文件
	file, err := os.Open(filePath)
	if err != nil {
		return fmt.Errorf("打开文件失败: %w", err)
	}
	defer file.Close()
	
	// 反序列化
	decode := json.NewDecoder(file)
	if err := decode.Decode(&at.recentTrades); err != nil {
		return fmt.Errorf("反序列化交易记录失败: %w", err)
	}
	
	log.Printf("  ✅ 已加载 %d 条最近交易记录", len(at.recentTrades))
	return nil
}

// GetStatus 获取系统状态（用于API）
func (at *AutoTrader) GetStatus() map[string]interface{} {
	var aiProvider string
	switch at.config.AIModel {
	case "qwen":
		aiProvider = "Qwen"
		// Grok实现已移除
	case "deepseek":
		fallthrough
	default:
		aiProvider = "DeepSeek"
	}

	return map[string]interface{}{
		"trader_id":       at.id,
		"trader_name":     at.name,
		"ai_model":        at.aiModel,
		"exchange":        at.exchange,
		"is_running":      at.isRunning,
		"start_time":      at.startTime.Format(time.RFC3339),
		"runtime_minutes": int(time.Since(at.startTime).Minutes()),
		"call_count":      at.callCount,
		"initial_balance": at.initialBalance,
		"scan_interval":   at.config.ScanInterval.String(),
		"stop_until":      at.stopUntil.Format(time.RFC3339),
		"last_reset_time": at.lastResetTime.Format(time.RFC3339),
		"ai_provider":     aiProvider,
	}
}

// GetAccountInfo 获取账户信息（用于API）
func (at *AutoTrader) GetAccountInfo() (map[string]interface{}, error) {
	balance, err := at.trader.GetBalance()
	if err != nil {
		return nil, fmt.Errorf("获取余额失败: %w", err)
	}

	// 获取账户字段
	totalWalletBalance := 0.0
	totalUnrealizedProfit := 0.0
	availableBalance := 0.0

	if wallet, ok := balance["totalWalletBalance"].(float64); ok {
		totalWalletBalance = wallet
	}
	if unrealized, ok := balance["totalUnrealizedProfit"].(float64); ok {
		totalUnrealizedProfit = unrealized
	}
	if avail, ok := balance["availableBalance"].(float64); ok {
		availableBalance = avail
	}

	// Total Equity = 钱包余额 + 未实现盈亏
	totalEquity := totalWalletBalance + totalUnrealizedProfit

	// 获取持仓计算总保证金
	positions, err := at.trader.GetPositions()
	if err != nil {
		return nil, fmt.Errorf("获取持仓失败: %w", err)
	}

	totalMarginUsed := 0.0
	totalUnrealizedPnL := 0.0
	for _, pos := range positions {
		markPrice := pos["markPrice"].(float64)
		quantity := pos["positionAmt"].(float64)
		if quantity < 0 {
			quantity = -quantity
		}
		unrealizedPnl := pos["unRealizedProfit"].(float64)
		totalUnrealizedPnL += unrealizedPnl

		leverage := 10
		if lev, ok := pos["leverage"].(float64); ok {
			leverage = int(lev)
		}
		marginUsed := (quantity * markPrice) / float64(leverage)
		totalMarginUsed += marginUsed
	}

	totalPnL := totalEquity - at.initialBalance
	totalPnLPct := 0.0
	if at.initialBalance > 0 {
		totalPnLPct = (totalPnL / at.initialBalance) * 100
	}

	marginUsedPct := 0.0
	if totalEquity > 0 {
		marginUsedPct = (totalMarginUsed / totalEquity) * 100
	}

	return map[string]interface{}{
		// 核心字段
		"total_equity":      totalEquity,           // 账户净值 = wallet + unrealized
		"wallet_balance":    totalWalletBalance,    // 钱包余额（不含未实现盈亏）
		"unrealized_profit": totalUnrealizedProfit, // 未实现盈亏（从API）
		"available_balance": availableBalance,      // 可用余额

		// 盈亏统计
		"total_pnl":            totalPnL,           // 总盈亏 = equity - initial
		"total_pnl_pct":        totalPnLPct,        // 总盈亏百分比
		"total_unrealized_pnl": totalUnrealizedPnL, // 未实现盈亏（从持仓计算）
		"initial_balance":      at.initialBalance,  // 初始余额
		"daily_pnl":            at.dailyPnL,        // 日盈亏

		// 持仓信息
		"position_count":  len(positions),  // 持仓数量
		"margin_used":     totalMarginUsed, // 保证金占用
		"margin_used_pct": marginUsedPct,   // 保证金使用率
	}, nil
}

// GetPositions 获取持仓列表（用于API）
func (at *AutoTrader) GetPositions() ([]map[string]interface{}, error) {
	positions, err := at.trader.GetPositions()
	if err != nil {
		return nil, fmt.Errorf("获取持仓失败: %w", err)
	}

	var result []map[string]interface{}
	for _, pos := range positions {
		symbol := pos["symbol"].(string)
		side := pos["side"].(string)
		entryPrice := pos["entryPrice"].(float64)
		markPrice := pos["markPrice"].(float64)
		quantity := pos["positionAmt"].(float64)
		if quantity < 0 {
			quantity = -quantity
		}
		unrealizedPnl := pos["unRealizedProfit"].(float64)
		liquidationPrice := pos["liquidationPrice"].(float64)

		leverage := 10
		if lev, ok := pos["leverage"].(float64); ok {
			leverage = int(lev)
		}

		pnlPct := 0.0
		if side == "long" {
			pnlPct = ((markPrice - entryPrice) / entryPrice) * float64(leverage) * 100
		} else {
			pnlPct = ((entryPrice - markPrice) / entryPrice) * float64(leverage) * 100
		}

		marginUsed := (quantity * markPrice) / float64(leverage)

		result = append(result, map[string]interface{}{
			"symbol":             symbol,
			"side":               side,
			"entry_price":        entryPrice,
			"mark_price":         markPrice,
			"quantity":           quantity,
			"leverage":           leverage,
			"unrealized_pnl":     unrealizedPnl,
			"unrealized_pnl_pct": pnlPct,
			"liquidation_price":  liquidationPrice,
			"margin_used":        marginUsed,
		})
	}

	return result, nil
}

// sortDecisionsByPriority 对决策排序：先平仓，再开仓，最后hold/wait
// 这样可以避免换仓时仓位叠加超限
func sortDecisionsByPriority(decisions []decision.Decision) []decision.Decision {
	if len(decisions) <= 1 {
		return decisions
	}

	// 定义优先级
	getActionPriority := func(action string) int {
		switch action {
		case "close_long", "close_short":
			return 1 // 最高优先级：先平仓
		case "open_long", "open_short":
			return 2 // 次优先级：后开仓
		case "hold", "wait":
			return 3 // 最低优先级：观望
		default:
			return 999 // 未知动作放最后
		}
	}

	// 复制决策列表
	sorted := make([]decision.Decision, len(decisions))
	copy(sorted, decisions)

	// 按优先级排序
	for i := 0; i < len(sorted)-1; i++ {
		for j := i + 1; j < len(sorted); j++ {
			if getActionPriority(sorted[i].Action) > getActionPriority(sorted[j].Action) {
				sorted[i], sorted[j] = sorted[j], sorted[i]
			}
		}
	}

	return sorted
}
