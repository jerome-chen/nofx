package decision

import (
	"encoding/json"
	"fmt"
	"log"
	"nofx/market"
	"nofx/mcp"
	"nofx/pool"
	"strings"
	"time"
)

// PositionInfo 持仓信息
type PositionInfo struct {
	Symbol           string  `json:"symbol"`
	Side             string  `json:"side"` // "long" or "short"
	EntryPrice       float64 `json:"entry_price"`
	MarkPrice        float64 `json:"mark_price"`
	Quantity         float64 `json:"quantity"`
	Leverage         int     `json:"leverage"`
	UnrealizedPnL    float64 `json:"unrealized_pnl"`
	UnrealizedPnLPct float64 `json:"unrealized_pnl_pct"`
	LiquidationPrice float64 `json:"liquidation_price"`
	MarginUsed       float64 `json:"margin_used"`
	UpdateTime       int64   `json:"update_time"` // 持仓更新时间戳（毫秒）
	OpeningReason    string  `json:"opening_reason"` // 开仓理由
}

// AccountInfo 账户信息
type AccountInfo struct {
	TotalEquity      float64 `json:"total_equity"`      // 账户净值
	AvailableBalance float64 `json:"available_balance"` // 可用余额
	TotalPnL         float64 `json:"total_pnl"`         // 总盈亏
	TotalPnLPct      float64 `json:"total_pnl_pct"`     // 总盈亏百分比
	MarginUsed       float64 `json:"margin_used"`       // 已用保证金
	MarginUsedPct    float64 `json:"margin_used_pct"`   // 保证金使用率
	PositionCount    int     `json:"position_count"`    // 持仓数量
}

// CandidateCoin 候选币种（来自币种池）
type CandidateCoin struct {
	Symbol  string   `json:"symbol"`
	Sources []string `json:"sources"` // 来源: "ai500" 和/或 "oi_top"
}

// OITopData 持仓量增长Top数据（用于AI决策参考）
type OITopData struct {
	Rank              int     // OI Top排名
	OIDeltaPercent    float64 // 持仓量变化百分比（1小时）
	OIDeltaValue      float64 // 持仓量变化价值
	PriceDeltaPercent float64 // 价格变化百分比
	NetLong           float64 // 净多仓
	NetShort          float64 // 净空仓
}

// Context 交易上下文（传递给AI的完整信息）
type Context struct {
	CurrentTime     string                  `json:"current_time"`
	RuntimeMinutes  int                     `json:"runtime_minutes"`
	CallCount       int                     `json:"call_count"`
	Account         AccountInfo             `json:"account"`
	Positions       []PositionInfo          `json:"positions"`
	CandidateCoins  []CandidateCoin         `json:"candidate_coins"`
	MarketDataMap   map[string]*market.Data `json:"-"` // 不序列化，但内部使用
	OITopDataMap    map[string]*OITopData   `json:"-"` // OI Top数据映射
	Performance     interface{}             `json:"-"` // 历史表现分析（logger.PerformanceAnalysis）
	BTCETHLeverage  int                     `json:"-"` // BTC/ETH杠杆倍数（从配置读取）
	AltcoinLeverage int                     `json:"-"` // 山寨币杠杆倍数（从配置读取）
	PairLeverage    map[string]int          `json:"-"` // 特定交易对的杠杆倍数
}

// Decision AI的交易决策
type Decision struct {
	Symbol          string  `json:"symbol"`
	Action          string  `json:"action"` // "open_long", "open_short", "close_long", "close_short", "hold", "wait"
	Leverage        int     `json:"leverage,omitempty"`
	PositionSizeUSD float64 `json:"position_size_usd,omitempty"`
	StopLoss        float64 `json:"stop_loss,omitempty"`
	TakeProfit      float64 `json:"take_profit,omitempty"`
	Confidence      int     `json:"confidence,omitempty"` // 信心度 (0-100)
	RiskUSD         float64 `json:"risk_usd,omitempty"`   // 最大美元风险
	Reasoning       string  `json:"reasoning"`
}

// FullDecision AI的完整决策（包含思维链）
type FullDecision struct {
	UserPrompt string     `json:"user_prompt"` // 发送给AI的输入prompt
	CoTTrace   string     `json:"cot_trace"`   // 思维链分析（AI输出）
	Decisions  []Decision `json:"decisions"`   // 具体决策列表
	Timestamp  time.Time  `json:"timestamp"`
}

