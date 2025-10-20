# ritmex-ai-trader

## 项目简介 · Project Overview

`ritmex-ai-trader` 是一个多智能体（Multi-Agent）自动交易实验平台，聚焦于加密资产高频/中频策略。整套系统把行情采集、特征工程、信号生成、风险控制、执行路由以及合规审计拆分成独立 Agent，通过 JSON 消息总线和共享状态存储协同工作。  
`ritmex-ai-trader` is a multi-agent trading research platform for crypto markets. Market data ingestion, feature engineering, signal generation, portfolio & risk control, execution, and compliance reporting live in separate agents that communicate via an internal JSON message bus and shared state stores.

方案灵感来自 [https://nof1.ai/](https://nof1.ai/) 的 Agent 架构，我们在其思路基础上补充量化交易需要的指标计算、风控和审计流程。  
The architecture is influenced by [https://nof1.ai/](https://nof1.ai/), extended with quantitative analytics, risk management, and audit features required for live trading.

## 使用的主要库 · Key Libraries

- Bun v1.2.20：统一运行时与打包工具，直接执行 TypeScript。  
  Bun v1.2.20 for runtime and bundling with native TypeScript support.
- `pinets`：拉取 Binance K 线并复用 PineScript 指标库。  
  `pinets` to source Binance market data and PineScript-compatible TA functions.
- `ai` / `@ai-sdk/google`：通过 Gemini/GPT 等模型生成交易决策说明。  
  `ai` and `@ai-sdk/google` for LLM-backed narrative and signal validation.
- `zod`：定义并校验 Agent 间的消息结构。  
  `zod` for schema validation across agent boundaries.
- `dotenv`：加载运行时配置。  
  `dotenv` to manage environment configuration.

## 模块划分 · Module Breakdown

1. 行情采集（Market Data Ingestion）  
   连接交易所 API / WebSocket，规整为统一的 K 线/逐笔格式并写入快照库。

2. 特征工程（Feature Engineering）  
   计算 EMA、RSI、ATR 等技术指标以及自定义统计特征，输出给信号模块。

3. 信号生成（Signal Generation）  
   调用模型或启发式逻辑，生成 `signal.long` / `signal.short` / `signal.flat` 等事件。

4. 组合与风控（Portfolio & Risk）  
   将信号映射为目标仓位、杠杆和资金分配，并执行回撤/风险限额检查。

5. 执行路由（Execution）  
   按照 TWAP/VWAP 或智能路由策略把目标仓位拆单下到场内。

6. 监督与健康监控（Supervisor）  
   统一监控 Agent 心跳、延迟与 SLA，触发熔断或恢复动作。

7. 合规与报表（Compliance & Reporting）  
   汇总交易日志、风控豁免、配置差异，生成可留存的审计报表。

Each responsibility is encapsulated in a dedicated agent so teams can iterate independently without breaking data contracts.

## 系统工作流 · High-Level Workflow

1. **数据准备 · Data Preparation**  
   - 通过 `pinets` 抓取最新 K 线，并同步到 SQLite/DuckDB 快照。  
   - 维护 Redis 缓存供实时指标消费。

2. **特征计算 · Feature Computation**  
   - 计算滚动指标（EMA、RSI、ATR 等）和自定义特征。  
   - 进行异常过滤（缺失值、假量等）。

3. **信号决策 · Signal Decision**  
   - 运行 LLM/Stat 模型进行多空判断，落到 `desiredPosition` 和 `action`。  
   - 若模型不可用则回退至本地 EMA/RSI 启发式。

4. **风险与执行 · Risk & Execution**  
   - 依据账户状态校验杠杆、回撤、持仓集中度。  
   - 生成执行指令并调用交易平台或模拟器。

5. **监控与审计 · Monitoring & Audit**  
   - Supervisor 监控延迟、失败情况，触发熔断或重启。  
   - 合规模块记录交易与配置变更，输出日报/周报。

## 快速开始 · Getting Started

1. 安装依赖 · Install dependencies

   ```bash
   bun install
   ```

2. 准备配置 · Prepare configuration

   ```bash
   cp env.example .env
   # 编辑 .env 填写 OKX/LLM 等密钥
   ```

3. 启动主循环 · Run the main loop

   ```bash
   bun run index.ts
   ```

> 说明：默认运行在 Dry-Run 模式，仅打印信号，不会向交易所发送真实订单。  
> Notes: The default configuration is dry-run; signals are logged but not dispatched to venues.

## 后续路线 · Next Steps

- 对接实际交易账户并完善订单回报处理。  
  Integrate with live brokerage APIs and add fill reconciliation.
- 扩展指标和宏观数据源，完善模型验证流水线。  
  Enrich feature sets with macro feeds and build regression tests for indicators.
- 增加端到端回测与模拟撮合模块。  
  Add historical backtesting and simulated matching to validate strategies offline.
