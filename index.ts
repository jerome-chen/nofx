import { config as loadEnv } from "dotenv";
import { PineTS, Provider } from "pinets";
import { generateObject } from "ai";
import { google } from "@ai-sdk/google";
import { z } from "zod";

loadEnv();

enum PositionState {
  EMPTY = "EMPTY",
  LONG = "LONG",
  SHORT = "SHORT",
}

enum ActionState {
  ENTER_LONG = "ENTER_LONG",
  EXIT_LONG = "EXIT_LONG",
  ENTER_SHORT = "ENTER_SHORT",
  EXIT_SHORT = "EXIT_SHORT",
}

type NullableAction = ActionState | null;

const DECISION_SCHEMA = z.object({
  marketCommentary: z
    .string()
    .describe("Brief human-readable market outlook, preferably Chinese."),
  desiredPosition: z
    .nativeEnum(PositionState)
    .describe("Position state the agent would like to hold after this decision."),
  action: z
    .nativeEnum(ActionState)
    .nullable()
    .describe(
      "Trading action to perform this minute. Null if no position change is required."
    ),
  confidence: z
    .number()
    .min(0)
    .max(1)
    .describe("Model confidence in the action or lack thereof."),
  rationale: z
    .string()
    .describe("Key quantitative/qualitative drivers supporting the decision."),
  riskNotice: z
    .string()
    .describe("Risk notes and contingency plans for the next interval."),
});

type TradingDecision = z.infer<typeof DECISION_SCHEMA>;

interface MarketFeatures {
  symbol: string;
  timeframe: string;
  timestamp: string;
  close: number | null;
  open: number | null;
  high: number | null;
  low: number | null;
  volume: number | null;
  emaFast: number | null;
  emaSlow: number | null;
  emaTrendBias: "BULLISH" | "BEARISH" | "NEUTRAL";
  rsi: number | null;
  atr: number | null;
  priceChangePct: number | null;
}

interface TradingSignalPayload {
  action: ActionState;
  instrument: string;
  signalToken: string;
  timestamp: string;
  maxLag: string;
  orderType: string;
  orderPriceOffset: string;
  investmentType: string;
  amount: string;
}

const CONFIG = {
  dataSymbol: process.env.DATA_SYMBOL ?? "BTCUSDT",
  signalInstrument: process.env.SIGNAL_INSTRUMENT ?? "BTC-USDT-SWAP",
  timeframe: process.env.TIMEFRAME ?? "1", // '1' is 1 minute in pinets
  lookback: Number.parseInt(process.env.LOOKBACK ?? "200", 10),
  aiModel: process.env.AI_MODEL ?? "gemini-2.0-flash",
  iterationIntervalMs: Number.parseInt(
    process.env.ITERATION_INTERVAL_MS ?? `${60_000}`,
    10
  ),
  signalToken: process.env.OKX_SIGNAL_TOKEN ?? "your-signaltoken-here",
  maxLag: process.env.SIGNAL_MAX_LAG ?? "300",
  orderType: process.env.ORDER_TYPE ?? "market",
  orderPriceOffset: process.env.ORDER_PRICE_OFFSET ?? "",
  entryInvestmentType: process.env.ENTRY_INVESTMENT_TYPE ?? "percentage_balance",
  exitInvestmentType: process.env.EXIT_INVESTMENT_TYPE ?? "percentage_position",
  amount: process.env.ORDER_AMOUNT ?? "100",
  tradingUrl:
    process.env.TRADING_URL ??
    "https://www.okx.com/pap/algo/signal/trigger", // default paper endpoint
  dryRun: (process.env.DRY_RUN ?? "true").toLowerCase() !== "false",
};

let currentPosition: PositionState = PositionState.EMPTY;

const pineTS = new PineTS(
  Provider.Binance,
  CONFIG.dataSymbol,
  CONFIG.timeframe,
  CONFIG.lookback
);

function extractLatest(result: Record<string, unknown>, key: string): number | null {
  const series = result?.[key] as unknown;
  if (Array.isArray(series)) {
    for (let i = series.length - 1; i >= 0; i--) {
      const candidate = series[i];
      if (typeof candidate === "number" && Number.isFinite(candidate)) {
        return candidate;
      }
    }
  } else if (typeof series === "number" && Number.isFinite(series)) {
    return series;
  }
  return null;
}