// GetFullDecision 获取AI的完整交易决策（批量分析所有币种和持仓）
func GetFullDecision(ctx *Context, mcpClient *mcp.Client) (*FullDecision, error) {
	// 1. 为所有币种获取市场数据
	if err := fetchMarketDataForContext(ctx); err != nil {
		return nil, fmt.Errorf("获取市场数据失败: %w", err)
	}

	// 2. 构建 System Prompt（固定规则）和 User Prompt（动态数据）
	systemPrompt := buildSystemPrompt(ctx.Account.TotalEquity, ctx.BTCETHLeverage, ctx.AltcoinLeverage, ctx.PairLeverage)
	userPrompt := buildUserPrompt(ctx)

	// 3. 调用AI API（使用 system + user prompt）
	aiResponse, err := mcpClient.CallWithMessages(systemPrompt, userPrompt)
	if err != nil {
		return nil, fmt.Errorf("调用AI API失败: %w", err)
	}

	// 4. 解析AI响应
	decision, err := parseFullDecisionResponse(aiResponse, ctx.Account.TotalEquity, ctx.BTCETHLeverage, ctx.AltcoinLeverage, ctx.PairLeverage)
	if err != nil {
		return nil, fmt.Errorf("解析AI响应失败: %w", err)
	}

	decision.Timestamp = time.Now()
	decision.UserPrompt = userPrompt // 保存输入prompt
	return decision, nil
}

// fetchMarketDataForContext 为上下文中的所有币种获取市场数据和OI数据
func fetchMarketDataForContext(ctx *Context) error {
	ctx.MarketDataMap = make(map[string]*market.Data)
	ctx.OITopDataMap = make(map[string]*OITopData)

	// 收集所有需要获取数据的币种
	symbolSet := make(map[string]bool)

	// 1. 优先获取持仓币种的数据（这是必须的）
	for _, pos := range ctx.Positions {
		symbolSet[pos.Symbol] = true
	}

	// 2. 候选币种数量根据账户状态动态调整
	maxCandidates := calculateMaxCandidates(ctx)
	for i, coin := range ctx.CandidateCoins {
		if i >= maxCandidates {
			break
		}
		symbolSet[coin.Symbol] = true
	}

	// 并发获取市场数据
	// 持仓币种集合（用于判断是否跳过OI检查）
	positionSymbols := make(map[string]bool)
	for _, pos := range ctx.Positions {
		positionSymbols[pos.Symbol] = true
	}

	for symbol := range symbolSet {
		data, err := market.Get(symbol)
		if err != nil {
			// 单个币种失败不影响整体，只记录错误
			continue
		}

		// ⚠️ 流动性过滤：持仓价值低于15M USD的币种不做（多空都不做）
		// 持仓价值 = 持仓量 × 当前价格
		// 但现有持仓必须保留（需要决策是否平仓）
		isExistingPosition := positionSymbols[symbol]
		if !isExistingPosition && data.OpenInterest != nil && data.CurrentPrice > 0 {
			// 计算持仓价值（USD）= 持仓量 × 当前价格
			oiValue := data.OpenInterest.Latest * data.CurrentPrice
			oiValueInMillions := oiValue / 1_000_000 // 转换为百万美元单位
			if oiValueInMillions < 5 {
				log.Printf("⚠️  %s 持仓价值过低(%.2fM USD < 5M)，跳过此币种 [持仓量:%.0f × 价格:%.4f]",
					symbol, oiValueInMillions, data.OpenInterest.Latest, data.CurrentPrice)
				continue
			}
		}

		ctx.MarketDataMap[symbol] = data
	}

	// 加载OI Top数据（不影响主流程）
	oiPositions, err := pool.GetOITopPositions()
	if err == nil {
		for _, pos := range oiPositions {
			// 标准化符号匹配
			symbol := pos.Symbol
			ctx.OITopDataMap[symbol] = &OITopData{
				Rank:              pos.Rank,
				OIDeltaPercent:    pos.OIDeltaPercent,
				OIDeltaValue:      pos.OIDeltaValue,
				PriceDeltaPercent: pos.PriceDeltaPercent,
				NetLong:           pos.NetLong,
				NetShort:          pos.NetShort,
			}
		}
	}

	return nil
}

// calculateMaxCandidates 根据账户状态计算需要分析的候选币种数量
func calculateMaxCandidates(ctx *Context) int {
	// 直接返回候选池的全部币种数量
	// 因为候选池已经在 auto_trader.go 中筛选过了
	// 固定分析前20个评分最高的币种（来自AI500）
	return len(ctx.CandidateCoins)
}

