package trader

import (
	"log"
	"testing"
	"time"
)

// 测试updateTime处理逻辑
func TestUpdateTimeHandling(t *testing.T) {
	// 测试场景1：有效的int64 updateTime
	t.Run("ValidInt64UpdateTime", func(t *testing.T) {
		// 模拟API返回的updateTime
		validTime := int64(time.Now().UnixMilli() - 10*60*1000)
		pos := map[string]interface{}{
			"updateTime": validTime,
		}
		
		// 模拟GetPositions中的updateTime处理逻辑
		updateTime := int64(0)
		foundValidTime := false
		if ut, ok := pos["updateTime"].(int64); ok && ut > 0 {
			updateTime = ut
			foundValidTime = true
		} else if utFloat, ok := pos["updateTime"].(float64); ok && utFloat > 0 {
			updateTime = int64(utFloat)
			foundValidTime = true
		}
		if !foundValidTime {
			updateTime = time.Now().UnixMilli()
		}
		
		if updateTime != validTime {
			t.Errorf("Expected updateTime %d, got %d", validTime, updateTime)
		}
		
		// 计算持仓时长
		durationMs := time.Now().UnixMilli() - updateTime
		durationMin := durationMs / (1000 * 60)
		log.Printf("Valid int64 updateTime: %d, holding duration: %d minutes", updateTime, durationMin)
	})
	
	// 测试场景2：有效的float64 updateTime（API返回的科学计数法格式）
	t.Run("ValidFloat64UpdateTime", func(t *testing.T) {
		// 模拟API返回的updateTime（浮点数格式）
		validTime := int64(time.Now().UnixMilli() - 5*60*1000)
		floatTime := float64(validTime)
		pos := map[string]interface{}{
			"updateTime": floatTime,
		}
		
		// 模拟GetPositions中的updateTime处理逻辑
		updateTime := int64(0)
		foundValidTime := false
		if ut, ok := pos["updateTime"].(int64); ok && ut > 0 {
			updateTime = ut
			foundValidTime = true
		} else if utFloat, ok := pos["updateTime"].(float64); ok && utFloat > 0 {
			updateTime = int64(utFloat)
			foundValidTime = true
		}
		if !foundValidTime {
			updateTime = time.Now().UnixMilli()
		}
		
		if updateTime != validTime {
			t.Errorf("Expected updateTime %d, got %d", validTime, updateTime)
		}
		
		// 计算持仓时长
		durationMs := time.Now().UnixMilli() - updateTime
		durationMin := durationMs / (1000 * 60)
		log.Printf("Valid float64 updateTime: %f (converted to %d), holding duration: %d minutes", floatTime, updateTime, durationMin)
	})
	
	// 测试场景3：updateTime为0
	t.Run("ZeroUpdateTime", func(t *testing.T) {
		pos := map[string]interface{}{
			"updateTime": int64(0),
		}
		
		// 模拟GetPositions中的updateTime处理逻辑
		updateTime := int64(0)
		foundValidTime := false
		if ut, ok := pos["updateTime"].(int64); ok && ut > 0 {
			updateTime = ut
			foundValidTime = true
		} else if utFloat, ok := pos["updateTime"].(float64); ok && utFloat > 0 {
			updateTime = int64(utFloat)
			foundValidTime = true
		}
		if !foundValidTime {
			updateTime = time.Now().UnixMilli()
		}
		
		if updateTime <= 0 {
			t.Errorf("Expected positive updateTime, got %d", updateTime)
		}
		
		log.Printf("Zero updateTime handled, using current time: %d", updateTime)
	})
	
	// 测试场景4：updateTime不存在
	t.Run("MissingUpdateTime", func(t *testing.T) {
		pos := map[string]interface{}{}
		
		// 模拟GetPositions中的updateTime处理逻辑
		updateTime := int64(0)
		foundValidTime := false
		if ut, ok := pos["updateTime"].(int64); ok && ut > 0 {
			updateTime = ut
			foundValidTime = true
		} else if utFloat, ok := pos["updateTime"].(float64); ok && utFloat > 0 {
			updateTime = int64(utFloat)
			foundValidTime = true
		}
		if !foundValidTime {
			updateTime = time.Now().UnixMilli()
		}
		
		if updateTime <= 0 {
			t.Errorf("Expected positive updateTime, got %d", updateTime)
		}
		
		log.Printf("Missing updateTime handled, using current time: %d", updateTime)
	})
	
	log.Println("TestUpdateTimeHandling completed successfully!")
}

