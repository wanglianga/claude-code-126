package api

import (
	"net/http"
	"strconv"

	"coffeechain/internal/auth"
	"coffeechain/internal/service"
)

func (s *Server) incidents(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		auth.WriteError(w, 405, "GET only")
		return
	}
	storeID := s.queryStoreID(r)
	status := r.URL.Query().Get("status")
	list, err := s.app.ListIncidents(r.Context(), storeID, status, 100)
	if err != nil {
		auth.WriteError(w, 500, err.Error())
		return
	}
	auth.WriteOK(w, list)
}

func (s *Server) incidentSub(w http.ResponseWriter, r *http.Request) {
	id, sub, ok := pathID("/api/incidents/", r.URL.Path)
	if !ok {
		auth.WriteError(w, 400, "异常单号无效")
		return
	}
	switch sub {
	case "":
		v, err := s.app.GetIncident(r.Context(), id)
		if err != nil {
			service.WriteBizError(w, err)
			return
		}
		auth.WriteOK(w, v)
	case "threads":
		s.addThread(w, r, id)
	case "resolve":
		s.resolveIncident(w, r, id)
	case "rider-wait-comp":
		s.riderWaitComp(w, r, id)
	default:
		auth.WriteError(w, 404, "未知异常操作: "+sub)
	}
}

func (s *Server) addThread(w http.ResponseWriter, r *http.Request, id int64) {
	if r.Method != http.MethodPost {
		auth.WriteError(w, 405, "POST only")
		return
	}
	var in struct {
		Message string `json:"message"`
	}
	if err := decodeBody(r, &in); err != nil {
		auth.WriteError(w, 400, "请求体格式错误")
		return
	}
	if err := s.app.AddThread(r.Context(), id, toSvc(sess(r)), in.Message); err != nil {
		service.WriteBizError(w, err)
		return
	}
	auth.WriteOK(w, map[string]string{"message": "留言已加入异常时间线，门店/骑手/顾客/客服/财务可见同一订单"})
}

func (s *Server) resolveIncident(w http.ResponseWriter, r *http.Request, id int64) {
	if r.Method != http.MethodPost {
		auth.WriteError(w, 405, "POST only")
		return
	}
	if _, ok := s.requireRoles(w, r, "store_staff", "area_manager", "supervisor",
		"platform_support", "admin"); !ok {
		return
	}
	if err := s.app.ResolveIncident(r.Context(), id, toSvc(sess(r))); err != nil {
		service.WriteBizError(w, err)
		return
	}
	auth.WriteOK(w, map[string]string{"message": "异常已复盘关闭"})
}

func (s *Server) riderWaitComp(w http.ResponseWriter, r *http.Request, id int64) {
	if r.Method != http.MethodPost {
		auth.WriteError(w, 405, "POST only")
		return
	}
	if _, ok := s.requireRoles(w, r, "platform_support", "admin"); !ok {
		return
	}
	var in struct {
		Amount float64 `json:"amount"`
	}
	if err := decodeBody(r, &in); err != nil {
		auth.WriteError(w, 400, "请求体格式错误")
		return
	}
	compID, err := s.app.ProposeRiderWaitComp(r.Context(), id, in.Amount, sess(r).ID)
	if err != nil {
		service.WriteBizError(w, err)
		return
	}
	auth.WriteOK(w, map[string]interface{}{
		"compensation_id": compID, "message": "骑手等待补贴方案已提交财务审批",
	})
}

// ---- 赔付 ----

func (s *Server) compSub(w http.ResponseWriter, r *http.Request) {
	id, sub, ok := pathID("/api/compensations/", r.URL.Path)
	if !ok {
		auth.WriteError(w, 400, "赔付单号无效")
		return
	}
	switch sub {
	case "approve":
		s.approveComp(w, r, id, true)
	case "reject":
		s.approveComp(w, r, id, false)
	case "pay":
		s.payComp(w, r, id)
	default:
		auth.WriteError(w, 404, "未知赔付操作: "+sub)
	}
}

func (s *Server) approveComp(w http.ResponseWriter, r *http.Request, id int64, approve bool) {
	if r.Method != http.MethodPost {
		auth.WriteError(w, 405, "POST only")
		return
	}
	if _, ok := s.requireRoles(w, r, "finance", "admin"); !ok {
		return
	}
	var in struct {
		Note string `json:"note"`
	}
	_ = decodeBody(r, &in)
	if err := s.app.ApproveComp(r.Context(), id, sess(r).ID, approve, in.Note); err != nil {
		service.WriteBizError(w, err)
		return
	}
	msg := "已驳回"
	if approve {
		msg = "已批准，计入门店补偿成本"
	}
	auth.WriteOK(w, map[string]string{"message": msg})
}

func (s *Server) payComp(w http.ResponseWriter, r *http.Request, id int64) {
	if r.Method != http.MethodPost {
		auth.WriteError(w, 405, "POST only")
		return
	}
	if _, ok := s.requireRoles(w, r, "finance", "admin"); !ok {
		return
	}
	if err := s.app.PayComp(r.Context(), id, sess(r).ID); err != nil {
		service.WriteBizError(w, err)
		return
	}
	auth.WriteOK(w, map[string]string{"message": "赔付已放款"})
}