// buildSystemPrompt 构建 System Prompt（固定规则，可缓存）
func buildSystemPrompt(accountEquity float64, btcEthLeverage, altcoinLeverage int, pairLeverage map[string]int) string {
	var sb strings.Builder

	// 核心使命
	sb.WriteString("你是专业的加密货币交易AI，在币安合约市场进行自主交易。\n")
	sb.WriteString("本系统以 **最大化夏普比率（Sharpe Ratio）** 为唯一核心目标。\n\n")
	sb.WriteString("---\n\n")

	// 🎯 核心目标
	sb.WriteString("# 🎯 核心目标\n\n")
	sb.WriteString("**夏普比率 = 平均收益 / 收益波动率**\n")
	sb.WriteString("目标：高质量、稳定、低波动的可持续收益。\n")
	sb.WriteString("你不是高频交易者，而是风险控制的量化交易员。\n\n")
	sb.WriteString("提升夏普的核心：\n\n")

	sb.WriteString("* ✅ 只执行高确定性机会（信心度 ≥ 75）\n\n")

	sb.WriteString("* ✅ 保持持仓耐心（30 – 60 分钟 +）\n\n")

	sb.WriteString("* ✅ 控制风险与回撤，追求稳定增长\n\n")

	sb.WriteString("* ❌ 过度交易、提前平仓、小盈小亏\n\n")
	sb.WriteString("系统每 3 分钟扫描市场，但多数周期应输出 `wait` 或 `hold`。\n\n")
	sb.WriteString("---\n\n")

	// ⚖️ 硬约束（风险控制）
	sb.WriteString("# ⚖️ 硬约束（风险控制）\n\n")
	sb.WriteString("1. **风险回报比** ≥ 1 : 3\n")
	sb.WriteString("2. **最多持仓** 3 个币种\n")
	sb.WriteString("3. **单币仓位**\n\n")

	sb.WriteString(fmt.Sprintf("    * 山寨：%.0f – %.0f U (%dx)\n", accountEquity*0.8, accountEquity*1.5, altcoinLeverage))
	sb.WriteString(fmt.Sprintf("    * BTC/ETH：%.0f – %.0f U (%dx)\n", accountEquity*5, accountEquity*10, btcEthLeverage))

	// 添加交易对特定杠杆限制
	if len(pairLeverage) > 0 {
		sb.WriteString("    * 特定交易对限制：\n")
		for pair, leverage := range pairLeverage {
			sb.WriteString(fmt.Sprintf("      - %s：%.0f – %.0f U (%dx)\n", pair, accountEquity*0.8, accountEquity*1.5, leverage))
		}
	}

	sb.WriteString("4. **总保证金使用率** ≤ 90 %\n")
	sb.WriteString("5. **单笔风险敞口** ≤ 账户净值 3 %\n")
	sb.WriteString("6. **强平价** 距离 ≥ 15 %（防止过高杠杆）\n\n")
	sb.WriteString("---\n\n")

	// 🧭 市场结构与趋势识别
	sb.WriteString("# 🧭 市场结构与趋势识别\n\n")
	sb.WriteString("多维分析 → 趋势 + 结构 + 形态：\n\n")

	sb.WriteString("| 模块             | 说明                                                   | \n")
	sb.WriteString("| -------------- | ---------------------------------------------------- | \n")
	sb.WriteString("| **趋势方向**       | 结合 EMA 20、MACD 判断：价格 > EMA → 上升趋势； 价格 < EMA → 下降趋势   | \n")
	sb.WriteString("| **市场结构 (MS)**  | 利用 4 小时 K 线 识别 HH / HL / LL / LH 结构；确认 MS Break 后再反转 | \n")
	sb.WriteString("| **支撑阻力 (S/R)** | 从 4H / 1D 关键 Swing 高低点 绘制，分 强度 1–3 级                 | \n")
	sb.WriteString("| **图形识别**       | 可识别 双顶/双底、头肩/倒头肩、楔形、三角形、旗形等；趋势延续或反转信号                | \n")
	sb.WriteString("| **量与持仓量**      | 成交量 + OI 同向 = 趋势强化；反向 = 趋势衰减                         | \n")
	sb.WriteString("| **资金费率**       | 极端正 → 多头拥挤，易回调；极端负 → 空头拥挤，易反弹                        | \n\n")

	sb.WriteString("只有当 趋势、结构、成交量、形态 四维一致时才开仓。\n\n")
	sb.WriteString("---\n\n")

	// ⏱️ 交易节奏
	sb.WriteString("# ⏱️ 交易节奏\n\n")
	sb.WriteString("* 优秀节奏：每天 2 – 4 笔 （每小时 0.1 – 0.2 笔）\n")
	sb.WriteString("* 每小时 > 2 笔 = 过度交易\n")
	sb.WriteString("* 持仓 ≥ 30 分钟； < 15 分钟 平仓 = 信号噪音或过度紧张\n\n")
	sb.WriteString("---\n\n")

	// 🧮 资金与仓位控制
	sb.WriteString("# 🧮 资金与仓位控制\n\n")
	sb.WriteString("**Position Size (USD)** = 可用资金 × 杠杆 × 仓位比例\n")
	sb.WriteString("⚠️ 重要杠杆限制：\n")
	sb.WriteString(fmt.Sprintf("* BTC/ETH 最大杠杆: %d倍\n", btcEthLeverage))
	sb.WriteString(fmt.Sprintf("* 山寨币最大杠杆: %d倍\n\n", altcoinLeverage))
		if len(pairLeverage) > 0 {
		sb.WriteString("    * 特定交易对限制：\n")
		for pair, leverage := range pairLeverage {
			sb.WriteString(fmt.Sprintf("      - %s交易对：最大杠杆：%d倍\n", pair, leverage))
		}
	}

	sb.WriteString("按信心度动态调整杠杆（但不能超过最大杠杆限制）：\n\n")
	sb.WriteString("| 信心度         | 杠杆范围      | 仓位比例（账户净值） | \n")
	sb.WriteString("| ----------- | --------- | ---------- | \n")
	sb.WriteString("| 0.60 – 0.75 | 3 – 5 x   | 10 % 以内    | \n")
	sb.WriteString("| 0.75 – 0.85 | 5 – 10 x  | 20 % 以内    | \n")
	sb.WriteString("| 0.85 – 1.00 | 10 – 20 x | 30 % 以内    | \n\n")
	sb.WriteString("**风险计算：**\n")
	sb.WriteString("`risk_usd = |entry – stop_loss| × position_size / entry`\n")
	sb.WriteString("单笔 risk_usd ≤ 账户 3 %。\n\n")
	sb.WriteString("---\n\n")

	// 🧠 夏普比率 自我调节
	sb.WriteString("# 🧠 夏普比率 自我调节\n\n")
	sb.WriteString("| 夏普区间     | 策略调整                   | \n")
	sb.WriteString("| -------- | ---------------------- | \n")
	sb.WriteString("| < −0.5   | 停止交易 ≥ 6 周期 ，反思信号质量与频率 | \n")
	sb.WriteString("| −0.5 ~ 0 | 仅执行 信心度 > 80 信号 ，降低频率  | \n")
	sb.WriteString("| 0 ~ 0.7  | 维持策略                   | \n")
	sb.WriteString("| > 0.7    | 可适度扩大仓位 (+20 %)        | \n\n")
	sb.WriteString("---\n\n")

	// 🧩 决策流程
	sb.WriteString("# 🧩 决策流程\n\n")
	sb.WriteString("1. **分析夏普比率**：评估策略状态\n")
	sb.WriteString("2. **评估持仓**：是否符合趋势与结构 ？\n")
	sb.WriteString("3. **寻找机会**：趋势、结构、形态 共振 ≥ 75 分 ？\n")
	sb.WriteString("4. **生成决策**：输出 思维链 + JSON 决策数组\n\n")
	sb.WriteString("---\n\n")

	// 📤 输出格式
	sb.WriteString("# 📤 输出格式\n\n")
	sb.WriteString("**⚠️ 严格要求：必须按以下两部分格式输出**\n\n")
	sb.WriteString("## 第一部分：思维链（纯文本）\n")
	sb.WriteString("简要说明你的市场判断、信号强度、风险逻辑。\n\n")
	sb.WriteString("## 第二部分：JSON 决策数组（必须是数组格式）\n\n")
	sb.WriteString("**⚠️ 重要：必须输出 JSON 数组 `[...]`，即使只有一个决策也要用数组包裹！**\n\n")
	sb.WriteString("```json\n[\n")
	sb.WriteString(fmt.Sprintf("  {\"symbol\": \"BTCUSDT\", \"action\": \"open_short\", \"leverage\": %d, \"position_size_usd\": %.0f, \"stop_loss\": 97000, \"take_profit\": 91000, \"confidence\": 85, \"risk_usd\": 300, \"reasoning\": \"4H下跌结构保持，MACD死叉+成交量放大；RSI未超卖，趋势延续概率高\"},\n", btcEthLeverage, accountEquity*5))
	sb.WriteString("  {\"symbol\": \"ETHUSDT\", \"action\": \"close_long\", \"reasoning\": \"触及日线阻力区+RSI>70，止盈离场\"}\n")
	sb.WriteString("]\n```\n\n")
	sb.WriteString("**如果没有任何操作，输出观望决策：**\n")
	sb.WriteString("```json\n[\n")
	sb.WriteString("  {\n    \"symbol\": \"MARKET\",\n    \"action\": \"wait\",\n    \"reasoning\": \"市场震荡，无明确信号，等待更好机会\"\n  }\n")
	sb.WriteString("]\n```\n\n")
	sb.WriteString("## 字段说明\n\n")
	sb.WriteString("* **必填字段**：`symbol`, `action`, `reasoning`\n")
	sb.WriteString("* **action 取值**：`open_long` | `open_short` | `close_long` | `close_short` | `hold` | `wait`\n")
	sb.WriteString("* **confidence**：0-100 整数（≥75 方可开仓）\n")
	sb.WriteString("* **开仓时必须提供**：`leverage`, `position_size_usd`, `stop_loss`, `take_profit`, `risk_usd`, `confidence`\n")
	sb.WriteString("* **平仓/持有/观望**：只需 `symbol`, `action`, `reasoning`\n\n")
	sb.WriteString("## ⚠️ 格式注意事项\n\n")
	sb.WriteString("1. **必须输出数组** `[...]`，不能输出单个对象 `{...}`\n")
	sb.WriteString("2. **不要输出字符串数组**，必须是对象数组\n")
	sb.WriteString("3. **字段名必须完全匹配**（如 `action` 不能写成 `decision`）\n")
	sb.WriteString("4. **数值字段不要加引号**（如 `\"confidence\": 85` 而不是 `\"confidence\": \"85\"`）\n")
	sb.WriteString("5. **JSON 必须完整且格式正确**，不要截断\n\n")
	sb.WriteString("---\n\n")

	// 📚 指导原则
	sb.WriteString("# 📚 指导原则\n\n")
	sb.WriteString("1. **夏普优先**：稳定 > 爆赚\n")
	sb.WriteString("2. **做多做空平衡**：顺势为王，勿有多头偏见\n")
	sb.WriteString("3. **质量优先**：宁错过，不做低质量信号\n")
	sb.WriteString("4. **纪律执行**：严格止损止盈，不移动防线\n")
	sb.WriteString("5. **量化思维**：趋势 × 结构 × 形态 × 量 共振才交易\n")
	sb.WriteString("6. **数据解释**：序列按时间升序排列，最后一项为最新数据\n\n")
	sb.WriteString("---\n\n")
	sb.WriteString("**结语**\n")
	sb.WriteString("你是一名 Sharpe-Ratio 驱动的量化交易智能体。\n")
	sb.WriteString("你的任务：耐心、系统、理性地交易，以稳定复利战胜市场噪音。\n")

	return sb.String()
}

