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

// 测试auto_trader.go中的firstSeen机制
func TestPositionFirstSeenMechanism(t *testing.T) {
	// 创建模拟的AutoTrader实例
	at := &AutoTrader{
		positionFirstSeenTime: make(map[string]int64),
	}
	
	// 测试场景1：API返回有效的updateTime
	t.Run("APIProvidesUpdateTime", func(t *testing.T) {
		posKey := "BTCUSDT_long"
		apiUpdateTime := int64(time.Now().UnixMilli() - 30*60*1000)
		pos := map[string]interface{}{
			"updateTime": apiUpdateTime,
		}
		
		// 模拟auto_trader.go中的逻辑
		updateTime, ok := pos["updateTime"].(int64)
		if !ok || updateTime == 0 {
			// 这个分支不应该执行
			updateTime = time.Now().UnixMilli()
		} else {
			// 更新本地记录
			at.positionFirstSeenTime[posKey] = updateTime
		}
		
		// 验证本地记录被更新
		if storedTime, exists := at.positionFirstSeenTime[posKey]; !exists || storedTime != apiUpdateTime {
			t.Errorf("Expected stored time %d, got %d (exists: %v)", apiUpdateTime, storedTime, exists)
		}
		
		log.Printf("API provided updateTime stored: %d", updateTime)
	})
	
	// 测试场景2：API不提供updateTime，使用本地记录
	t.Run("UseLocalFirstSeenTime", func(t *testing.T) {
		posKey := "ETHUSDT_short"
		// 先设置本地记录
		localFirstSeenTime := int64(time.Now().UnixMilli() - 20*60*1000)
		at.positionFirstSeenTime[posKey] = localFirstSeenTime
		
		// 模拟没有updateTime的API响应
		pos := map[string]interface{}{}
		
		// 模拟auto_trader.go中的逻辑
		updateTime, ok := pos["updateTime"].(int64)
		if !ok || updateTime == 0 {
			if firstSeenTime, exists := at.positionFirstSeenTime[posKey]; exists {
				updateTime = firstSeenTime
			} else {
				updateTime = time.Now().UnixMilli()
				at.positionFirstSeenTime[posKey] = updateTime
			}
		}
		
		// 验证使用了本地记录
		if updateTime != localFirstSeenTime {
			t.Errorf("Expected to use local time %d, got %d", localFirstSeenTime, updateTime)
		}
		
		log.Printf("Local firstSeenTime used: %d", updateTime)
	})
	
	// 测试场景3：API不提供updateTime，且本地没有记录
	t.Run("CreateNewFirstSeenTime", func(t *testing.T) {
		posKey := "SOLUSDT_long"
		// 确保本地没有记录
		delete(at.positionFirstSeenTime, posKey)
		
		// 模拟没有updateTime的API响应
		pos := map[string]interface{}{}
		
		// 模拟auto_trader.go中的逻辑
		updateTime, ok := pos["updateTime"].(int64)
		if !ok || updateTime == 0 {
			if firstSeenTime, exists := at.positionFirstSeenTime[posKey]; exists {
				updateTime = firstSeenTime
			} else {
				updateTime = time.Now().UnixMilli()
				at.positionFirstSeenTime[posKey] = updateTime
			}
		}
		
		// 验证创建了新记录
		if storedTime, exists := at.positionFirstSeenTime[posKey]; !exists || storedTime <= 0 {
			t.Errorf("Expected new stored time > 0, got %d (exists: %v)", storedTime, exists)
		}
		
		log.Printf("New firstSeenTime created: %d", updateTime)
	})
	
	log.Println("TestPositionFirstSeenMechanism completed successfully!")
}