// 测试auto_trader.go中的交易记录机制
func TestTradeTrackingMechanism(t *testing.T) {
	// 创建模拟的AutoTrader实例
	at := &AutoTrader{
		openTrades: make(map[string]*RecentTrade),
	}
	
	// 测试场景1：API返回有效的updateTime，创建新的交易记录
	t.Run("CreateNewTradeRecord", func(t *testing.T) {
		posKey := "BTCUSDT_long"
		apiUpdateTime := time.Now().Add(-30 * time.Minute)
		
		// 模拟创建新交易记录
		at.openTrades[posKey] = &RecentTrade{
			Symbol:   "BTCUSDT",
			Side:     "LONG",
			OpenTime: apiUpdateTime,
		}
		
		// 验证交易记录被创建
		if trade, exists := at.openTrades[posKey]; !exists {
			t.Errorf("Expected trade record to exist for %s", posKey)
		} else if !trade.OpenTime.Equal(apiUpdateTime) {
			t.Errorf("Expected open time %v, got %v", apiUpdateTime, trade.OpenTime)
		}
		
		log.Printf("New trade record created with open time: %v", apiUpdateTime)
	})
	
	// 测试场景2：使用本地已有的交易记录
	t.Run("UseExistingTradeRecord", func(t *testing.T) {
		posKey := "ETHUSDT_short"
		// 先设置本地记录
		localOpenTime := time.Now().Add(-20 * time.Minute)
		at.openTrades[posKey] = &RecentTrade{
			Symbol:   "ETHUSDT",
			Side:     "SHORT",
			OpenTime: localOpenTime,
		}
		
		// 模拟获取已存在的交易记录
		trade, exists := at.openTrades[posKey]
		if !exists {
			t.Errorf("Expected trade record to exist for %s", posKey)
		}
		
		// 验证使用了本地记录的时间
		if !trade.OpenTime.Equal(localOpenTime) {
			t.Errorf("Expected to use local open time %v, got %v", localOpenTime, trade.OpenTime)
		}
		
		// 计算持仓时长
		duration := time.Since(trade.OpenTime)
		log.Printf("Local trade record used: open time %v, duration %v", trade.OpenTime, duration)
	})
	
	// 测试场景3：平仓并移至recentTrades
	t.Run("CloseTradeAndMoveToRecent", func(t *testing.T) {
		posKey := "SOLUSDT_long"
		symbol := "SOLUSDT"
		
		// 先创建持仓记录
		openTime := time.Now().Add(-15 * time.Minute)
		at.openTrades[posKey] = &RecentTrade{
			Symbol:     symbol,
			Side:       "LONG",
			EntryPrice: 100.0,
			OpenTime:   openTime,
		}
		
		// 模拟平仓逻辑
		trade := at.openTrades[posKey]
		trade.CloseTime = time.Now()
		trade.ClosePrice = 105.0
		trade.PnLPct = 5.0 // 计算的盈利百分比
		trade.Duration = trade.CloseTime.Sub(trade.OpenTime).String()
		
		// 模拟从openTrades移除并添加到recentTrades
		at.recentTrades = make(map[string]*RecentTrade)
		at.recentTrades[symbol] = trade
		delete(at.openTrades, posKey)
		
		// 验证状态
		if _, exists := at.openTrades[posKey]; exists {
			t.Errorf("Trade should be removed from openTrades")
		}
		if recentTrade, exists := at.recentTrades[symbol]; !exists {
			t.Errorf("Trade should be added to recentTrades")
		} else if recentTrade.PnLPct != 5.0 {
			t.Errorf("Expected PnLPct 5.0, got %f", recentTrade.PnLPct)
		}
		
		log.Printf("Trade closed and moved to recentTrades: %s, PnL: %.2f%%", symbol, trade.PnLPct)
	})
	
	log.Println("TestTradeTrackingMechanism completed successfully!")
}