// buildUserPrompt 构建 User Prompt（动态数据）
func buildUserPrompt(ctx *Context) string {
	var sb strings.Builder

	// 系统状态
	sb.WriteString(fmt.Sprintf("**时间**: %s | **周期**: #%d | **运行**: %d分钟\n\n",
		ctx.CurrentTime, ctx.CallCount, ctx.RuntimeMinutes))

	// BTC 市场
	if btcData, hasBTC := ctx.MarketDataMap["BTCUSDT"]; hasBTC {
		sb.WriteString(fmt.Sprintf("**BTC**: %.2f (1h: %+.2f%%, 4h: %+.2f%%) | MACD: %.4f | RSI: %.2f\n\n",
			btcData.CurrentPrice, btcData.PriceChange1h, btcData.PriceChange4h,
			btcData.CurrentMACD, btcData.CurrentRSI7))
	}

	// 账户
	sb.WriteString(fmt.Sprintf("**账户**: 净值%.2f | 余额%.2f (%.1f%%) | 盈亏%+.2f%% | 保证金%.1f%% | 持仓%d个\n\n",
		ctx.Account.TotalEquity,
		ctx.Account.AvailableBalance,
		(ctx.Account.AvailableBalance/ctx.Account.TotalEquity)*100,
		ctx.Account.TotalPnLPct,
		ctx.Account.MarginUsedPct,
		ctx.Account.PositionCount))

	// 持仓（完整市场数据）
	if len(ctx.Positions) > 0 {
		sb.WriteString("## 当前持仓\n")
		for i, pos := range ctx.Positions {
			// 计算持仓时长
			holdingDuration := ""
			if pos.UpdateTime > 0 {
				durationMs := time.Now().UnixMilli() - pos.UpdateTime
				durationMin := durationMs / (1000 * 60) // 转换为分钟
				if durationMin < 60 {
					holdingDuration = fmt.Sprintf(" | 持仓时长%d分钟", durationMin)
				} else {
					durationHour := durationMin / 60
					durationMinRemainder := durationMin % 60
					holdingDuration = fmt.Sprintf(" | 持仓时长%d小时%d分钟", durationHour, durationMinRemainder)
				}
			}

			sb.WriteString(fmt.Sprintf("%d. %s %s | 入场价%.4f 当前价%.4f | 盈亏%+.2f%% | 杠杆%dx | 保证金%.0f | 强平价%.4f%s\n",
				i+1, pos.Symbol, strings.ToUpper(pos.Side),
				pos.EntryPrice, pos.MarkPrice, pos.UnrealizedPnLPct,
				pos.Leverage, pos.MarginUsed, pos.LiquidationPrice, holdingDuration))

			// 添加开仓理由（如果有）
			if pos.OpeningReason != "" {
				sb.WriteString(fmt.Sprintf("   开仓理由: %s\n", pos.OpeningReason))
			}
			sb.WriteString("\n")

			// 使用FormatMarketData输出完整市场数据
			if marketData, ok := ctx.MarketDataMap[pos.Symbol]; ok {
				sb.WriteString(market.Format(marketData))
				sb.WriteString("\n")
			}
		}
	} else {
		sb.WriteString("**当前持仓**: 无\n\n")
	}

	// 候选币种（完整市场数据）
	sb.WriteString(fmt.Sprintf("## 候选币种 (%d个)\n\n", len(ctx.MarketDataMap)))
	displayedCount := 0
	for _, coin := range ctx.CandidateCoins {
		marketData, hasData := ctx.MarketDataMap[coin.Symbol]
		if !hasData {
			continue
		}
		displayedCount++

		sourceTags := ""
		if len(coin.Sources) > 1 {
			sourceTags = " (AI500+OI_Top双重信号)"
		} else if len(coin.Sources) == 1 && coin.Sources[0] == "oi_top" {
			sourceTags = " (OI_Top持仓增长)"
		}

		// 使用FormatMarketData输出完整市场数据
		sb.WriteString(fmt.Sprintf("### %d. %s%s\n\n", displayedCount, coin.Symbol, sourceTags))
		sb.WriteString(market.Format(marketData))
		sb.WriteString("\n")
	}
	sb.WriteString("\n")

	// 夏普比率（直接传值，不要复杂格式化）
	if ctx.Performance != nil {
		// 直接从interface{}中提取SharpeRatio
		type PerformanceData struct {
			SharpeRatio float64 `json:"sharpe_ratio"`
		}
		var perfData PerformanceData
		if jsonData, err := json.Marshal(ctx.Performance); err == nil {
			if err := json.Unmarshal(jsonData, &perfData); err == nil {
				sb.WriteString(fmt.Sprintf("## 📊 夏普比率: %.2f\n\n", perfData.SharpeRatio))
			}
		}
	}

	sb.WriteString("---\n\n")
	sb.WriteString("现在请分析并输出决策（思维链 + JSON）\n")

	return sb.String()
}