function computeTrendBias(
  emaFast: number | null,
  emaSlow: number | null
): MarketFeatures["emaTrendBias"] {
  if (emaFast == null || emaSlow == null) {
    return "NEUTRAL";
  }
  if (emaFast > emaSlow) {
    return "BULLISH";
  }
  if (emaFast < emaSlow) {
    return "BEARISH";
  }
  return "NEUTRAL";
}

function percentChange(current: number | null, previous: number | null): number | null {
  if (
    current == null ||
    previous == null ||
    !Number.isFinite(current) ||
    !Number.isFinite(previous) ||
    previous === 0
  ) {
    return null;
  }
  return ((current - previous) / previous) * 100;
}

function formatNumber(value: number | null, digits = 2): string {
  if (typeof value !== "number" || !Number.isFinite(value)) {
    return "NA";
  }
  return value.toFixed(digits);
}

async function fetchMarketFeatures(): Promise<MarketFeatures> {
  const context = await pineTS.run(($: any) => {
    const emaFast = $.ta.ema($.data.close, 9);
    const emaSlow = $.ta.ema($.data.close, 21);
    const rsi = $.ta.rsi($.data.close, 14);
    const atr = $.ta.atr($.data.high, $.data.low, $.data.close, 14);

    return {
      emaFast,
      emaSlow,
      rsi,
      atr,
    };
  }, 64);

  const { result } = context as { result: Record<string, unknown> };

  const emaFast = extractLatest(result, "emaFast");
  const emaSlow = extractLatest(result, "emaSlow");
  const rsi = extractLatest(result, "rsi");
  const atr = extractLatest(result, "atr");

  const closeSeries = (pineTS as any).close as number[] | undefined;
  const openSeries = (pineTS as any).open as number[] | undefined;
  const highSeries = (pineTS as any).high as number[] | undefined;
  const lowSeries = (pineTS as any).low as number[] | undefined;
  const volumeSeries = (pineTS as any).volume as number[] | undefined;
  const closeTimes = (pineTS as any).closeTime as number[] | undefined;

  const latestIdx = closeSeries && closeSeries.length > 0 ? closeSeries.length - 1 : -1;
  const prevIdx = latestIdx > 0 ? latestIdx - 1 : -1;

  const close = latestIdx >= 0 ? closeSeries?.[latestIdx] ?? null : null;
  const open = latestIdx >= 0 ? openSeries?.[latestIdx] ?? null : null;
  const high = latestIdx >= 0 ? highSeries?.[latestIdx] ?? null : null;
  const low = latestIdx >= 0 ? lowSeries?.[latestIdx] ?? null : null;
  const volume = latestIdx >= 0 ? volumeSeries?.[latestIdx] ?? null : null;
  const prevClose =
    prevIdx >= 0 && closeSeries ? (closeSeries?.[prevIdx] ?? null) : null;

  const timestamp =
    latestIdx >= 0 && closeTimes && closeTimes[latestIdx]
      ? new Date(closeTimes[latestIdx]).toISOString()
      : new Date().toISOString();

  return {
    symbol: CONFIG.dataSymbol,
    timeframe: CONFIG.timeframe,
    timestamp,
    close: close ?? null,
    open: open ?? null,
    high: high ?? null,
    low: low ?? null,
    volume: volume ?? null,
    emaFast,
    emaSlow,
    emaTrendBias: computeTrendBias(emaFast, emaSlow),
    rsi,
    atr,
    priceChangePct: percentChange(close ?? null, prevClose),
  };
}

function describeStateSnapshot(
  features: MarketFeatures,
  state: PositionState
): string {
  const payload = {
    time: features.timestamp,
    state,
    price: features.close,
    changePct: features.priceChangePct,
    emaFast: features.emaFast,
    emaSlow: features.emaSlow,
    trend: features.emaTrendBias,
    rsi: features.rsi,
    atr: features.atr,
    volume: features.volume,
  };
  return JSON.stringify(payload, (_, value) =>
    typeof value === "number" ? Number(value.toFixed(6)) : value
  );
}

