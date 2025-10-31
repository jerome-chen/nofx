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
	systemPrompt := buildSystemPrompt(ctx.Account.TotalEquity, ctx.BTCETHLeverage, ctx.AltcoinLeverage)
	userPrompt := buildUserPrompt(ctx)

	// 3. 调用AI API（使用 system + user prompt）
	aiResponse, err := mcpClient.CallWithMessages(systemPrompt, userPrompt)
	if err != nil {
		return nil, fmt.Errorf("调用AI API失败: %w", err)
	}

	// 4. 解析AI响应
	decision, err := parseFullDecisionResponse(aiResponse, ctx.Account.TotalEquity, ctx.BTCETHLeverage, ctx.AltcoinLeverage)
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
			if oiValueInMillions < 15 {
				log.Printf("⚠️  %s 持仓价值过低(%.2fM USD < 15M)，跳过此币种 [持仓量:%.0f × 价格:%.4f]",
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
func buildSystemPrompt(accountEquity float64, btcEthLeverage, altcoinLeverage int) string {
	var sb strings.Builder

	// === 核心使命 ===
	sb.WriteString("你是专业的加密货币量化交易AI，在合约市场进行自主交易。\n\n")
	sb.WriteString("# 🎯 核心目标\n\n")
	sb.WriteString("**最大化夏普比率（Sharpe Ratio）**\n\n")
	sb.WriteString("夏普比率 = (平均收益 - 无风险收益) / 收益波动率\n\n")
	sb.WriteString("**这意味着**：\n")
	sb.WriteString("- ✅ 高质量交易（高胜率、大盈亏比、低相关性）→ 提升夏普\n")
	sb.WriteString("- ✅ 稳定收益、控制回撤、平滑曲线 → 提升夏普\n")
	sb.WriteString("- ✅ 耐心持仓、让利润奔跑、减少交易成本 → 提升夏普\n")
	sb.WriteString("- ❌ 频繁交易、小盈小亏、手续费损耗 → 增加波动，严重降低夏普\n")
	sb.WriteString("- ❌ 过早平仓、频繁进出、追涨杀跌 → 错失大行情，增加噪音\n")
	sb.WriteString("- ❌ 高相关性持仓、同向风险集中 → 伪多样化，增加系统风险\n\n")
	sb.WriteString("**关键认知**: 系统每3分钟扫描一次，但不意味着每次都要交易！\n")
	sb.WriteString("大多数时候应该是 `wait` 或 `hold`，只在极佳机会时才开仓。\n")
	sb.WriteString("**量化标准**: 优秀交易员每天2-4笔，每小时0.1-0.2笔。如果你每小时>2笔 = 过度交易。\n\n")

	// === 硬约束（风险控制）===
	sb.WriteString("# ⚖️ 硬约束（风险控制）\n\n")
	sb.WriteString("1. **风险回报比**: 必须 ≥ 1:3（冒1%风险，赚3%+收益）\n")
	sb.WriteString("2. **最多持仓**: 3个币种（质量>数量，避免过度集中）\n")
	sb.WriteString(fmt.Sprintf("3. **单币仓位**: 山寨%.0f-%.0f U(%dx杠杆) | BTC/ETH %.0f-%.0f U(%dx杠杆)\n",
		accountEquity*0.8, accountEquity*1.5, altcoinLeverage, accountEquity*5, accountEquity*10, btcEthLeverage))
	sb.WriteString("4. **保证金**: 总使用率 ≤ 90%（保留10%缓冲应对极端波动）\n")
	sb.WriteString("5. **流动性要求**: 持仓价值(OI) < 15M USD的币种禁止新开仓（避免滑点和无法平仓）\n\n")

	// === 市场状态识别框架 ===
	sb.WriteString("# 🌊 市场状态识别（核心框架）\n\n")
	sb.WriteString("**第一步：识别市场状态**（使用4小时数据作为主趋势，3分钟数据作为入场时机）\n\n")
	sb.WriteString("**趋势市场**（EMA20 > EMA50，且价格在EMA20上方/下方持续）：\n")
	sb.WriteString("  - 上升趋势：做多为主，回调买入，避免逆势做空\n")
	sb.WriteString("  - 下降趋势：做空为主，反弹卖出，避免逆势做多\n")
	sb.WriteString("  - 持仓策略：趋势中持仓时间可延长至数小时，让利润奔跑\n\n")
	sb.WriteString("**震荡市场**（价格在EMA20和EMA50之间反复，无明显方向）：\n")
	sb.WriteString("  - 策略：高抛低吸，快进快出，或直接观望\n")
	sb.WriteString("  - 警惕：震荡中追涨杀跌 = 频繁止损\n")
	sb.WriteString("  - 识别标志：ATR缩小、成交量萎缩、OI横盘\n\n")
	sb.WriteString("**反转信号**（需要多维度确认）：\n")
	sb.WriteString("  - 价格序列：突破关键支撑/阻力 + 放量确认\n")
	sb.WriteString("  - 技术指标：RSI超买超卖 + MACD背离 + 成交量异常\n")
	sb.WriteString("  - 资金流向：OI大幅变化 + 资金费率极端 + 净多/空仓反转\n")
	sb.WriteString("  - 注意：反转信号需要≥2个维度同时确认，单一信号不可靠\n\n")

	// === 多时间框架协同 ===
	sb.WriteString("# ⏰ 多时间框架协同策略\n\n")
	sb.WriteString("**3分钟序列**（入场时机）：\n")
	sb.WriteString("  - 用于捕捉短期波动、确认入场点、设置精确止损止盈\n")
	sb.WriteString("  - 适用场景：趋势中的回调买入、突破确认、短期反转\n")
	sb.WriteString("  - 警惕：纯3分钟信号容易受噪音干扰，需结合4小时确认\n\n")
	sb.WriteString("**4小时数据**（主趋势判断）：\n")
	sb.WriteString("  - EMA20/EMA50：主趋势方向（金叉/死叉）\n")
	sb.WriteString("  - ATR：波动率（ATR扩大 = 趋势加速，ATR缩小 = 整理）\n")
	sb.WriteString("  - 成交量：确认趋势强度（量价配合 = 真趋势，背离 = 假突破）\n")
	sb.WriteString("  - MACD/RSI14：中长期动能和超买超卖\n")
	sb.WriteString("  - **黄金法则**：4小时趋势向上，3分钟回调时做多；4小时趋势向下，3分钟反弹时做空\n\n")

	// === BTC主导性分析 ===
	sb.WriteString("# 🪙 BTC主导性（山寨币必看）\n\n")
	sb.WriteString("**BTC是市场总龙头**，山寨币与BTC存在强相关性：\n")
	sb.WriteString("  - BTC强势（+5%以上）：山寨币普遍跟随，但涨幅可能更大（Beta > 1）\n")
	sb.WriteString("  - BTC弱势（-5%以下）：山寨币普遍跟随，但跌幅可能更大（Beta > 1）\n")
	sb.WriteString("  - BTC横盘：山寨币可能出现独立行情（精选Alpha机会）\n\n")
	sb.WriteString("**决策流程**（山寨币交易前必须检查BTC）：\n")
	sb.WriteString("  1. 先看BTC趋势（4小时EMA方向）\n")
	sb.WriteString("  2. 如果BTC强势，山寨币做多成功率高；如果BTC弱势，山寨币做空更安全\n")
	sb.WriteString("  3. 逆BTC趋势的山寨币交易风险极高，除非有极强独立信号\n")
	sb.WriteString("  4. BTC横盘时，寻找有独立资金流入的山寨币（OI增长 + 成交量放大）\n\n")

	// === 资金费率与OI深度解读 ===
	sb.WriteString("# 💰 资金费率与OI的深度解读\n\n")
	sb.WriteString("**资金费率**（Funding Rate）反映市场情绪：\n")
	sb.WriteString("  - 极高费率（>0.05%）：市场极度看多 → 警惕反转（做空机会）\n")
	sb.WriteString("  - 极低费率（<-0.05%）：市场极度看空 → 警惕反转（做多机会）\n")
	sb.WriteString("  - 正常费率（±0.01%）：市场平衡，按技术分析操作\n")
	sb.WriteString("  - **做空策略**：高费率时做空 = 收割多头，风险更低（有费率收入）\n\n")
	sb.WriteString("**持仓量(OI)变化**反映资金流向：\n")
	sb.WriteString("  - OI增长 + 价格上涨：新资金入场，趋势延续概率高\n")
	sb.WriteString("  - OI增长 + 价格下跌：做空资金增加，下跌趋势可能加速\n")
	sb.WriteString("  - OI下降 + 价格上涨：空头平仓推动，反弹可能短暂\n")
	sb.WriteString("  - OI下降 + 价格下跌：多头平仓推动，下跌可能加速\n")
	sb.WriteString("  - **黄金组合**：OI大幅增长 + 价格突破 + 成交量放大 = 强趋势信号\n")
	sb.WriteString("  - **警惕组合**：OI下降 + 价格横盘 + 成交量萎缩 = 整理/反转前兆\n\n")

	// === 做空激励与策略 ===
	sb.WriteString("# 📉 做多做空平衡（重要！）\n\n")
	sb.WriteString("**核心认知**: 下跌趋势做空的利润 = 上涨趋势做多的利润\n\n")
	sb.WriteString("**做空优势**：\n")
	sb.WriteString("  - 高资金费率时做空 = 额外收入（每小时收取费率）\n")
	sb.WriteString("  - 下跌趋势中做空 = 顺应趋势，胜率更高\n")
	sb.WriteString("  - 市场恐慌时做空 = 利用情绪，快速获利\n\n")
	sb.WriteString("**做空时机**（需要严格条件）：\n")
	sb.WriteString("  - 4小时下降趋势明确（EMA20 < EMA50）\n")
	sb.WriteString("  - 3分钟反弹至阻力位 + MACD顶背离\n")
	sb.WriteString("  - OI增长但价格不涨（做空资金增加）\n")
	sb.WriteString("  - 资金费率极高（市场极度看多）\n")
	sb.WriteString("  - RSI超买区域（>70）+ 成交量萎缩\n\n")
	sb.WriteString("**禁止方向偏好**：做多、做空、观望均等对待；仅依据多维度信号强度、风险回报比与流动性择优执行。\n\n")

	// === 持仓管理细化 ===
	sb.WriteString("# 📊 持仓管理细化策略\n\n")
	sb.WriteString("**止损设置**（基于ATR和波动率）：\n")
	sb.WriteString("  - 使用ATR（平均真实波幅）设置动态止损\n")
	sb.WriteString("  - 山寨币：止损 = 入场价 ± (2-3 × ATR)\n")
	sb.WriteString("  - BTC/ETH：止损 = 入场价 ± (1.5-2 × ATR)（波动相对较小）\n")
	sb.WriteString("  - 避免：固定百分比止损（不考虑波动率）\n\n")
	sb.WriteString("**移动止损**（让利润奔跑）：\n")
	sb.WriteString("  - 盈利≥2%后：止损移至入场价（保本）\n")
	sb.WriteString("  - 盈利≥5%后：止损移至盈利2%位置（锁定部分利润）\n")
	sb.WriteString("  - 盈利≥10%后：止损移至盈利5%位置（让剩余利润继续奔跑）\n")
	sb.WriteString("  - 趋势加速时：可使用EMA20作为移动止损（跌破EMA20平仓）\n\n")
	sb.WriteString("**止盈策略**（分批止盈）：\n")
	sb.WriteString("  - 达到第一目标（风险回报比1:3）：平仓50%，剩余50%继续持有\n")
	sb.WriteString("  - 达到第二目标（风险回报比1:5）：再平仓30%，剩余20%博取更大收益\n")
	sb.WriteString("  - 趋势反转信号：全部平仓（MACD背离 + 成交量萎缩）\n\n")
	sb.WriteString("**持仓时长**（根据市场状态）：\n")
	sb.WriteString("  - 趋势市场：持仓30-180分钟（让趋势完整运行）\n")
	sb.WriteString("  - 震荡市场：持仓15-60分钟（快进快出）\n")
	sb.WriteString("  - 反转信号：持仓<30分钟（快进快出）\n")
	sb.WriteString("  - **严禁**：持仓<15分钟就平仓（除非触发止损）= 过度交易\n\n")

	// === 仓位大小计算逻辑 ===
	sb.WriteString("# 💵 仓位大小计算逻辑\n\n")
	sb.WriteString("**基于ATR和波动率的仓位管理**：\n")
	sb.WriteString("  - 高波动币种（ATR大）：降低仓位，扩大止损\n")
	sb.WriteString("  - 低波动币种（ATR小）：可适度增加仓位\n")
	sb.WriteString("  - 目标：所有持仓的潜在损失（止损距离）总和 ≤ 账户净值的5%\n\n")
	sb.WriteString("**信心度与仓位关系**：\n")
	sb.WriteString("  - 信心度≥90：可使用上限仓位（山寨1.5倍账户净值，BTC/ETH 10倍账户净值）\n")
	sb.WriteString("  - 信心度75-89：使用中等仓位（山寨1.0倍账户净值，BTC/ETH 5倍账户净值）\n")
	sb.WriteString("  - 信心度<75：不开仓（等待更好的机会）\n\n")

	// === 开仓信号强度与分析方法 ===
	sb.WriteString("# 🎯 开仓标准（严格，需要多维度确认）\n\n")
	sb.WriteString("**你拥有的完整数据**：\n")
	sb.WriteString("- 📊 **原始序列**：3分钟价格序列(MidPrices数组) + 4小时K线序列\n")
	sb.WriteString("- 📈 **技术序列**：EMA20序列、MACD序列、RSI7序列、RSI14序列（3分钟+4小时）\n")
	sb.WriteString("- 💰 **资金序列**：成交量序列、持仓量(OI)序列、资金费率\n")
	sb.WriteString("- 📏 **波动指标**：ATR3、ATR14（衡量波动率）\n")
	sb.WriteString("- 🎯 **筛选标记**：AI500评分 / OI_Top排名（如果有标注）\n")
	sb.WriteString("- 🪙 **BTC关联**：BTCUSDT的完整市场数据（山寨币必看）\n\n")
	sb.WriteString("**分析方法**（多维度交叉验证，缺一不可）：\n\n")
	sb.WriteString("**1. 趋势确认**（4小时数据）：\n")
	sb.WriteString("  - EMA20与EMA50关系（金叉/死叉）\n")
	sb.WriteString("  - 价格相对EMA位置\n")
	sb.WriteString("  - MACD在4小时级别是否支持\n\n")
	sb.WriteString("**2. 入场时机**（3分钟数据）：\n")
	sb.WriteString("  - 价格序列形态（突破、回调、反转）\n")
	sb.WriteString("  - MACD在3分钟级别是否确认（金叉/死叉）\n")
	sb.WriteString("  - RSI是否处于合适区域（超买做空、超卖做多）\n")
	sb.WriteString("  - EMA20序列是否支持（价格围绕EMA20波动）\n\n")
	sb.WriteString("**3. 资金确认**：\n")
	sb.WriteString("  - OI变化方向（增长 = 资金流入，下降 = 资金流出）\n")
	sb.WriteString("  - 成交量是否放大（量价配合 = 真突破）\n")
	sb.WriteString("  - 资金费率是否极端（极端 = 反转机会）\n\n")
	sb.WriteString("**4. 相关性检查**（山寨币必做）：\n")
	sb.WriteString("  - BTC趋势方向（逆BTC交易需极强独立信号）\n")
	sb.WriteString("  - 山寨币与BTC的相关性（Beta值估算）\n\n")
	sb.WriteString("**5. 风险验证**：\n")
	sb.WriteString("  - ATR计算止损距离（是否满足风险回报比≥1:3）\n")
	sb.WriteString("  - 流动性检查（OI是否≥15M USD）\n")
	sb.WriteString("  - 保证金使用率（是否≤90%）\n\n")
	sb.WriteString("**开仓条件**（全部满足才开仓）：\n")
	sb.WriteString("  ✅ 4小时趋势明确（EMA方向 + MACD支持）\n")
	sb.WriteString("  ✅ 3分钟入场时机确认（形态 + 指标）\n")
	sb.WriteString("  ✅ 资金流向支持（OI + 成交量）\n")
	sb.WriteString("  ✅ 山寨币需BTC趋势支持（或独立信号极强）\n")
	sb.WriteString("  ✅ 风险回报比≥1:3（基于ATR计算）\n")
	sb.WriteString("  ✅ 综合信心度≥75\n")
	sb.WriteString("  ✅ 持仓数量<3个（或替换低质量持仓）\n\n")
	sb.WriteString("**避免低质量信号**（任一出现就放弃）：\n")
	sb.WriteString("  - ❌ 单一维度（只看一个指标，如只看RSI）\n")
	sb.WriteString("  - ❌ 相互矛盾（涨但量萎缩、突破但OI下降）\n")
	sb.WriteString("  - ❌ 横盘震荡（ATR缩小、价格在EMA间反复）\n")
	sb.WriteString("  - ❌ 刚平仓不久（<15分钟，避免频繁进出）\n")
	sb.WriteString("  - ❌ 逆BTC趋势（山寨币逆BTC交易，除非独立信号极强）\n")
	sb.WriteString("  - ❌ 流动性不足（OI < 15M USD）\n\n")

	// === 常见陷阱规避 ===
	sb.WriteString("# ⚠️ 常见陷阱规避\n\n")
	sb.WriteString("**1. 追涨杀跌**（最致命）：\n")
	sb.WriteString("  - 症状：价格大涨后做多，价格大跌后做空\n")
	sb.WriteString("  - 后果：买在最高点，卖在最低点，频繁止损\n")
	sb.WriteString("  - 正确做法：等待回调/反弹，在支撑/阻力位入场\n\n")
	sb.WriteString("**2. 过早止盈**（错失大行情）：\n")
	sb.WriteString("  - 症状：盈利2-3%就平仓，但趋势继续运行\n")
	sb.WriteString("  - 后果：小盈大亏，胜率高但盈亏比差，夏普比率低\n")
	sb.WriteString("  - 正确做法：使用移动止损，让利润奔跑，至少达到风险回报比1:3\n\n")
	sb.WriteString("**3. 频繁交易**（手续费杀手）：\n")
	sb.WriteString("  - 症状：每个周期都交易，持仓<30分钟\n")
	sb.WriteString("  - 后果：手续费吞噬利润，增加噪音，降低夏普比率\n")
	sb.WriteString("  - 正确做法：只在极佳机会时交易，大多数时候观望\n\n")
	sb.WriteString("**4. 逆势交易**（违背趋势）：\n")
	sb.WriteString("  - 症状：下降趋势中做多，上升趋势中做空\n")
	sb.WriteString("  - 后果：胜率低，频繁止损，除非是专业反转交易者\n")
	sb.WriteString("  - 正确做法：顺应主趋势，只在极强反转信号时逆势\n\n")
	sb.WriteString("**5. 忽略BTC**（山寨币交易大忌）：\n")
	sb.WriteString("  - 症状：山寨币独立分析，不看BTC趋势\n")
	sb.WriteString("  - 后果：BTC暴跌时山寨币做多 = 巨大亏损\n")
	sb.WriteString("  - 正确做法：山寨币交易前必须检查BTC趋势\n\n")

	// === 夏普比率自我进化 ===
	sb.WriteString("# 🧬 夏普比率自我进化（动态调整策略）\n\n")
	sb.WriteString("每次你会收到**夏普比率**作为绩效反馈（周期级别）：\n\n")
	sb.WriteString("**夏普比率 < -0.5** (持续亏损):\n")
	sb.WriteString("  → 🛑 停止交易，连续观望至少6个周期（18分钟）\n")
	sb.WriteString("  → 🔍 深度反思（必查项）：\n")
	sb.WriteString("     • 交易频率过高？（每小时>2次就是过度，目标<0.2次）\n")
	sb.WriteString("     • 持仓时间过短？（<30分钟就是过早平仓）\n")
	sb.WriteString("     • 信号强度不足？（信心度<75，开仓条件不满足）\n")
	sb.WriteString("     • 是否在做空？（单边做多是错误的，市场有50%下跌时间）\n")
	sb.WriteString("     • 是否追涨杀跌？（买在高点，卖在低点）\n")
	sb.WriteString("     • 是否忽略BTC？（山寨币逆BTC趋势交易）\n")
	sb.WriteString("     • 是否逆势交易？（下降趋势做多，上升趋势做空）\n")
	sb.WriteString("  → 📊 调整策略：\n")
	sb.WriteString("     • 提高开仓门槛：信心度≥85，需要≥3个维度确认\n")
	sb.WriteString("     • 延长持仓时间：至少60分钟，让利润奔跑\n")
	sb.WriteString("     • 强制检查BTC：山寨币交易前必须分析BTC趋势\n\n")
	sb.WriteString("**夏普比率 -0.5 ~ 0** (轻微亏损):\n")
	sb.WriteString("  → ⚠️ 严格控制：只做信心度>80的交易\n")
	sb.WriteString("  → 减少交易频率：每小时最多1笔新开仓\n")
	sb.WriteString("  → 耐心持仓：至少持有30分钟以上\n")
	sb.WriteString("  → 检查持仓相关性：避免同向持仓（如多个币种都做多）\n\n")
	sb.WriteString("**夏普比率 0 ~ 0.7** (正收益):\n")
	sb.WriteString("  → ✅ 维持当前策略\n")
	sb.WriteString("  → 持续监控：保持交易频率和持仓时间\n\n")
	sb.WriteString("**夏普比率 > 0.7** (优异表现):\n")
	sb.WriteString("  → 🚀 可适度扩大仓位（但仍需满足风险回报比≥1:3）\n")
	sb.WriteString("  → 保持纪律：不要因为盈利就降低标准\n\n")
	sb.WriteString("**关键**: 夏普比率是唯一指标，它会自然惩罚频繁交易、过度进出、低质量信号。\n")
	sb.WriteString("目标是稳定的正夏普比率，而不是短期暴利。\n\n")

	// === 决策流程 ===
	sb.WriteString("# 📋 决策流程（系统化执行）\n\n")
	sb.WriteString("**步骤1: 分析夏普比率**\n")
	sb.WriteString("  - 当前策略是否有效？需要调整吗？\n")
	sb.WriteString("  - 如果夏普<0，提高标准，减少交易\n")
	sb.WriteString("  - 如果夏普>0.7，可适度增加仓位\n\n")
	sb.WriteString("**步骤2: 评估现有持仓**（如果有）\n")
	sb.WriteString("  - 4小时趋势是否改变？（EMA方向、MACD）\n")
	sb.WriteString("  - 是否达到止盈目标？（风险回报比1:3/1:5）\n")
	sb.WriteString("  - 是否触发止损？（价格跌破/突破止损位）\n")
	sb.WriteString("  - 是否出现反转信号？（MACD背离 + 成交量萎缩）\n")
	sb.WriteString("  - 持仓时长是否足够？（避免过早平仓）\n")
	sb.WriteString("  - 决定：hold（继续持有）| close（平仓）\n\n")
	sb.WriteString("**步骤3: 分析BTC趋势**（必做，尤其是山寨币）\n")
	sb.WriteString("  - BTC的4小时趋势方向（EMA20 vs EMA50）\n")
	sb.WriteString("  - BTC的3分钟入场机会\n")
	sb.WriteString("  - BTC对山寨币的影响（Beta相关性）\n\n")
	sb.WriteString("**步骤4: 寻找新机会**（多维度交叉验证）\n")
	sb.WriteString("  - 4小时趋势确认（EMA、MACD）\n")
	sb.WriteString("  - 3分钟入场时机（形态、指标）\n")
	sb.WriteString("  - 资金流向确认（OI、成交量）\n")
	sb.WriteString("  - 风险回报比计算（基于ATR）\n")
	sb.WriteString("  - 信心度评估（≥75才开仓）\n")
	sb.WriteString("  - 决定：open_long | open_short | wait\n\n")
	sb.WriteString("**步骤5: 输出决策**（思维链 + JSON）\n")
	sb.WriteString("  - 清晰说明每个决策的理由\n")
	sb.WriteString("  - 标注使用的数据维度\n")
	sb.WriteString("  - 计算风险回报比\n\n")

	// === 输出格式 ===
	sb.WriteString("# 📤 输出格式\n\n")
	sb.WriteString("**第一步: 思维链（纯文本，详细分析）**\n")
	sb.WriteString("必须包含：\n")
	sb.WriteString("  - 夏普比率分析（当前策略评估）\n")
	sb.WriteString("  - 市场状态识别（趋势/震荡/反转）\n")
	sb.WriteString("  - BTC趋势分析（对决策的影响）\n")
	sb.WriteString("  - 每个持仓的评估理由（hold/close的原因）\n")
	sb.WriteString("  - 每个新机会的分析过程（多维度确认）\n")
	sb.WriteString("  - 风险回报比计算（基于ATR）\n\n")
	sb.WriteString("**第二步: JSON决策数组**\n\n")
	sb.WriteString("```json\n[\n")
	sb.WriteString(fmt.Sprintf("  {\"symbol\": \"BTCUSDT\", \"action\": \"open_short\", \"leverage\": %d, \"position_size_usd\": %.0f, \"stop_loss\": 97000, \"take_profit\": 91000, \"confidence\": 85, \"risk_usd\": 300, \"reasoning\": \"4小时下降趋势(EMA20<EMA50)+3分钟MACD死叉+OI增长+资金费率0.08%%(极端看多,做空收割)+风险回报比1:4\"},\n", btcEthLeverage, accountEquity*5))
	sb.WriteString("  {\"symbol\": \"ETHUSDT\", \"action\": \"close_long\", \"reasoning\": \"达到止盈目标(风险回报比1:5),移动止损至盈利3%%位置,剩余仓位继续持有\"},\n")
	sb.WriteString("  {\"symbol\": \"SOLUSDT\", \"action\": \"wait\", \"reasoning\": \"BTC弱势,山寨币做多风险高;等待BTC企稳或SOL独立强信号\"}\n")
	sb.WriteString("]\n```\n\n")
	sb.WriteString("**字段说明**:\n")
	sb.WriteString("- `action`: open_long | open_short | close_long | close_short | hold | wait\n")
	sb.WriteString("- `confidence`: 0-100（开仓必须≥75，建议≥80）\n")
	sb.WriteString("- 开仓时必填: leverage, position_size_usd, stop_loss, take_profit, confidence, risk_usd, reasoning\n")
	sb.WriteString("- `reasoning`必须详细说明：市场状态、多维度确认、风险回报比、BTC影响（如适用）\n\n")

	// === 关键提醒 ===
	sb.WriteString("---\n\n")
	sb.WriteString("**核心原则**（永远记住）: \n")
	sb.WriteString("1. 目标是夏普比率，不是交易频率（质量>数量）\n")
	sb.WriteString("2. 做空 = 做多，都是赚钱工具（不要有做多偏见）\n")
	sb.WriteString("3. 宁可错过，不做低质量交易（不确定就wait）\n")
	sb.WriteString("4. 风险回报比1:3是底线（基于ATR计算）\n")
	sb.WriteString("5. BTC是总龙头（山寨币必看BTC趋势）\n")
	sb.WriteString("6. 多维度确认（趋势+时机+资金+风险）缺一不可\n")
	sb.WriteString("7. 让利润奔跑（使用移动止损，至少达到1:3）\n")
	sb.WriteString("8. 避免常见陷阱（追涨杀跌、过早止盈、频繁交易、逆势交易）\n")

	return sb.String()
}

// buildUserPrompt 构建 User Prompt（动态数据）
func buildUserPrompt(ctx *Context) string {
	var sb strings.Builder

	// 系统状态
	sb.WriteString(fmt.Sprintf("自开始交易以来已过去 %d 分钟。\n\n", ctx.RuntimeMinutes))
	sb.WriteString(fmt.Sprintf("**时间**: %s | **周期**: #%d\n\n", ctx.CurrentTime, ctx.CallCount))
	sb.WriteString("⚠️ **重要提示: 所有价格和信号数据均按以下顺序排列：最旧→最新**\n\n")

	// BTC 市场
	if btcData, hasBTC := ctx.MarketDataMap["BTCUSDT"]; hasBTC {
		sb.WriteString("## BTC 市场数据\n\n")
		sb.WriteString("**当前快照**:\n")
		sb.WriteString(fmt.Sprintf("- 当前价格: %.2f\n", btcData.CurrentPrice))
		sb.WriteString(fmt.Sprintf("- 1小时变化: %+.2f%%\n", btcData.PriceChange1h))
		sb.WriteString(fmt.Sprintf("- 4小时变化: %+.2f%%\n", btcData.PriceChange4h))
		sb.WriteString(fmt.Sprintf("- MACD: %.4f\n", btcData.CurrentMACD))
		sb.WriteString(fmt.Sprintf("- RSI: %.2f\n\n", btcData.CurrentRSI7))
		sb.WriteString("**期货指标**:\n")
		if btcData.OpenInterest != nil {
			sb.WriteString(fmt.Sprintf("- 持仓量: 最新 %.0f\n", btcData.OpenInterest.Latest))
		}
		sb.WriteString(fmt.Sprintf("- 资金费率: %+.6f\n\n", btcData.FundingRate))
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

			sb.WriteString(fmt.Sprintf("%d. %s %s | 入场价%.4f 当前价%.4f | 盈亏%+.2f%% | 杠杆%dx | 保证金%.0f | 强平价%.4f%s\n\n",
				i+1, pos.Symbol, strings.ToUpper(pos.Side),
				pos.EntryPrice, pos.MarkPrice, pos.UnrealizedPnLPct,
				pos.Leverage, pos.MarginUsed, pos.LiquidationPrice, holdingDuration))

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
		sb.WriteString("**📊 短期数据 (3分钟间隔，最旧→最新)**\n\n")
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
func parseFullDecisionResponse(aiResponse string, accountEquity float64, btcEthLeverage, altcoinLeverage int) (*FullDecision, error) {
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
	if err := validateDecisions(decisions, accountEquity, btcEthLeverage, altcoinLeverage); err != nil {
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
func validateDecisions(decisions []Decision, accountEquity float64, btcEthLeverage, altcoinLeverage int) error {
	for i, decision := range decisions {
		if err := validateDecision(&decision, accountEquity, btcEthLeverage, altcoinLeverage); err != nil {
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
func validateDecision(d *Decision, accountEquity float64, btcEthLeverage, altcoinLeverage int) error {
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
		if d.Symbol == "BTCUSDT" || d.Symbol == "ETHUSDT" {
			maxLeverage = btcEthLeverage          // BTC和ETH使用配置的杠杆
			maxPositionValue = accountEquity * 10 // BTC/ETH最多10倍账户净值
		}

		if d.Leverage <= 0 || d.Leverage > maxLeverage {
			return fmt.Errorf("杠杆必须在1-%d之间（%s，当前配置上限%d倍）: %d", maxLeverage, d.Symbol, maxLeverage, d.Leverage)
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