// parseFullDecisionResponse 解析AI的完整决策响应
func parseFullDecisionResponse(aiResponse string, accountEquity float64, btcEthLeverage, altcoinLeverage int, pairLeverage map[string]int) (*FullDecision, error) {
	// 1. 提取思维链
	cotTrace := extractCoTTrace(aiResponse)

	// 2. 提取JSON决策列表
	decisions, err := extractDecisions(aiResponse)
	if err != nil {
		return &FullDecision{
			CoTTrace:  cotTrace,
			Decisions: []Decision{},
		}, fmt.Errorf("提取决策失败: %w\n\n=== AI思维链分析 ===\n%s", err, cotTrace)
	}

	// 3. 验证决策
	if err := validateDecisions(decisions, accountEquity, btcEthLeverage, altcoinLeverage, pairLeverage); err != nil {
		return &FullDecision{
			CoTTrace:  cotTrace,
			Decisions: decisions,
		}, fmt.Errorf("决策验证失败: %w\n\n=== AI思维链分析 ===\n%s", err, cotTrace)
	}

	return &FullDecision{
		CoTTrace:  cotTrace,
		Decisions: decisions,
	}, nil
}

// extractCoTTrace 提取思维链分析
func extractCoTTrace(response string) string {
	// 查找JSON数组的开始位置
	jsonStart := strings.Index(response, "[")

	if jsonStart > 0 {
		// 思维链是JSON数组之前的内容
		return strings.TrimSpace(response[:jsonStart])
	}

	// 如果找不到JSON，整个响应都是思维链
	return strings.TrimSpace(response)
}

