# 交易对特定杠杆配置功能

## 功能说明

本系统现已支持针对同一DEX中不同交易对设置不同杠杆率的功能。通过此功能，您可以为特定交易对设置自定义杠杆倍数，从而实现更精细化的风险控制。

## 杠杆配置优先级

系统在确定交易对的最大杠杆率时，按以下优先级从高到低应用：

1. **交易对特定配置** (`pair_leverage`): 如果在配置中为特定交易对设置了杠杆值，则优先使用此值
2. **币种类型配置**:
   - BTC/ETH交易对: 使用`btc_eth_leverage`配置
   - 其他山寨币: 使用`altcoin_leverage`配置

## 配置示例

### 全局杠杆配置

在配置文件根级别，可以设置全局杠杆默认值：

```json
"leverage": {
  "btc_eth_leverage": 50,
  "altcoin_leverage": 20,
  "pair_leverage": {
    "SOLUSDT": 15,
    "BNBUSDT": 25,
    "DOGEUSDT": 10
  }
},
```

### 交易员级别杠杆配置

每个交易员可以覆盖全局配置，设置自己的杠杆策略：

```json
{
  "id": "example_trader",
  "name": "示例交易员",
  "enabled": true,
  "ai_model": "qwen",
  "exchange": "binance",
  "btc_eth_leverage": 10,
  "altcoin_leverage": 8,
  "pair_leverage": {
    "SOLUSDT": 12,
    "BNBUSDT": 15,
    "DOGEUSDT": 6,
    "XRPUSDT": 10
  },
  // 其他配置...
}
```

## 实现细节

1. 在`config/config.go`中添加了`PairLeverage map[string]int`字段到`LeverageConfig`和`TraderConfig`结构体

2. 在`decision/engine.go`中更新了验证逻辑，优先检查交易对特定杠杆配置

3. 系统会在应用杠杆时输出日志，记录使用的杠杆配置类型，例如：
   ```
   💡 使用特定交易对杠杆配置: SOLUSDT 设置为15倍
   ```

## 使用建议

1. 对于高波动性的小市值币种，可以设置较低的杠杆率
2. 对于主流稳定币种，可以设置较高的杠杆率
3. 交易员级别配置会覆盖全局配置，可以针对不同AI模型或交易策略设置不同的杠杆策略
4. 杠杆值设置为0或负数时，系统会自动修正为1倍

## 注意事项

- 所有杠杆配置最终都会受到交易所实际支持的最大杠杆限制
- 杠杆修正逻辑会自动将过高的杠杆值调整为配置的最大值
- 仓位大小验证仍然基于币种类型（BTC/ETH vs 山寨币）的仓位价值上限