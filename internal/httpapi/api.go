// Package httpapi 提供费用分摊结算服务的 HTTP 接口。
package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"task016-splitbill/internal/splitbill"
)

// ErrBadJSON 表示请求体不是单个合法 JSON 对象。
var ErrBadJSON = errors.New("请求体不是合法的单个 JSON 对象")

// API 是费用分摊服务的 HTTP 接口实现。
type API struct {
	store *splitbill.Store
}

// New 创建使用给定内存存储的服务实例。
func New(store *splitbill.Store) *API {
	return &API{store: store}
}

// Handler 返回 HTTP 路由。
func (a *API) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", a.health)
	mux.HandleFunc("POST /groups", a.createGroup)
	mux.HandleFunc("POST /groups/{id}/bills", a.addBill)
	mux.HandleFunc("GET /groups/{id}/balance", a.balance)
	mux.HandleFunc("GET /groups/{id}/settlement", a.settlement)
	return mux
}

func decodeJSON(r *http.Request, v any) error {
	if r.Body == nil {
		return ErrBadJSON
	}
	dec := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("%w: %v", ErrBadJSON, err)
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return ErrBadJSON
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func (a *API) health(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

type createGroupRequest struct {
	Members []string `json:"members"`
}

func (a *API) createGroup(w http.ResponseWriter, r *http.Request) {
	var req createGroupRequest
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error(), "status": http.StatusBadRequest})
		return
	}
	if len(req.Members) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "缺少 members 字段或为空", "status": http.StatusBadRequest})
		return
	}
	id, err := a.store.CreateGroup(req.Members)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error(), "status": http.StatusBadRequest})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"group_id": id})
}

type participantJSON struct {
	Name   string `json:"name"`
	Weight int64  `json:"weight,omitempty"`
	Fixed  *int64 `json:"fixed,omitempty"`
}

type addBillRequest struct {
	Payer        string            `json:"payer"`
	Amount       int64             `json:"amount"`
	Mode         splitbill.Mode    `json:"mode"`
	Participants []participantJSON `json:"participants"`
}

type shareJSON struct {
	Name   string `json:"name"`
	Amount int64  `json:"amount"`
}

func (a *API) addBill(w http.ResponseWriter, r *http.Request) {
	var req addBillRequest
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error(), "status": http.StatusBadRequest})
		return
	}
	if req.Payer == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "缺少 payer 字段", "status": http.StatusBadRequest})
		return
	}
	if req.Mode == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "缺少 mode 字段", "status": http.StatusBadRequest})
		return
	}
	parts := make([]splitbill.ParticipantInput, len(req.Participants))
	for i, p := range req.Participants {
		parts[i] = splitbill.ParticipantInput{Name: p.Name, Weight: p.Weight, Fixed: p.Fixed}
	}
	groupID := r.PathValue("id")
	bill, err := a.store.AddBill(groupID, req.Payer, req.Amount, req.Mode, parts)
	if err != nil {
		writeJSON(w, statusFor(err), map[string]any{"error": err.Error(), "status": statusFor(err)})
		return
	}
	shares := make([]shareJSON, len(bill.Shares))
	for i, s := range bill.Shares {
		shares[i] = shareJSON{Name: s.Name, Amount: s.AmountCents}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"bill_id": bill.ID,
		"shares":  shares,
	})
}

type balanceItem struct {
	Name string `json:"name"`
	Net  int64  `json:"net"`
}

func (a *API) balance(w http.ResponseWriter, r *http.Request) {
	groupID := r.PathValue("id")
	bal, err := a.store.Balance(groupID)
	if err != nil {
		writeJSON(w, statusFor(err), map[string]any{"error": err.Error(), "status": statusFor(err)})
		return
	}
	// 按成员名称排序，保证输出稳定可测。
	items := make([]balanceItem, 0, len(bal))
	for name, net := range bal {
		items = append(items, balanceItem{Name: name, Net: net})
	}
	// 简单选择排序按名称升序，避免引入 sort 依赖歧义；成员数小。
	for i := 0; i < len(items); i++ {
		for j := i + 1; j < len(items); j++ {
			if items[j].Name < items[i].Name {
				items[i], items[j] = items[j], items[i]
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"balances": items})
}

type transferJSON struct {
	From   string `json:"from"`
	To     string `json:"to"`
	Amount int64  `json:"amount"`
}

func (a *API) settlement(w http.ResponseWriter, r *http.Request) {
	groupID := r.PathValue("id")
	transfers, err := a.store.Settlement(groupID)
	if err != nil {
		writeJSON(w, statusFor(err), map[string]any{"error": err.Error(), "status": statusFor(err)})
		return
	}
	out := make([]transferJSON, 0, len(transfers))
	for _, tr := range transfers {
		out = append(out, transferJSON{From: tr.From, To: tr.To, Amount: tr.AmountCents})
	}
	writeJSON(w, http.StatusOK, map[string]any{"transfers": out})
}

// statusFor 根据领域错误映射 HTTP 状态码：组不存在为 404，其余输入错误为 400。
func statusFor(err error) int {
	if errors.Is(err, splitbill.ErrGroupNotFound) {
		return http.StatusNotFound
	}
	return http.StatusBadRequest
}