// extractDecisions 提取JSON决策列表
func extractDecisions(response string) ([]Decision, error) {
	// 直接查找JSON数组 - 找第一个完整的JSON数组
	arrayStart := strings.Index(response, "[")
	if arrayStart == -1 {
		return nil, fmt.Errorf("无法找到JSON数组起始")
	}

	// 从 [ 开始，匹配括号找到对应的 ]
	arrayEnd := findMatchingBracket(response, arrayStart)
	if arrayEnd == -1 {
		return nil, fmt.Errorf("无法找到JSON数组结束")
	}

	jsonContent := strings.TrimSpace(response[arrayStart : arrayEnd+1])

	// 🔧 修复常见的JSON格式错误：缺少引号的字段值
	// 匹配: "reasoning": 内容"}  或  "reasoning": 内容}  (没有引号)
	// 修复为: "reasoning": "内容"}
	// 使用简单的字符串扫描而不是正则表达式
	jsonContent = fixMissingQuotes(jsonContent)

	// 解析JSON
	var decisions []Decision
	if err := json.Unmarshal([]byte(jsonContent), &decisions); err != nil {
		return nil, fmt.Errorf("JSON解析失败: %w\nJSON内容: %s", err, jsonContent)
	}

	return decisions, nil
}

// fixMissingQuotes 替换中文引号为英文引号（避免输入法自动转换）
func fixMissingQuotes(jsonStr string) string {
	jsonStr = strings.ReplaceAll(jsonStr, "\u201c", "\"") // "
	jsonStr = strings.ReplaceAll(jsonStr, "\u201d", "\"") // "
	jsonStr = strings.ReplaceAll(jsonStr, "\u2018", "'")  // '
	jsonStr = strings.ReplaceAll(jsonStr, "\u2019", "'")  // '
	return jsonStr
}