async function requestDecision(
  features: MarketFeatures,
  state: PositionState
): Promise<TradingDecision> {
  const prompt = [
    "你是一个加密货币高频交易的AI决策模块，需要根据实时输入的市场特征和当前仓位给出下一分钟的操作建议。",
    "规则：",
    "1. desiredPosition 只能是 EMPTY、LONG 或 SHORT。",
    "2. action 只能是 ENTER_LONG、EXIT_LONG、ENTER_SHORT、EXIT_SHORT；如果无需操作，返回 null。",
    "3. 如果 action 不为 null，其含义必须能把当前仓位推进到 desiredPosition。",
    "4. marketCommentary 用中文简报当前市场形势，可 1-2 句。",
    "5. rationale 说明触发该判断的关键因子，例如均线、RSI 等。",
    "6. riskNotice 提示潜在风险或需观察的指标。",
    "输入数据（JSON）：",
    describeStateSnapshot(features, state),
    "请严格按照对象模式返回答案。",
  ].join("\n");

  const { object } = await generateObject({
    model: google(CONFIG.aiModel),
    schemaName: "TradingDecision",
    schemaDescription: "AI交易模块输出的标准化决策。",
    schema: DECISION_SCHEMA,
    prompt,
  });

  return object;
}

function fallbackDecision(
  features: MarketFeatures,
  state: PositionState
): TradingDecision {
  const { emaFast, emaSlow, rsi } = features;
  let desiredPosition = state;
  let action: NullableAction = null;

  if (emaFast != null && emaSlow != null) {
    if (emaFast > emaSlow * 1.001 && (rsi == null || rsi < 70)) {
      desiredPosition = PositionState.LONG;
    } else if (emaFast < emaSlow * 0.999 && (rsi == null || rsi > 30)) {
      desiredPosition = PositionState.SHORT;
    } else if (rsi != null && (rsi > 75 || rsi < 25)) {
      desiredPosition = PositionState.EMPTY;
    }
  }

  if (desiredPosition !== state) {
    if (desiredPosition === PositionState.LONG) {
      action = state === PositionState.SHORT ? ActionState.EXIT_SHORT : ActionState.ENTER_LONG;
    } else if (desiredPosition === PositionState.SHORT) {
      action = state === PositionState.LONG ? ActionState.EXIT_LONG : ActionState.ENTER_SHORT;
    } else {
      action = state === PositionState.LONG ? ActionState.EXIT_LONG : ActionState.EXIT_SHORT;
    }
  }

  const commentary =
    features.emaTrendBias === "BULLISH"
      ? "均线呈现多头排列，做多动能占优。"
      : features.emaTrendBias === "BEARISH"
      ? "均线呈现空头排列，做空动能偏强。"
      : "均线信号模糊，保持谨慎。";

  const emaFastText =
    features.emaFast != null ? features.emaFast.toFixed(2) : "NA";
  const emaSlowText =
    features.emaSlow != null ? features.emaSlow.toFixed(2) : "NA";
  const rsiText = features.rsi != null ? `RSI=${features.rsi.toFixed(1)}` : "";
  const rationale = `emaFast=${emaFastText} emaSlow=${emaSlowText} ${rsiText}`.trim();

  return DECISION_SCHEMA.parse({
    marketCommentary: commentary,
    desiredPosition,
    action,
    confidence: 0.4,
    rationale,
    riskNotice: "该判断来自本地启发式回退策略，需关注下一笔行情波动。",
  });
}

function resolveNextPosition(
  prev: PositionState,
  action: NullableAction
): PositionState {
  if (!action) {
    return prev;
  }

  switch (action) {
    case ActionState.ENTER_LONG:
      return PositionState.LONG;
    case ActionState.EXIT_LONG:
      return PositionState.EMPTY;
    case ActionState.ENTER_SHORT:
      return PositionState.SHORT;
    case ActionState.EXIT_SHORT:
      return PositionState.EMPTY;
    default:
      return prev;
  }
}

