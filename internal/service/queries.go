package service

import (
	"context"
	"database/sql"
	"net/http"
	"time"
)

func (a *App) ResolveStore(ctx context.Context, code string) (int64, error) {
	var id int64
	err := a.DB.QueryRowContext(ctx, `SELECT id FROM stores WHERE code=$1`, code).Scan(&id)
	if err == sql.ErrNoRows {
		return 0, &BizError{http.StatusNotFound, "门店不存在: " + code}
	}
	return id, err
}

type StoreView struct {
	ID             int64  `json:"id"`
	Code           string `json:"code"`
	Name           string `json:"name"`
	Address        string `json:"address"`
	HourlyCapacity int    `json:"hourly_capacity"`
	Acceptance     string `json:"acceptance"`
	Status         string `json:"status"`
}

func (a *App) ListStores(ctx context.Context) ([]StoreView, error) {
	rows, err := a.DB.QueryContext(ctx, `
		SELECT id,code,name,COALESCE(address,''),hourly_capacity,acceptance,status
		FROM stores ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []StoreView{}
	for rows.Next() {
		var v StoreView
		if err := rows.Scan(&v.ID, &v.Code, &v.Name, &v.Address,
			&v.HourlyCapacity, &v.Acceptance, &v.Status); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}

type ProductView struct {
	ID            int64   `json:"id"`
	SKU           string  `json:"sku"`
	Name          string  `json:"name"`
	Category      string  `json:"category"`
	Price         float64 `json:"price"`
	Cost          float64 `json:"cost"`
	MakeSecs      int     `json:"make_secs"`
	NeedsStraw    bool    `json:"needs_straw"`
	NeedsUtensils bool    `json:"needs_utensils"`
	BagClass      string  `json:"bag_class"`
}

func (a *App) ListProducts(ctx context.Context) ([]ProductView, error) {
	rows, err := a.DB.QueryContext(ctx, `
		SELECT id,sku,name,category,price,cost,make_secs,needs_straw,needs_utensils,bag_class
		FROM products ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ProductView{}
	for rows.Next() {
		var p ProductView
		if err := rows.Scan(&p.ID, &p.SKU, &p.Name, &p.Category, &p.Price, &p.Cost,
			&p.MakeSecs, &p.NeedsStraw, &p.NeedsUtensils, &p.BagClass); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, nil
}

type InventoryView struct {
	IngredientID int64   `json:"ingredient_id"`
	Name         string  `json:"name"`
	Unit         string  `json:"unit"`
	Qty          float64 `json:"qty"`
}

func (a *App) ListInventory(ctx context.Context, storeID int64) ([]InventoryView, error) {
	rows, err := a.DB.QueryContext(ctx, `
		SELECT i.id,i.name,i.unit,COALESCE(inv.qty,0)
		FROM ingredients i
		LEFT JOIN inventory inv ON inv.ingredient_id=i.id AND inv.store_id=$1
		ORDER BY i.id`, storeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []InventoryView{}
	for rows.Next() {
		var v InventoryView
		if err := rows.Scan(&v.IngredientID, &v.Name, &v.Unit, &v.Qty); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}

type DelistingView struct {
	ID        int64     `json:"id"`
	ProductID int64     `json:"product_id"`
	SKU       string    `json:"sku"`
	Name      string    `json:"name"`
	Reason    string    `json:"reason"`
	Active    bool      `json:"active"`
	CreatedAt time.Time `json:"created_at"`
}

func (a *App) ListDelistings(ctx context.Context, storeID int64, activeOnly bool) ([]DelistingView, error) {
	q := `
		SELECT d.id,d.product_id,p.sku,p.name,d.reason,d.active,d.created_at
		FROM temporary_delistings d JOIN products p ON p.id=d.product_id
		WHERE ($1=0 OR d.store_id=$1) AND ($2=false OR d.active=true)
		ORDER BY d.id DESC`
	rows, err := a.DB.QueryContext(ctx, q, storeID, activeOnly)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []DelistingView{}
	for rows.Next() {
		var v DelistingView
		if err := rows.Scan(&v.ID, &v.ProductID, &v.SKU, &v.Name, &v.Reason, &v.Active, &v.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}

type OrderItemView struct {
	ProductName string `json:"product_name"`
	SKU         string `json:"sku"`
	Qty         int    `json:"qty"`
	IceHot      string `json:"ice_hot"`
	Sugar       string `json:"sugar"`
	CupSize     string `json:"cup_size"`
	Remark      string `json:"remark"`
	Shortage    bool   `json:"shortage"`
}

type CupLine struct {
	CupID      int64  `json:"cup_id"`
	SealCode   string `json:"seal_code"`
	Status     string `json:"status"`
	IsRemake   bool   `json:"is_remake"`
	QueueSeq   int64  `json:"queue_seq"`
	Stale      bool   `json:"stale"`
	ProductSKU string `json:"product_sku"`
}

type OrderView struct {
	ID              int64           `json:"id"`
	PlatformCode    string          `json:"platform_code"`
	PlatformOrderNo string          `json:"platform_order_no"`
	StoreCode       string          `json:"store_code"`
	Status          string          `json:"status"`
	TotalAmount     float64         `json:"total_amount"`
	TotalCost       float64         `json:"total_cost"`
	LostMargin      float64         `json:"lost_margin"`
	IceHot          string          `json:"ice_hot"`
	Sugar           string          `json:"sugar"`
	CupSize         string          `json:"cup_size"`
	Remark          string          `json:"remark"`
	ExpectedBags    int             `json:"expected_bags"`
	ActualBags      *int            `json:"actual_bags,omitempty"`
	HotColdSplit    *bool           `json:"hot_cold_split,omitempty"`
	StrawUtensilsOK *bool           `json:"straw_utensils_ok,omitempty"`
	SlaBreach       bool            `json:"sla_breach"`
	OrderedAt       time.Time       `json:"ordered_at"`
	RiderETA        *time.Time      `json:"rider_eta,omitempty"`
	RiderArrivedAt  *time.Time      `json:"rider_arrived_at,omitempty"`
	ReadyAt         *time.Time      `json:"ready_at,omitempty"`
	HandedOverAt    *time.Time      `json:"handed_over_at,omitempty"`
	CancelledAt     *time.Time      `json:"cancelled_at,omitempty"`
	CancelReason    string          `json:"cancel_reason,omitempty"`
	RiderName       string          `json:"rider_name,omitempty"`
	CustomerName    string          `json:"customer_name,omitempty"`
	Items           []OrderItemView `json:"items"`
	Cups            []CupLine       `json:"cups"`
	IncidentIDs     []int64         `json:"incident_ids"`
}

func scanOrderDetail(ctx context.Context, db interface {
	QueryRowContext(context.Context, string, ...interface{}) *sql.Row
}, id int64) (*OrderView, error) {
	var o OrderView
	var rider, customer, cancelReason sql.NullString
	var expectedBags sql.NullInt64
	var actualBags sql.NullInt64
	var hotSplit, strawOK sql.NullBool
	var eta, arrived, ready, handed, cancelled sql.NullTime
	err := db.QueryRowContext(ctx, `
		SELECT o.id,p.code,o.platform_order_no,s.code,o.status,
		       o.total_amount,o.total_cost,o.lost_margin,
		       o.ice_hot,COALESCE(o.sugar,''),COALESCE(o.cup_size,''),COALESCE(o.remark,''),
		       o.expected_bags,o.actual_bags,o.hot_cold_split,o.straw_utensils_ok,
		       o.sla_breach,o.ordered_at,o.rider_eta,o.rider_arrived_at,
		       o.ready_at,o.handed_over_at,o.cancelled_at,COALESCE(o.cancel_reason,''),
		       COALESCE(r.name,''),COALESCE(c.name,'')
		FROM orders o
		JOIN platforms p ON p.id=o.platform_id
		JOIN stores s ON s.id=o.store_id
		LEFT JOIN riders r ON r.id=o.rider_id
		LEFT JOIN customers c ON c.id=o.customer_id
		WHERE o.id=$1`, id).Scan(
		&o.ID, &o.PlatformCode, &o.PlatformOrderNo, &o.StoreCode, &o.Status,
		&o.TotalAmount, &o.TotalCost, &o.LostMargin,
		&o.IceHot, &o.Sugar, &o.CupSize, &o.Remark,
		&expectedBags, &actualBags, &hotSplit, &strawOK,
		&o.SlaBreach, &o.OrderedAt, &eta, &arrived, &ready, &handed, &cancelled,
		&cancelReason, &rider, &customer)
	if err == sql.ErrNoRows {
		return nil, &BizError{http.StatusNotFound, "订单不存在"}
	}
	if err != nil {
		return nil, err
	}
	o.ExpectedBags = int(expectedBags.Int64)
	if actualBags.Valid {
		n := int(actualBags.Int64)
		o.ActualBags = &n
	}
	if hotSplit.Valid {
		o.HotColdSplit = &hotSplit.Bool
	}
	if strawOK.Valid {
		o.StrawUtensilsOK = &strawOK.Bool
	}
	setTime(&o.RiderETA, eta)
	setTime(&o.RiderArrivedAt, arrived)
	setTime(&o.ReadyAt, ready)
	setTime(&o.HandedOverAt, handed)
	setTime(&o.CancelledAt, cancelled)
	o.CancelReason = cancelReason.String
	o.RiderName = rider.String
	o.CustomerName = customer.String
	return &o, nil
}

func setTime(dst **time.Time, v sql.NullTime) {
	if v.Valid {
		t := v.Time
		*dst = &t
	}
}

func (a *App) GetOrder(ctx context.Context, id int64) (*OrderView, error) {
	o, err := scanOrderDetail(ctx, a.DB, id)
	if err != nil {
		return nil, err
	}
	rows, err := a.DB.QueryContext(ctx, `
		SELECT p.name,p.sku,oi.qty,COALESCE(oi.ice_hot,'normal'),COALESCE(oi.sugar,''),
		       COALESCE(oi.cup_size,''),COALESCE(oi.remark,''),oi.shortage
		FROM order_items oi JOIN products p ON p.id=oi.product_id
		WHERE oi.order_id=$1 ORDER BY oi.id`, id)
	if err != nil {
		return nil, err
	}
	o.Items = []OrderItemView{}
	for rows.Next() {
		var it OrderItemView
		if err := rows.Scan(&it.ProductName, &it.SKU, &it.Qty, &it.IceHot, &it.Sugar,
			&it.CupSize, &it.Remark, &it.Shortage); err != nil {
			rows.Close()
			return nil, err
		}
		o.Items = append(o.Items, it)
	}
	rows.Close()

	crows, err := a.DB.QueryContext(ctx, `
		SELECT c.id,c.seal_code,c.status,c.is_remake,c.queue_seq,
		       c.stale_flagged_at IS NOT NULL,p.sku
		FROM cups c JOIN order_items oi ON oi.id=c.item_id
		JOIN products p ON p.id=oi.product_id
		WHERE c.order_id=$1 ORDER BY c.id`, id)
	if err != nil {
		return nil, err
	}
	o.Cups = []CupLine{}
	for crows.Next() {
		var c CupLine
		if err := crows.Scan(&c.CupID, &c.SealCode, &c.Status, &c.IsRemake,
			&c.QueueSeq, &c.Stale, &c.ProductSKU); err != nil {
			crows.Close()
			return nil, err
		}
		o.Cups = append(o.Cups, c)
	}
	crows.Close()

	irows, err := a.DB.QueryContext(ctx,
		`SELECT id FROM incidents WHERE order_id=$1 ORDER BY id`, id)
	if err != nil {
		return nil, err
	}
	o.IncidentIDs = []int64{}
	for irows.Next() {
		var x int64
		if err := irows.Scan(&x); err != nil {
			irows.Close()
			return nil, err
		}
		o.IncidentIDs = append(o.IncidentIDs, x)
	}
	irows.Close()
	return o, nil
}

type LossView struct {
	ID          int64     `json:"id"`
	ProductSKU  string    `json:"product_sku"`
	ProductName string    `json:"product_name"`
	Qty         float64   `json:"qty"`
	CostAmount  float64   `json:"cost_amount"`
	Reason      string    `json:"reason"`
	OrderID     int64     `json:"order_id"`
	CreatedAt   time.Time `json:"created_at"`
}

func (a *App) ListLosses(ctx context.Context, storeID int64) ([]LossView, error) {
	rows, err := a.DB.QueryContext(ctx, `
		SELECT l.id,COALESCE(p.sku,''),COALESCE(p.name,''),l.qty,l.cost_amount,
		       l.reason,l.order_id,l.created_at
		FROM stock_losses l LEFT JOIN products p ON p.id=l.product_id
		WHERE ($1=0 OR l.store_id=$1) ORDER BY l.id DESC`, storeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []LossView{}
	for rows.Next() {
		var v LossView
		if err := rows.Scan(&v.ID, &v.ProductSKU, &v.ProductName, &v.Qty,
			&v.CostAmount, &v.Reason, &v.OrderID, &v.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}

type OrderSummary struct {
	ID              int64     `json:"id"`
	PlatformCode    string    `json:"platform_code"`
	PlatformOrderNo string    `json:"platform_order_no"`
	StoreCode       string    `json:"store_code"`
	Status          string    `json:"status"`
	TotalAmount     float64   `json:"total_amount"`
	SlaBreach       bool      `json:"sla_breach"`
	OrderedAt       time.Time `json:"ordered_at"`
}

func (a *App) ListOrders(ctx context.Context, storeID int64, status string, limit int) ([]OrderSummary, error) {
	rows, err := a.DB.QueryContext(ctx, `
		SELECT o.id,p.code,o.platform_order_no,s.code,o.status,o.total_amount,
		       o.sla_breach,o.ordered_at
		FROM orders o
		JOIN platforms p ON p.id=o.platform_id
		JOIN stores s ON s.id=o.store_id
		WHERE ($1=0 OR o.store_id=$1) AND ($2='' OR o.status=$2)
		ORDER BY o.id DESC LIMIT $3`, storeID, status, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []OrderSummary{}
	for rows.Next() {
		var o OrderSummary
		if err := rows.Scan(&o.ID, &o.PlatformCode, &o.PlatformOrderNo, &o.StoreCode,
			&o.Status, &o.TotalAmount, &o.SlaBreach, &o.OrderedAt); err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, nil
}
