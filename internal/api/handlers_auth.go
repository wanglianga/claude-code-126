package api

import (
	"log"
	"net/http"
	"time"

	"coffeechain/internal/auth"
)

type loginReq struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		auth.WriteError(w, 405, "POST only")
		return
	}
	var in loginReq
	if err := decodeBody(r, &in); err != nil {
		auth.WriteError(w, 400, "请求体格式错误")
		return
	}
	tok, se, err := s.auth.LoginUser(r.Context(), in.Username, in.Password)
	if err != nil {
		auth.WriteError(w, 401, err.Error())
		return
	}
	auth.WriteOK(w, map[string]interface{}{
		"token": tok, "role": se.Role, "name": se.Name,
		"store_id": se.StoreID, "region_id": se.RegionID,
	})
}

func (s *Server) customerLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		auth.WriteError(w, 405, "POST only")
		return
	}
	var in struct {
		AccessCode string `json:"access_code"`
	}
	if err := decodeBody(r, &in); err != nil {
		auth.WriteError(w, 400, "请求体格式错误")
		return
	}
	tok, se, err := s.auth.LoginCustomer(r.Context(), in.AccessCode)
	if err != nil {
		auth.WriteError(w, 401, err.Error())
		return
	}
	auth.WriteOK(w, map[string]interface{}{"token": tok, "role": "customer", "name": se.Name})
}

func (s *Server) riderLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		auth.WriteError(w, 405, "POST only")
		return
	}
	var in struct {
		RiderCode string `json:"rider_code"`
	}
	if err := decodeBody(r, &in); err != nil {
		auth.WriteError(w, 400, "请求体格式错误")
		return
	}
	tok, se, err := s.auth.LoginRider(r.Context(), in.RiderCode)
	if err != nil {
		auth.WriteError(w, 401, err.Error())
		return
	}
	auth.WriteOK(w, map[string]interface{}{"token": tok, "role": "rider", "name": se.Name})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

func withLogging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: 200}
		next.ServeHTTP(rec, r)
		log.Printf("%s %s -> %d (%dms)", r.Method, r.URL.Path, rec.status,
			time.Since(start).Milliseconds())
	})
}