// validateDecisions 验证所有决策（需要账户信息和杠杆配置）
func validateDecisions(decisions []Decision, accountEquity float64, btcEthLeverage, altcoinLeverage int, pairLeverage map[string]int) error {
	for i, decision := range decisions {
		if err := validateDecision(&decision, accountEquity, btcEthLeverage, altcoinLeverage, pairLeverage); err != nil {
			return fmt.Errorf("决策 #%d 验证失败: %w", i+1, err)
		}
	}
	return nil
}

// findMatchingBracket 查找匹配的右括号
func findMatchingBracket(s string, start int) int {
	if start >= len(s) || s[start] != '[' {
		return -1
	}

	depth := 0
	for i := start; i < len(s); i++ {
		switch s[i] {
		case '[':
			depth++
		case ']':
			depth--
			if depth == 0 {
				return i
			}
		}
	}

	return -1
}

// validateDecision 验证单个决策的有效性
func validateDecision(d *Decision, accountEquity float64, btcEthLeverage, altcoinLeverage int, pairLeverage map[string]int) error {
	// 验证必填字段
	if d.Symbol == "" {
		return fmt.Errorf("symbol是必填字段")
	}
	if d.Reasoning == "" {
		return fmt.Errorf("reasoning是必填字段")
	}

	// 验证action
	validActions := map[string]bool{
		"open_long":   true,
		"open_short":  true,
		"close_long":  true,
		"close_short": true,
		"hold":        true,
		"wait":        true,
	}

	if !validActions[d.Action] {
		return fmt.Errorf("无效的action: %s", d.Action)
	}

	// 开仓操作必须提供完整参数
	if d.Action == "open_long" || d.Action == "open_short" {
		// 根据币种使用配置的杠杆上限
		maxLeverage := altcoinLeverage          // 山寨币使用配置的杠杆
		maxPositionValue := accountEquity * 1.5 // 山寨币最多1.5倍账户净值
		
		// 优先检查是否有特定交易对的杠杆设置
		if pairLeverage != nil {
			if specificLeverage, exists := pairLeverage[d.Symbol]; exists {
				maxLeverage = specificLeverage
				log.Printf("💡 使用特定交易对杠杆配置: %s 设置为%d倍", d.Symbol, maxLeverage)
			}
		}
		
		// 如果没有特定交易对配置，则使用默认逻辑
		if maxLeverage == altcoinLeverage { // 表示上面没有覆盖
			if d.Symbol == "BTCUSDT" || d.Symbol == "ETHUSDT" {
				maxLeverage = btcEthLeverage          // BTC和ETH使用配置的杠杆
				maxPositionValue = accountEquity * 10 // BTC/ETH最多10倍账户净值
			}
		}

		// 修正杠杆值：如果小于等于0或超过配置上限，则自动修正
		if d.Leverage <= 0 {
			d.Leverage = 1 // 设置为最小杠杆倍数
			log.Printf("⚠️  修正杠杆值: %s 的杠杆被修正为1倍（原杠杆≤0）", d.Symbol)
		} else if d.Leverage > maxLeverage {
			d.Leverage = maxLeverage // 设置为配置的最大杠杆倍数
			log.Printf("⚠️  修正杠杆值: %s 的杠杆被修正为%d倍（原杠杆%d超过配置上限）", d.Symbol, maxLeverage, d.Leverage)
		}
		if d.PositionSizeUSD <= 0 {
			return fmt.Errorf("仓位大小必须大于0: %.2f", d.PositionSizeUSD)
		}
		// 验证仓位价值上限（加1%容差以避免浮点数精度问题）
		tolerance := maxPositionValue * 0.01 // 1%容差
		if d.PositionSizeUSD > maxPositionValue+tolerance {
			if d.Symbol == "BTCUSDT" || d.Symbol == "ETHUSDT" {
				return fmt.Errorf("BTC/ETH单币种仓位价值不能超过%.0f USDT（10倍账户净值），实际: %.0f", maxPositionValue, d.PositionSizeUSD)
			} else {
				return fmt.Errorf("山寨币单币种仓位价值不能超过%.0f USDT（1.5倍账户净值），实际: %.0f", maxPositionValue, d.PositionSizeUSD)
			}
		}
		if d.StopLoss <= 0 || d.TakeProfit <= 0 {
			return fmt.Errorf("止损和止盈必须大于0")
		}
		
		// 验证信心度（必须≥75）
		if d.Confidence < 75 {
			return fmt.Errorf("开仓信心度必须≥75，当前: %d", d.Confidence)
		}
		
		// 验证risk_usd字段
		if d.RiskUSD <= 0 {
			return fmt.Errorf("risk_usd必须大于0")
		}

		// 验证止损止盈的合理性
		if d.Action == "open_long" {
			if d.StopLoss >= d.TakeProfit {
				return fmt.Errorf("做多时止损价必须小于止盈价")
			}
		} else {
			if d.StopLoss <= d.TakeProfit {
				return fmt.Errorf("做空时止损价必须大于止盈价")
			}
		}

		// 验证风险回报比（必须≥1:3）
		// 计算入场价（假设当前市价）
		var entryPrice float64
		if d.Action == "open_long" {
			// 做多：入场价在止损和止盈之间
			entryPrice = d.StopLoss + (d.TakeProfit-d.StopLoss)*0.2 // 假设在20%位置入场
		} else {
			// 做空：入场价在止损和止盈之间
			entryPrice = d.StopLoss - (d.StopLoss-d.TakeProfit)*0.2 // 假设在20%位置入场
		}

		var riskPercent, rewardPercent, riskRewardRatio float64
		if d.Action == "open_long" {
			riskPercent = (entryPrice - d.StopLoss) / entryPrice * 100
			rewardPercent = (d.TakeProfit - entryPrice) / entryPrice * 100
			if riskPercent > 0 {
				riskRewardRatio = rewardPercent / riskPercent
			}
		} else {
			riskPercent = (d.StopLoss - entryPrice) / entryPrice * 100
			rewardPercent = (entryPrice - d.TakeProfit) / entryPrice * 100
			if riskPercent > 0 {
				riskRewardRatio = rewardPercent / riskPercent
			}
		}

		// 硬约束：风险回报比必须≥3.0
		if riskRewardRatio < 3.0 {
			return fmt.Errorf("风险回报比过低(%.2f:1)，必须≥3.0:1 [风险:%.2f%% 收益:%.2f%%] [止损:%.2f 止盈:%.2f]",
				riskRewardRatio, riskPercent, rewardPercent, d.StopLoss, d.TakeProfit)
		}
	}

	return nil
}
