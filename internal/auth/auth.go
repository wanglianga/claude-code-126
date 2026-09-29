package auth

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/redis/go-redis/v9"
	"golang.org/x/crypto/bcrypt"
)

type Session struct {
	Kind     string `json:"kind"` // user / customer / rider
	ID       int64  `json:"id"`
	Role     string `json:"role,omitempty"`
	StoreID  int64  `json:"store_id,omitempty"`
	RegionID int64  `json:"region_id,omitempty"`
	Name     string `json:"name"`
}

const sessionTTL = 12 * time.Hour

type Manager struct {
	rdb    *redis.Client
	db     *sql.DB
	apiKey string
}

func NewManager(rdb *redis.Client, db *sql.DB, apiKey string) *Manager {
	return &Manager{rdb: rdb, db: db, apiKey: apiKey}
}

func newToken() string {
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// EnsurePasswords 将种子账号的 RESET_ME 占位密码写入 bcrypt
func (m *Manager) EnsurePasswords() error {
	pwds := map[string]string{
		"admin": "admin123",
	}
	rows, err := m.db.Query(`SELECT username FROM users WHERE password = 'RESET_ME'`)
	if err != nil {
		return err
	}
	var names []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			rows.Close()
			return err
		}
		names = append(names, n)
	}
	rows.Close()
	for _, name := range names {
		pw := pwds[name]
		if pw == "" {
			pw = "demo123"
		}
		h, err := bcrypt.GenerateFromPassword([]byte(pw), bcrypt.DefaultCost)
		if err != nil {
			return err
		}
		if _, err := m.db.Exec(`UPDATE users SET password=$1 WHERE username=$2`, string(h), name); err != nil {
			return err
		}
	}
	return nil
}

func (m *Manager) issue(ctx context.Context, s Session) (string, error) {
	tok := newToken()
	b, _ := json.Marshal(s)
	if err := m.rdb.Set(ctx, "session:"+tok, b, sessionTTL).Err(); err != nil {
		return "", err
	}
	return tok, nil
}

func (m *Manager) LoginUser(ctx context.Context, username, password string) (string, Session, error) {
	var (
		id, storeID, regionID int64
		storeIDsql            sql.NullInt64
		regionIDsql           sql.NullInt64
		hash, name, role      string
	)
	err := m.db.QueryRowContext(ctx,
		`SELECT id, password, display_name, role, store_id, region_id
		 FROM users WHERE username=$1 AND active=true`, username).
		Scan(&id, &hash, &name, &role, &storeIDsql, &regionIDsql)
	if err == sql.ErrNoRows {
		return "", Session{}, fmt.Errorf("账号或密码错误")
	}
	if err != nil {
		return "", Session{}, err
	}
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) != nil {
		return "", Session{}, fmt.Errorf("账号或密码错误")
	}
	storeID, regionID = storeIDsql.Int64, regionIDsql.Int64
	s := Session{Kind: "user", ID: id, Role: role, StoreID: storeID, RegionID: regionID, Name: name}
	tok, err := m.issue(ctx, s)
	return tok, s, err
}

func (m *Manager) LoginCustomer(ctx context.Context, accessCode string) (string, Session, error) {
	var id int64
	var name string
	err := m.db.QueryRowContext(ctx, `SELECT id, COALESCE(name,'顾客') FROM customers WHERE access_code=$1`, accessCode).
		Scan(&id, &name)
	if err != nil {
		return "", Session{}, fmt.Errorf("顾客登录码无效")
	}
	s := Session{Kind: "customer", ID: id, Role: "customer", Name: name}
	tok, err := m.issue(ctx, s)
	return tok, s, err
}

func (m *Manager) LoginRider(ctx context.Context, riderCode string) (string, Session, error) {
	var id int64
	var name string
	err := m.db.QueryRowContext(ctx, `SELECT id, name FROM riders WHERE rider_code=$1`, riderCode).
		Scan(&id, &name)
	if err != nil {
		return "", Session{}, fmt.Errorf("骑手编号无效")
	}
	s := Session{Kind: "rider", ID: id, Role: "rider", Name: name}
	tok, err := m.issue(ctx, s)
	return tok, s, err
}

func (m *Manager) Resolve(ctx context.Context, token string) (Session, error) {
	b, err := m.rdb.Get(ctx, "session:"+token).Bytes()
	if err != nil {
		return Session{}, fmt.Errorf("未登录或会话已过期")
	}
	var s Session
	if err := json.Unmarshal(b, &s); err != nil {
		return Session{}, err
	}
	return s, nil
}

func (m *Manager) CheckAPIKey(key string) bool {
	return key != "" && key == m.apiKey
}

// ---- HTTP 辅助 ----

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func WriteError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func WriteOK(w http.ResponseWriter, v interface{}) {
	writeJSON(w, 200, v)
}

type ctxKey string

const sessionKey ctxKey = "session"

func (m *Manager) WithSession(s Session, r *http.Request) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), sessionKey, s))
}

func SessionFrom(ctx context.Context) (Session, bool) {
	s, ok := ctx.Value(sessionKey).(Session)
	return s, ok
}

// AuthMiddleware 校验 Bearer Token（user/customer/rider 通用）
func (m *Manager) AuthMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := r.Header.Get("Authorization")
		if len(h) < 8 || h[:7] != "Bearer " {
			WriteError(w, 401, "缺少登录令牌")
			return
		}
		s, err := m.Resolve(r.Context(), h[7:])
		if err != nil {
			WriteError(w, 401, err.Error())
			return
		}
		next.ServeHTTP(w, m.WithSession(s, r))
	})
}

// RequireUserKind/RequireRole 在具体 handler 内通过 Require* 校验
func RequireRole(w http.ResponseWriter, r *http.Request, roles ...string) (Session, bool) {
	s, _ := SessionFrom(r.Context())
	for _, role := range roles {
		if s.Role == role {
			return s, true
		}
	}
	WriteError(w, 403, "当前角色无权执行此操作")
	return s, false
}
