package api

import (
	"net/http"
	"strconv"

	"coffeechain/internal/auth"
	"coffeechain/internal/service"
)

// /api/stores/{code}/board|queue|events|acceptance|inventory|replenish
func (s *Server) storeSub(w http.ResponseWriter, r *http.Request) {
	rest := r.URL.Path[len("/api/stores/"):]
	// code/action 形式
	var code, action string
	for i := 0; i < len(rest); i++ {
		if rest[i] == '/' {
			code, action = rest[:i], rest[i+1:]
			break
		}
	}
	if code == "" {
		auth.WriteError(w, 400, "缺少门店编码")
		return
	}
	storeID, err := s.app.ResolveStore(r.Context(), code)
	if err != nil {
		service.WriteBizError(w, err)
		return
	}
	if !s.checkStoreScope(w, r, storeID) {
		return
	}
	switch action {
	case "board":
		b, err := s.app.GetBoard(r.Context(), storeID)
		if err != nil {
			auth.WriteError(w, 500, err.Error())
			return
		}
		auth.WriteOK(w, b)
	case "events":
		limit, _ := strconv.ParseInt(r.URL.Query().Get("limit"), 10, 64)
		if limit <= 0 || limit > 50 {
			limit = 20
		}
		evs, err := s.app.GetEvents(r.Context(), storeID, limit)
		if err != nil {
			auth.WriteError(w, 500, err.Error())
			return
		}
		auth.WriteOK(w, evs)
	case "queue":
		list, err := s.app.StaffQueue(r.Context(), storeID)
		if err != nil {
			auth.WriteError(w, 500, err.Error())
			return
		}
		auth.WriteOK(w, list)
	case "inventory":
		list, err := s.app.ListInventory(r.Context(), storeID)
		if err != nil {
			auth.WriteError(w, 500, err.Error())
			return
		}
		auth.WriteOK(w, list)
	case "losses":
		list, err := s.app.ListLosses(r.Context(), storeID)
		if err != nil {
			auth.WriteError(w, 500, err.Error())
			return
		}
		auth.WriteOK(w, list)
	case "acceptance":
		s.setAcceptance(w, r, storeID)
	case "replenish":
		s.replenish(w, r, storeID)
	default:
		auth.WriteError(w, 404, "未知门店操作: "+action)
	}
}

func (s *Server) setAcceptance(w http.ResponseWriter, r *http.Request, storeID int64) {
	if r.Method != http.MethodPost {
		auth.WriteError(w, 405, "POST only")
		return
	}
	if _, ok := s.requireRoles(w, r, "store_staff", "area_manager", "supervisor", "admin"); !ok {
		return
	}
	var in struct {
		Mode string `json:"mode"`
		Note string `json:"note"`
	}
	if err := decodeBody(r, &in); err != nil {
		auth.WriteError(w, 400, "请求体格式错误")
		return
	}
	se := sess(r)
	if err := s.app.SetAcceptance(r.Context(), storeID, in.Mode, in.Note, se.ID); err != nil {
		service.WriteBizError(w, err)
		return
	}
	auth.WriteOK(w, map[string]string{"message": "门店接单状态已更新: " + in.Mode})
}

func (s *Server) replenish(w http.ResponseWriter, r *http.Request, storeID int64) {
	if r.Method != http.MethodPost {
		auth.WriteError(w, 405, "POST only")
		return
	}
	if _, ok := s.requireRoles(w, r, "store_staff", "admin"); !ok {
		return
	}
	var in struct {
		IngredientID int64   `json:"ingredient_id"`
		Qty          float64 `json:"qty"`
	}
	if err := decodeBody(r, &in); err != nil {
		auth.WriteError(w, 400, "请求体格式错误")
		return
	}
	if err := s.app.Replenish(r.Context(), storeID, in.IngredientID, in.Qty, sess(r).ID); err != nil {
		service.WriteBizError(w, err)
		return
	}
	auth.WriteOK(w, map[string]string{"message": "补货成功"})
}

// /api/cups/{id}/start|seal|remake ；封签扫码也支持 POST /api/cups/seal-scan
func (s *Server) cupSub(w http.ResponseWriter, r *http.Request) {
	rest := r.URL.Path[len("/api/cups/"):]
	idStr, action := "", ""
	for i := 0; i < len(rest); i++ {
		if rest[i] == '/' {
			idStr, action = rest[:i], rest[i+1:]
			break
		}
	}
	if idStr == "" {
		idStr = rest
	}
	if idStr == "seal-scan" {
		s.sealScan(w, r)
		return
	}
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		auth.WriteError(w, 400, "杯号无效")
		return
	}
	switch action {
	case "start":
		s.startBrew(w, r, id)
	case "remake":
		s.remake(w, r, id)
	default:
		auth.WriteError(w, 404, "未知杯操作: "+action)
	}
}

func (s *Server) startBrew(w http.ResponseWriter, r *http.Request, id int64) {
	if r.Method != http.MethodPost {
		auth.WriteError(w, 405, "POST only")
		return
	}
	if _, ok := s.requireRoles(w, r, "store_staff", "admin"); !ok {
		return
	}
	if err := s.app.StartBrew(r.Context(), id, sess(r).ID); err != nil {
		service.WriteBizError(w, err)
		return
	}
	auth.WriteOK(w, map[string]interface{}{"cup_id": id, "status": "brewing"})
}

func (s *Server) sealScan(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		auth.WriteError(w, 405, "POST only")
		return
	}
	if _, ok := s.requireRoles(w, r, "store_staff", "admin"); !ok {
		return
	}
	var in struct {
		SealCode string `json:"seal_code"`
	}
	if err := decodeBody(r, &in); err != nil {
		auth.WriteError(w, 400, "请求体格式错误")
		return
	}
	v, err := s.app.SealScan(r.Context(), in.SealCode, sess(r).ID)
	if err != nil {
		service.WriteBizError(w, err)
		return
	}
	auth.WriteOK(w, v)
}

func (s *Server) remake(w http.ResponseWriter, r *http.Request, id int64) {
	if r.Method != http.MethodPost {
		auth.WriteError(w, 405, "POST only")
		return
	}
	if _, ok := s.requireRoles(w, r, "store_staff", "admin"); !ok {
		return
	}
	var in struct {
		Reason string `json:"reason"`
	}
	_ = decodeBody(r, &in)
	newID, err := s.app.RemakeCup(r.Context(), id, sess(r).ID, in.Reason)
	if err != nil {
		service.WriteBizError(w, err)
		return
	}
	auth.WriteOK(w, map[string]interface{}{
		"old_cup_id": id, "new_cup_id": newID,
		"message": "旧杯已报损（重做成本进班组复盘），新杯重新进入出杯队列",
	})
}
