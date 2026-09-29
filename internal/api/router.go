package api

import (
	_ "embed"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	httpSwagger "github.com/swaggo/http-swagger/v2"

	"coffeechain/internal/auth"
	"coffeechain/internal/service"
)

//go:embed openapi/openapi.json
var openapiSpec []byte

type Server struct {
	app    *service.App
	auth   *auth.Manager
	apiKey string
}

func NewServer(app *service.App, am *auth.Manager, apiKey string) http.Handler {
	s := &Server{app: app, auth: am, apiKey: apiKey}
	mux := http.NewServeMux()

	// OpenAPI 3.x 文档与内嵌 Swagger UI
	mux.HandleFunc("/openapi.json", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_, _ = w.Write(openapiSpec)
	})
	mux.HandleFunc("/swagger/", httpSwagger.Handler(
		httpSwagger.URL("/openapi.json"),
		httpSwagger.DocExpansion("none"),
	))

	// 认证
	mux.HandleFunc("/api/auth/login", s.login)
	mux.HandleFunc("/api/auth/customer-login", s.customerLogin)
	mux.HandleFunc("/api/auth/rider-login", s.riderLogin)

	// 公开只读
	mux.HandleFunc("/api/products", s.listProducts)
	mux.HandleFunc("/api/stores", s.listStores)

	// 外卖平台接入（API Key）
	mux.Handle("/api/platform/orders", s.requireAPIKey(http.HandlerFunc(s.acceptOrder)))

	// 需登录接口
	authed := http.NewServeMux()
	mux.Handle("/api/", s.auth.AuthMiddleware(authed))

	// 订单查询
	authed.HandleFunc("/api/orders", s.orders)
	authed.HandleFunc("/api/orders/", s.orderSub)
	// 异常
	authed.HandleFunc("/api/incidents", s.incidents)
	authed.HandleFunc("/api/incidents/", s.incidentSub)
	// 门店子资源
	authed.HandleFunc("/api/stores/", s.storeSub)
	// 杯操作
	authed.HandleFunc("/api/cups/", s.cupSub)
	// 赔付
	authed.HandleFunc("/api/compensations/", s.compSub)
	// 下架
	authed.HandleFunc("/api/delistings", s.delistings)
	authed.HandleFunc("/api/delistings/", s.delistingSub)
	// 班组
	authed.HandleFunc("/api/shifts/", s.shiftSub)
	// 经理 / 督导
	authed.HandleFunc("/api/manager/store-metrics", s.managerMetrics)
	authed.HandleFunc("/api/supervisor/shift-scores", s.supervisorScores)
	// 演示时钟
	authed.HandleFunc("/api/demo/clock", s.demoClock)
	authed.HandleFunc("/api/admin/stale-sweep", s.staleSweep)

	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	})
	return withLogging(mux)
}

// ---- 辅助 ----

func (s *Server) requireAPIKey(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.Header.Get("X-Platform-Key")
		if !s.auth.CheckAPIKey(key) {
			auth.WriteError(w, 401, "平台接入密钥无效（X-Platform-Key）")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func decodeBody(r *http.Request, v interface{}) error {
	defer r.Body.Close()
	dec := json.NewDecoder(r.Body)
	return dec.Decode(v)
}

func pathID(prefix, path string) (int64, string, bool) {
	rest := strings.TrimPrefix(path, prefix)
	parts := strings.Split(strings.Trim(rest, "/"), "/")
	if len(parts) == 0 || parts[0] == "" {
		return 0, "", false
	}
	id, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return 0, "", false
	}
	sub := ""
	if len(parts) > 1 {
		sub = parts[1]
	}
	return id, sub, true
}

func sess(r *http.Request) auth.Session {
	s, _ := auth.SessionFrom(r.Context())
	return s
}

func toSvc(s auth.Session) service.Session_ {
	return service.Session_{ID: s.ID, Role: s.Role, Name: s.Name}
}

func (s *Server) checkStoreScope(w http.ResponseWriter, r *http.Request, storeID int64) bool {
	se := sess(r)
	if se.Role == "store_staff" && se.StoreID != 0 && se.StoreID != storeID {
		auth.WriteError(w, 403, "店员只能操作所属门店")
		return false
	}
	return true
}

func (s *Server) requireRoles(w http.ResponseWriter, r *http.Request, roles ...string) (auth.Session, bool) {
	return auth.RequireRole(w, r, roles...)
}

func (s *Server) regionScope(r *http.Request) int64 {
	se := sess(r)
	if (se.Role == "area_manager" || se.Role == "supervisor") && se.RegionID != 0 {
		return se.RegionID
	}
	v, _ := strconv.ParseInt(r.URL.Query().Get("region_id"), 10, 64)
	return v
}

func (s *Server) queryStoreID(r *http.Request) int64 {
	se := sess(r)
	if se.Role == "store_staff" && se.StoreID != 0 {
		return se.StoreID
	}
	v, _ := strconv.ParseInt(r.URL.Query().Get("store_id"), 10, 64)
	return v
}
