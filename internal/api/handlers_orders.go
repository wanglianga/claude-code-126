package api

import (
	"net/http"
	"strconv"

	"coffeechain/internal/auth"
	"coffeechain/internal/service"
)

func (s *Server) acceptOrder(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		auth.WriteError(w, 405, "POST only")
		return
	}
	var in service.AcceptOrderInput
	if err := decodeBody(r, &in); err != nil {
		auth.WriteError(w, 400, "请求体格式错误: "+err.Error())
		return
	}
	id, err := s.app.AcceptOrder(r.Context(), in)
	if err != nil {
		service.WriteBizError(w, err)
		return
	}
	auth.WriteOK(w, map[string]interface{}{
		"order_id": id,
		"message":  "订单已接入：库存已扣减，杯贴已打印，进入门店出杯队列",
		"queue":    s.queueBoardHint(r, in.StoreCode),
	})
}

func (s *Server) queueBoardHint(r *http.Request, storeCode string) string {
	id, err := s.app.ResolveStore(r.Context(), storeCode)
	if err != nil {
		return ""
	}
	b, err := s.app.GetBoard(r.Context(), id)
	if err != nil {
		return ""
	}
	return "门店当前队列深度 " + strconv.Itoa(b.QueueDepth) + "，接单状态 " + b.Acceptance
}

func (s *Server) listProducts(w http.ResponseWriter, r *http.Request) {
	list, err := s.app.ListProducts(r.Context())
	if err != nil {
		auth.WriteError(w, 500, err.Error())
		return
	}
	auth.WriteOK(w, list)
}

func (s *Server) listStores(w http.ResponseWriter, r *http.Request) {
	rows, err := s.app.ListStores(r.Context())
	if err != nil {
		auth.WriteError(w, 500, err.Error())
		return
	}
	auth.WriteOK(w, rows)
}

func (s *Server) orders(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		auth.WriteError(w, 405, "GET only")
		return
	}
	storeID := s.queryStoreID(r)
	status := r.URL.Query().Get("status")
	limit := 50
	list, err := s.app.ListOrders(r.Context(), storeID, status, limit)
	if err != nil {
		auth.WriteError(w, 500, err.Error())
		return
	}
	auth.WriteOK(w, list)
}

func (s *Server) orderSub(w http.ResponseWriter, r *http.Request) {
	id, sub, ok := pathID("/api/orders/", r.URL.Path)
	if !ok {
		auth.WriteError(w, 400, "订单号无效")
		return
	}
	switch sub {
	case "":
		if r.Method != http.MethodGet {
			auth.WriteError(w, 405, "GET only")
			return
		}
		o, err := s.app.GetOrder(r.Context(), id)
		if err != nil {
			service.WriteBizError(w, err)
			return
		}
		auth.WriteOK(w, o)
	case "rider-arrive":
		s.riderArrive(w, r, id)
	case "handover":
		s.handover(w, r, id)
	case "deliver":
		s.deliver(w, r, id)
	case "report":
		s.report(w, r, id)
	default:
		auth.WriteError(w, 404, "未知订单操作: "+sub)
	}
}

func (s *Server) riderArrive(w http.ResponseWriter, r *http.Request, id int64) {
	if r.Method != http.MethodPost {
		auth.WriteError(w, 405, "POST only")
		return
	}
	se := sess(r)
	riderID := int64(0)
	if se.Role == "rider" {
		riderID = se.ID
	}
	if err := s.app.RiderArrive(r.Context(), id, riderID); err != nil {
		service.WriteBizError(w, err)
		return
	}
	auth.WriteOK(w, map[string]string{"message": "到店打卡成功；若未出杯将生成等待异常并联动门店"})
}

func (s *Server) handover(w http.ResponseWriter, r *http.Request, id int64) {
	if r.Method != http.MethodPost {
		auth.WriteError(w, 405, "POST only")
		return
	}
	se := sess(r)
	var in service.HandoverInput
	if err := decodeBody(r, &in); err != nil {
		auth.WriteError(w, 400, "请求体格式错误")
		return
	}
	in.OrderID = id
	staffID, riderID := int64(0), int64(0)
	if se.Role == "store_staff" {
		staffID = se.ID
	}
	if se.Role == "rider" {
		riderID = se.ID
	}
	res, err := s.app.HandoverCheck(r.Context(), in, staffID, riderID)
	if err != nil {
		service.WriteBizError(w, err)
		return
	}
	auth.WriteOK(w, res)
}

func (s *Server) deliver(w http.ResponseWriter, r *http.Request, id int64) {
	if r.Method != http.MethodPost {
		auth.WriteError(w, 405, "POST only")
		return
	}
	se := sess(r)
	riderID := int64(0)
	if se.Role == "rider" {
		riderID = se.ID
	}
	if err := s.app.ConfirmDelivered(r.Context(), id, riderID); err != nil {
		service.WriteBizError(w, err)
		return
	}
	auth.WriteOK(w, map[string]string{"message": "已确认送达"})
}

type reportReq struct {
	CupID  int64   `json:"cup_id"`
	Type   string  `json:"type"`
	Detail string  `json:"detail"`
	Amount float64 `json:"amount"`
}

func (s *Server) report(w http.ResponseWriter, r *http.Request, orderID int64) {
	if r.Method != http.MethodPost {
		auth.WriteError(w, 405, "POST only")
		return
	}
	var in reportReq
	if err := decodeBody(r, &in); err != nil {
		auth.WriteError(w, 400, "请求体格式错误")
		return
	}
	id, err := s.app.ReportIncident(r.Context(), service.ReportInput{
		OrderID: orderID, CupID: in.CupID, Type: in.Type, Detail: in.Detail, Amount: in.Amount,
	}, toSvc(sess(r)))
	if err != nil {
		service.WriteBizError(w, err)
		return
	}
	auth.WriteOK(w, map[string]interface{}{
		"incident_id": id,
		"message":     "异常已记录并联动门店/骑手/客服/财务到同一订单",
	})
}