function buildSignalPayload(
  action: ActionState,
  timestamp: string
): TradingSignalPayload {
  const isEntry =
    action === ActionState.ENTER_LONG || action === ActionState.ENTER_SHORT;
  return {
    action,
    instrument: CONFIG.signalInstrument,
    signalToken: CONFIG.signalToken,
    timestamp,
    maxLag: CONFIG.maxLag,
    orderType: CONFIG.orderType,
    orderPriceOffset: CONFIG.orderPriceOffset,
    investmentType: isEntry
      ? CONFIG.entryInvestmentType
      : CONFIG.exitInvestmentType,
    amount: CONFIG.amount,
  };
}

async function dispatchSignal(signal: TradingSignalPayload): Promise<void> {
  if (CONFIG.dryRun) {
    console.log(`[DRY-RUN] 信号已生成但未发送: ${JSON.stringify(signal)}`);
    return;
  }
  try {
    const response = await fetch(CONFIG.tradingUrl, {
      method: "POST",
      headers: {
        "Content-Type": "application/json",
      },
      body: JSON.stringify(signal),
    });
    if (!response.ok) {
      const text = await response.text();
      console.error(
        `发送信号失败: HTTP ${response.status} ${response.statusText} - ${text}`
      );
    } else {
      console.log("信号推送成功。");
    }
  } catch (error) {
    console.error("推送信号时出现错误。", error);
  }
}

async function evaluateOnce(): Promise<void> {
  const features = await fetchMarketFeatures();
  let decision: TradingDecision;

  console.log(`[${features.timestamp}] 市场特征:`);
  console.log(
    `  价格: close=${formatNumber(features.close)} open=${formatNumber(
      features.open
    )} high=${formatNumber(features.high)} low=${formatNumber(features.low)}`
  );
  console.log(
    `  指标: EMA(9)=${formatNumber(features.emaFast)} EMA(21)=${formatNumber(
      features.emaSlow
    )} RSI=${formatNumber(features.rsi)} ATR=${formatNumber(features.atr)}`
  );
  console.log(
    `  体量: volume=${formatNumber(features.volume, 0)} priceChangePct=${formatNumber(
      features.priceChangePct
    )}`
  );

  try {
    decision = await requestDecision(features, currentPosition);
  } catch (error) {
    console.error(
      "调用AI决策失败，使用本地回退逻辑。",
      (error as Error).message ?? error
    );
    decision = fallbackDecision(features, currentPosition);
  }

  console.log(
    `[${features.timestamp}] 市场评论: ${decision.marketCommentary} | 当前仓位: ${currentPosition} -> 目标仓位: ${decision.desiredPosition} | 置信度: ${decision.confidence.toFixed(
      2
    )}`
  );
  console.log(`决策依据: ${decision.rationale}`);
  console.log(`风险提示: ${decision.riskNotice}`);

  if (decision.action) {
    const validTransition =
      resolveNextPosition(currentPosition, decision.action) ===
      decision.desiredPosition ||
      decision.desiredPosition === currentPosition;

    if (!validTransition) {
      console.warn(
        `AI 生成的 action(${decision.action}) 与 desiredPosition(${decision.desiredPosition}) 不匹配，忽略此操作。`
      );
    } else {
      const signalPayload = buildSignalPayload(
        decision.action,
        features.timestamp
      );
      await dispatchSignal(signalPayload);
      currentPosition = resolveNextPosition(currentPosition, decision.action);
    }
  } else {
    currentPosition = decision.desiredPosition;
  }
}

async function main(): Promise<void> {
  if (!process.env.GOOGLE_GENERATIVE_AI_API_KEY) {
    console.warn(
      "未检测到 GOOGLE_API_KEY，将在 AI 请求失败时回退到本地策略。"
    );
  }

  console.log(
    `启动交易机器人：symbol=${CONFIG.dataSymbol} timeframe=${CONFIG.timeframe} 当前仓位=${currentPosition} dryRun=${CONFIG.dryRun}`
  );

  try {
    await pineTS.ready();
    console.log("市场数据初始化完成。");
  } catch (error) {
    console.error(
      "初始化市场数据失败，将使用空数据启动。稍后评估时会尝试继续拉取。",
      error
    );
  }

  while (true) {
    try {
      await evaluateOnce();
    } catch (error) {
      console.error("执行单次评估出现错误：", error);
    }
    await Bun.sleep(CONFIG.iterationIntervalMs);
  }
}

await main();