// ---- 临时下架 ----

func (s *Server) delistings(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		auth.WriteError(w, 405, "GET only")
		return
	}
	activeOnly := r.URL.Query().Get("active") == "true"
	list, err := s.app.ListDelistings(r.Context(), s.queryStoreID(r), activeOnly)
	if err != nil {
		auth.WriteError(w, 500, err.Error())
		return
	}
	auth.WriteOK(w, list)
}

func (s *Server) delistingSub(w http.ResponseWriter, r *http.Request) {
	id, sub, ok := pathID("/api/delistings/", r.URL.Path)
	if !ok {
		auth.WriteError(w, 400, "下架单号无效")
		return
	}
	if sub != "restore" {
		auth.WriteError(w, 404, "未知下架操作: "+sub)
		return
	}
	if r.Method != http.MethodPost {
		auth.WriteError(w, 405, "POST only")
		return
	}
	if _, ok := s.requireRoles(w, r, "store_staff", "area_manager", "admin"); !ok {
		return
	}
	if err := s.app.RestoreDelisting(r.Context(), id); err != nil {
		service.WriteBizError(w, err)
		return
	}
	auth.WriteOK(w, map[string]string{"message": "商品已恢复上架"})
}

// ---- 班组复盘 ----

func (s *Server) shiftSub(w http.ResponseWriter, r *http.Request) {
	id, sub, ok := pathID("/api/shifts/", r.URL.Path)
	if !ok {
		auth.WriteError(w, 400, "班组号无效")
		return
	}
	if sub != "reviews" {
		auth.WriteError(w, 404, "未知班组操作: "+sub)
		return
	}
	if r.Method != http.MethodPost {
		auth.WriteError(w, 405, "POST only")
		return
	}
	if _, ok := s.requireRoles(w, r, "supervisor", "area_manager", "admin"); !ok {
		return
	}
	var in struct {
		Content string `json:"content"`
	}
	if err := decodeBody(r, &in); err != nil {
		auth.WriteError(w, 400, "请求体格式错误")
		return
	}
	if err := s.app.AddShiftReview(r.Context(), id, sess(r).ID, in.Content); err != nil {
		service.WriteBizError(w, err)
		return
	}
	auth.WriteOK(w, map[string]string{"message": "班组复盘已保存，评分已同步更新"})
}

// ---- 经理 / 督导 ----

func (s *Server) managerMetrics(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		auth.WriteError(w, 405, "GET only")
		return
	}
	if _, ok := s.requireRoles(w, r, "area_manager", "supervisor", "admin"); !ok {
		return
	}
	q := r.URL.Query()
	storeID, _ := strconv.ParseInt(q.Get("store_id"), 10, 64)
	list, err := s.app.StoreDailyMetrics(r.Context(), s.regionScope(r), storeID,
		q.Get("from"), q.Get("to"))
	if err != nil {
		auth.WriteError(w, 500, err.Error())
		return
	}
	auth.WriteOK(w, list)
}

func (s *Server) supervisorScores(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		auth.WriteError(w, 405, "GET only")
		return
	}
	if _, ok := s.requireRoles(w, r, "supervisor", "area_manager", "admin"); !ok {
		return
	}
	q := r.URL.Query()
	storeID, _ := strconv.ParseInt(q.Get("store_id"), 10, 64)
	list, err := s.app.ShiftScores(r.Context(), s.regionScope(r), storeID,
		q.Get("from"), q.Get("to"))
	if err != nil {
		auth.WriteError(w, 500, err.Error())
		return
	}
	auth.WriteOK(w, list)
}

// ---- 演示时钟（模拟时间推移，用于骑手提前到店/放置过久场景）----

func (s *Server) demoClock(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		auth.WriteError(w, 405, "POST only")
		return
	}
	var in struct {
		OffsetSecs int64 `json:"offset_secs"`
	}
	if err := decodeBody(r, &in); err != nil {
		auth.WriteError(w, 400, "请求体格式错误")
		return
	}
	if err := s.app.SetClockOffset(r.Context(), in.OffsetSecs); err != nil {
		auth.WriteError(w, 500, err.Error())
		return
	}
	auth.WriteOK(w, map[string]interface{}{
		"offset_secs": in.OffsetSecs,
		"message":     "演示时钟偏移已设置；调用 stale-sweep 可检查放置过久",
	})
}

func (s *Server) staleSweep(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		auth.WriteError(w, 405, "POST only")
		return
	}
	if _, ok := s.requireRoles(w, r, "store_staff", "admin"); !ok {
		return
	}
	if err := s.app.SweepStale(r.Context()); err != nil {
		service.WriteBizError(w, err)
		return
	}
	auth.WriteOK(w, map[string]string{"message": "已扫描待取区，过久饮品已标记并生成重做异常"})
}
