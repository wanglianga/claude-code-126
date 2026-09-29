package service

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
)

type openIncidentIn struct {
	OrderID      int64
	CupID        int64
	StoreID      int64
	Type         string
	Responsible  string // store/staff/rider/platform/customer/none
	Detail       string
	ReportedRole string
	ReportedBy   int64
}

// openIncident 在事务内创建异常事件并写入首条时间线消息，
// 把门店/店员/骑手/顾客/平台客服/财务关联到同一订单
func (a *App) openIncident(ctx context.Context, tx *sql.Tx, in openIncidentIn) (int64, error) {
	var id int64
	err := tx.QueryRowContext(ctx, `
		INSERT INTO incidents (order_id,cup_id,type,responsible_party,detail,
		                       reported_by_role,reported_by_id,store_id,status)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,'open') RETURNING id`,
		in.OrderID, nullableInt64(in.CupID), in.Type, in.Responsible, in.Detail,
		in.ReportedRole, nullableInt64(in.ReportedBy), in.StoreID).Scan(&id)
	if err != nil {
		return 0, err
	}
	msg := map[string]string{
		"rider_early_wait": "骑手提前到店但饮品未出杯，等待责任待定",
		"remake_stale":     "饮品放置过久，需要重做",
		"remark_missed":    "顾客备注被漏看",
		"wrong_cup":        "发现错杯",
		"food_oos":         "轻食缺货，无法制作",
		"wrong_bag":        "骑手取错袋",
		"platform_cancel":  "平台取消订单",
		"leak_complaint":   "顾客收货后投诉漏液",
		"missing_utensils": "交接核验发现漏吸管/餐具",
	}[in.Type]
	if msg == "" {
		msg = in.Detail
	}
	role := orDefault(in.ReportedRole, "system")
	authorName := map[string]string{
		"staff": "门店", "rider": "骑手", "customer": "顾客",
		"support": "平台客服", "finance": "财务", "system": "系统",
	}[role]
	if authorName == "" {
		authorName = role
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO incident_threads (incident_id,author_role,author_name,message)
		VALUES ($1,$2,$3,$4)`, id, role, authorName, msg)
	if err != nil {
		return 0, err
	}
	return id, nil
}

func nullableInt64(v int64) interface{} {
	if v == 0 {
		return nil
	}
	return v
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

// IncidentView 异常详情（含六方信息与时间线、赔付）
type IncidentView struct {
	ID            int64              `json:"id"`
	OrderID       int64              `json:"order_id"`
	PlatformNo    string             `json:"platform_order_no"`
	PlatformCode  string             `json:"platform_code"`
	StoreCode     string             `json:"store_code"`
	StoreName     string             `json:"store_name"`
	Type          string             `json:"type"`
	Status        string             `json:"status"`
	Responsible   string             `json:"responsible_party"`
	Detail        string             `json:"detail"`
	CustomerName  string             `json:"customer_name"`
	RiderName     string             `json:"rider_name"`
	StaffName     string             `json:"staff_name"`
	SupportName   string             `json:"support_name"`
	FinanceName   string             `json:"finance_name"`
	CreatedAt     string             `json:"created_at"`
	ResolvedAt    *string            `json:"resolved_at,omitempty"`
	Threads       []ThreadMsg        `json:"threads"`
	Compensations []CompensationView `json:"compensations"`
}

type ThreadMsg struct {
	AuthorRole string `json:"author_role"`
	AuthorName string `json:"author_name"`
	Message    string `json:"message"`
	CreatedAt  string `json:"created_at"`
}

type CompensationView struct {
	ID     int64   `json:"id"`
	Kind   string  `json:"kind"`
	Amount float64 `json:"amount"`
	Bearer string  `json:"bearer"`
	Status string  `json:"status"`
	Note   string  `json:"note"`
}

func (a *App) GetIncident(ctx context.Context, id int64) (*IncidentView, error) {
	v := &IncidentView{}
	var resolved sql.NullTime
	err := a.DB.QueryRowContext(ctx, `
		SELECT i.id,i.order_id,o.platform_order_no,p.code,s.code,s.name,
		       i.type,i.status,COALESCE(i.responsible_party,'none'),COALESCE(i.detail,''),
		       COALESCE(c.name,''),COALESCE(r.name,''),COALESCE(st.display_name,''),
		       COALESCE(sup.display_name,''),COALESCE(fin.display_name,''),
		       to_char(i.created_at,'YYYY-MM-DD HH24:MI:SS'),
		       to_char(i.resolved_at,'YYYY-MM-DD HH24:MI:SS')
		FROM incidents i
		JOIN orders o ON o.id=i.order_id
		JOIN platforms p ON p.id=o.platform_id
		JOIN stores s ON s.id=i.store_id
		LEFT JOIN customers c ON c.id=o.customer_id
		LEFT JOIN riders r ON r.id=o.rider_id
		LEFT JOIN users st ON st.id=i.reported_by_id
		LEFT JOIN users sup ON sup.id=i.support_user_id
		LEFT JOIN users fin ON fin.id=i.finance_user_id
		WHERE i.id=$1`, id).Scan(
		&v.ID, &v.OrderID, &v.PlatformNo, &v.PlatformCode, &v.StoreCode, &v.StoreName,
		&v.Type, &v.Status, &v.Responsible, &v.Detail,
		&v.CustomerName, &v.RiderName, &v.StaffName, &v.SupportName, &v.FinanceName,
		&v.CreatedAt, &resolved)
	if err == sql.ErrNoRows {
		return nil, &BizError{http.StatusNotFound, "异常事件不存在"}
	}
	if err != nil {
		return nil, err
	}
	if resolved.Valid {
		s := resolved.Time.Format("2006-01-02 15:04:05")
		v.ResolvedAt = &s
	}
	rows, err := a.DB.QueryContext(ctx, `
		SELECT author_role,author_name,message,to_char(created_at,'YYYY-MM-DD HH24:MI:SS')
		FROM incident_threads WHERE incident_id=$1 ORDER BY id`, id)
	if err != nil {
		return nil, err
	}
	v.Threads = []ThreadMsg{}
	for rows.Next() {
		var t ThreadMsg
		if err := rows.Scan(&t.AuthorRole, &t.AuthorName, &t.Message, &t.CreatedAt); err != nil {
			rows.Close()
			return nil, err
		}
		v.Threads = append(v.Threads, t)
	}
	rows.Close()

	crows, err := a.DB.QueryContext(ctx, `
		SELECT id,kind,amount,bearer,status,COALESCE(note,'')
		FROM compensations WHERE incident_id=$1 ORDER BY id`, id)
	if err != nil {
		return nil, err
	}
	v.Compensations = []CompensationView{}
	for crows.Next() {
		var c CompensationView
		if err := crows.Scan(&c.ID, &c.Kind, &c.Amount, &c.Bearer, &c.Status, &c.Note); err != nil {
			crows.Close()
			return nil, err
		}
		v.Compensations = append(v.Compensations, c)
	}
	crows.Close()
	return v, nil
}

// ListIncidents 列表（可按门店/状态过滤）
func (a *App) ListIncidents(ctx context.Context, storeID int64, status string, limit int) ([]IncidentView, error) {
	q := `
		SELECT i.id,i.order_id,o.platform_order_no,p.code,s.code,s.name,
		       i.type,i.status,COALESCE(i.responsible_party,'none'),COALESCE(i.detail,''),
		       COALESCE(c.name,''),COALESCE(r.name,''),COALESCE(st.display_name,''),
		       COALESCE(sup.display_name,''),COALESCE(fin.display_name,''),
		       to_char(i.created_at,'YYYY-MM-DD HH24:MI:SS'),
		       to_char(i.resolved_at,'YYYY-MM-DD HH24:MI:SS')
		FROM incidents i
		JOIN orders o ON o.id=i.order_id
		JOIN platforms p ON p.id=o.platform_id
		JOIN stores s ON s.id=i.store_id
		LEFT JOIN customers c ON c.id=o.customer_id
		LEFT JOIN riders r ON r.id=o.rider_id
		LEFT JOIN users st ON st.id=i.reported_by_id
		LEFT JOIN users sup ON sup.id=i.support_user_id
		LEFT JOIN users fin ON fin.id=i.finance_user_id
		WHERE ($1=0 OR i.store_id=$1) AND ($2='' OR i.status=$2)
		ORDER BY i.id DESC LIMIT $3`
	rows, err := a.DB.QueryContext(ctx, q, storeID, status, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []IncidentView{}
	for rows.Next() {
		var v IncidentView
		var support, finance sql.NullString
		var resolved sql.NullString
		if err := rows.Scan(
			&v.ID, &v.OrderID, &v.PlatformNo, &v.PlatformCode, &v.StoreCode, &v.StoreName,
			&v.Type, &v.Status, &v.Responsible, &v.Detail,
			&v.CustomerName, &v.RiderName, &v.StaffName, &support, &finance,
			&v.CreatedAt, &resolved); err != nil {
			return nil, err
		}
		v.SupportName, v.FinanceName = support.String, finance.String
		out = append(out, v)
	}
	return out, nil
}

// AddThread 六方在同一异常单下沟通；平台客服/财务首次留言即认领
func (a *App) AddThread(ctx context.Context, incidentID int64, s Session_, content string) error {
	if content == "" {
		return &BizError{http.StatusBadRequest, "消息不能为空"}
	}
	var orderStoreID int64
	err := a.DB.QueryRowContext(ctx, `SELECT store_id FROM incidents WHERE id=$1`, incidentID).
		Scan(&orderStoreID)
	if err == sql.ErrNoRows {
		return &BizError{http.StatusNotFound, "异常事件不存在"}
	}
	if err != nil {
		return err
	}
	roleName := map[string]string{
		"staff": "门店店员", "rider": "骑手", "customer": "顾客",
		"support": "平台客服", "finance": "财务", "system": "系统",
	}
	role, name := s.Role, s.Name
	switch role {
	case "store_staff":
		role = "staff"
	case "platform_support":
		role = "support"
	}
	display := roleName[role]
	if display == "" {
		display = role
	}
	tx, err := a.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx,
		`INSERT INTO incident_threads (incident_id,author_role,author_name,message)
		 VALUES ($1,$2,$3,$4)`, incidentID, role, name+"("+display+")", content); err != nil {
		return err
	}
	if role == "support" {
		if _, err = tx.ExecContext(ctx,
			`UPDATE incidents SET support_user_id=$2,status=
			   CASE WHEN status='open' THEN 'in_process' ELSE status END
			 WHERE id=$1`, incidentID, s.ID); err != nil {
			return err
		}
	}
	if role == "finance" {
		if _, err = tx.ExecContext(ctx,
			`UPDATE incidents SET finance_user_id=$2 WHERE id=$1`, incidentID, s.ID); err != nil {
			return err
		}
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	a.pushEvent(ctx, orderStoreID, "incident",
		fmt.Sprintf("异常#%d 新增%s留言", incidentID, display), a.now(ctx))
	return nil
}

// Session_ 避免与 auth.Session 循环依赖，由 api 层传入同形结构
type Session_ struct {
	ID   int64
	Role string
	Name string
}

// ResolveIncident 关闭异常（店长/督导/客服）
func (a *App) ResolveIncident(ctx context.Context, incidentID int64, s Session_) error {
	now := a.now(ctx)
	var storeID int64
	var unresolved int
	err := a.DB.QueryRowContext(ctx,
		`SELECT i.store_id,
		        count(c.id) FILTER (WHERE c.status='proposed')
		 FROM incidents i LEFT JOIN compensations c ON c.incident_id=i.id
		 WHERE i.id=$1 GROUP BY i.store_id`, incidentID).
		Scan(&storeID, &unresolved)
	if err == sql.ErrNoRows {
		return &BizError{http.StatusNotFound, "异常事件不存在"}
	}
	if err != nil {
		return err
	}
	if unresolved > 0 {
		return &BizError{http.StatusConflict, "仍有待财务审批的赔付方案，不能关闭异常"}
	}
	if _, err = a.DB.ExecContext(ctx,
		`UPDATE incidents SET status='resolved', resolved_at=$2 WHERE id=$1`,
		incidentID, now); err != nil {
		return err
	}
	a.pushEvent(ctx, storeID, "incident", fmt.Sprintf("异常#%d 已复盘关闭", incidentID), now)
	return nil
}

func (a *App) proposeComp(ctx context.Context, tx *sql.Tx, incidentID, orderID int64,
	kind string, amount float64, bearer string, proposedBy int64, note string) (int64, error) {
	var id int64
	err := tx.QueryRowContext(ctx, `
		INSERT INTO compensations (incident_id,order_id,kind,amount,bearer,status,proposed_by,note)
		VALUES ($1,$2,$3,$4,$5,'proposed',$6,$7) RETURNING id`,
		incidentID, orderID, kind, amount, bearer, nullableInt64(proposedBy), note).Scan(&id)
	return id, err
}
