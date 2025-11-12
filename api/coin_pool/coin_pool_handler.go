package coin_pool

import (
	"encoding/json"
	"fmt"
	"net/http"
)

// CoinPoolHandler 处理coin_pool相关的API请求
type CoinPoolHandler struct {
	// 添加service依赖
	service *CoinPoolService
}

// NewCoinPoolHandler 创建新的CoinPoolHandler实例
func NewCoinPoolHandler() *CoinPoolHandler {
	return &CoinPoolHandler{
		service: NewCoinPoolService(),
	}
}

// CoinPoolResponse 定义coin_pool API的标准响应格式
type CoinPoolResponse struct {
	Code int                    `json:"code"`
	Msg  string                 `json:"msg"`
	Data map[string]interface{} `json:"data"`
}

// CoinPoolItem 定义coin_pool中的单个项目结构
type CoinPoolItem struct {
	CoinName    string  `json:"coin_name"`
	Symbol      string  `json:"symbol"`
	Price       float64 `json:"price"`
	ChangeRate  float64 `json:"change_rate"`
	Volume      float64 `json:"volume"`
	QuoteVolume float64 `json:"quote_volume"`
	Rank        int     `json:"rank"`
}

// OITopItem 定义oi-top中的单个项目结构
type OITopItem struct {
	CoinName   string  `json:"coin_name"`
	Symbol     string  `json:"symbol"`
	Price      float64 `json:"price"`
	ChangeRate float64 `json:"change_rate"`
	OI         float64 `json:"oi"`
	OIRank     int     `json:"oi_rank"`
	// 可以根据需要添加更多字段
}

// HandleGetCoinPoolAI500 处理获取AI500币池数据的请求
func (h *CoinPoolHandler) HandleGetCoinPoolAI500(w http.ResponseWriter, r *http.Request) {
	// 获取AI500币池数据
	coinList, err := h.service.GetAI500CoinPool()
	if err != nil {
		// 返回错误信息，不再使用模拟数据
		fmt.Printf("Error fetching AI500 coin pool: %v\n", err)
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(fmt.Sprintf(`{\"error\": \"%v\"}`, err)))
		return
	}

	// 设置响应头
	w.Header().Set("Content-Type", "application/json")

	// 返回JSON数据（包含success和data字段）
	response := map[string]interface{}{
		"success": true,
		"data":    coinList,
	}
	if err := json.NewEncoder(w).Encode(response); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{\"error\": \"Failed to encode response\"}`))
	}
}

// HandleGetCoinPoolOITop 处理获取持仓量排行榜数据的请求
func (h *CoinPoolHandler) HandleGetCoinPoolOITop(w http.ResponseWriter, r *http.Request) {
	// 获取持仓量排行榜数据
	coinList, err := h.service.GetOITopCoinPool()
	if err != nil {
		// 返回错误信息，不再使用模拟数据
		fmt.Printf("Error fetching OI top coin pool: %v\n", err)
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(fmt.Sprintf(`{\"error\": \"%v\"}`, err)))
		return
	}

	// 设置响应头
	w.Header().Set("Content-Type", "application/json")

	// 返回JSON数据（包含success和data字段）
	response := map[string]interface{}{
		"success": true,
		"data":    coinList,
	}
	if err := json.NewEncoder(w).Encode(response); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{\"error\": \"Failed to encode response\"}`))
	}
}

// writeJSONResponse 将响应写入HTTP响应体
func (h *CoinPoolHandler) writeJSONResponse(w http.ResponseWriter, response CoinPoolResponse) {
	w.Header().Set("Content-Type", "application/json")
	err := json.NewEncoder(w).Encode(response)
	if err != nil {
		http.Error(w, "Failed to encode response", http.StatusInternalServerError)
	}
}

// RegisterRoutes 注册coin_pool相关的路由
func RegisterRoutes(mux *http.ServeMux, handler *CoinPoolHandler) {
	mux.HandleFunc("/api/coin-pool/ai500", handler.HandleGetCoinPoolAI500)
	mux.HandleFunc("/api/coin-pool/oi-top", handler.HandleGetCoinPoolOITop)
}