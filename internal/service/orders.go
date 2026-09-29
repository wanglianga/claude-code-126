package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"coffeechain/internal/auth"
)

type AcceptItemInput struct {
	SKU     string `json:"sku"`
	Qty     int    `json:"qty"`
	IceHot  string `json:"ice_hot"`
	Sugar   string `json:"sugar"`
	CupSize string `json:"cup_size"`
	Remark  string `json:"remark"`
}

type AcceptOrderInput struct {
	PlatformCode    string            `json:"platform_code"`
	PlatformOrderNo string            `json:"platform_order_no"`
	StoreCode       string            `json:"store_code"`
	CustomerCode    string            `json:"customer_code"`
	RiderCode       string            `json:"rider_code"`
	RiderETA        *time.Time        `json:"rider_eta"`
	IceHot          string            `json:"ice_hot"`
	Sugar           string            `json:"sugar"`
	CupSize         string            `json:"cup_size"`
	Remark          string            `json:"remark"`
	MergeGroupNote  string            `json:"merge_group_note"` // 非空则把同店多平台单并入同一合并组
	Items           []AcceptItemInput `json:"items"`
}

// AcceptOrder 外卖平台下单接入。
// 规则：门店暂停拒单；爆单期限流（每单概率拒绝并提示平台稍后推单）；
//
//	原料不足拒单；成功即扣减库存、生成杯/封签码、进入门店出杯队列。
func (a *App) AcceptOrder(ctx context.Context, in AcceptOrderInput) (int64, error) {
	if len(in.Items) == 0 {
		return 0, &BizError{http.StatusBadRequest, "订单必须至少包含一个商品"}
	}
	if in.PlatformOrderNo == "" || in.StoreCode == "" || in.PlatformCode == "" {
		return 0, &BizError{http.StatusBadRequest, "platform_code/platform_order_no/store_code 必填"}
	}
	now := a.now(ctx)

	tx, err := a.DB.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	// 平台 / 门店 / 顾客 / 骑手
	var platformID int64
	if err = tx.QueryRowContext(ctx, `SELECT id FROM platforms WHERE code=$1`, in.PlatformCode).
		Scan(&platformID); err != nil {
		return 0, &BizError{http.StatusBadRequest, "未知外卖平台: " + in.PlatformCode}
	}
	var (
		storeID, overloadThr int64
		acceptance           string
		throttleRate         float64
	)
	err = tx.QueryRowContext(ctx, `
		SELECT id, overload_queue_threshold, acceptance, throttled_rate
		FROM stores WHERE code=$1 AND status='active'`, in.StoreCode).
		Scan(&storeID, &overloadThr, &acceptance, &throttleRate)
	if err == sql.ErrNoRows {
		return 0, &BizError{http.StatusNotFound, "门店不存在或已停业: " + in.StoreCode}
	}
	if err != nil {
		return 0, err
	}

	if acceptance == "paused" {
		return 0, &BizError{http.StatusConflict, "门店已临时限制接单（爆单保护），请平台稍后推送"}
	}

	// 队列深度 = 在制 + 待制作杯数
	var depth int
	if err = tx.QueryRowContext(ctx,
		`SELECT count(*) FROM cups c JOIN orders o ON o.id=c.order_id
		 WHERE o.store_id=$1 AND c.status IN ('queued','brewing')`, storeID).
		Scan(&depth); err != nil {
		return 0, err
	}
	if int64(depth) >= overloadThr {
		// 首次进入爆单：自动限流
		if acceptance != "throttled" {
			if _, err = tx.ExecContext(ctx,
				`UPDATE stores SET acceptance='throttled', updated_at=now() WHERE id=$1`, storeID); err != nil {
				return 0, err
			}
			if _, err = tx.ExecContext(ctx,
				`INSERT INTO overload_events (store_id,type,queue_depth,note)
				 VALUES ($1,'triggered',$2,'队列超阈值，自动限流')`, storeID, depth); err != nil {
				return 0, err
			}
			acceptance = "throttled"
		}
	}
	if acceptance == "throttled" {
		// 限流期：骑手预计很快到店的单优先放行，其余按比例拒绝（确定性：按单号哈希）
		if in.RiderETA == nil || in.RiderETA.Sub(now) > 20*time.Minute {
			hash := 0
			for _, ch := range in.PlatformOrderNo {
				hash = hash*31 + int(ch)
			}
			if hash%100 < int(throttleRate*100) {
				return 0, &BizError{http.StatusTooManyRequests,
					"门店爆单限流中，本单被临时限制接单，请稍后重试"}
			}
		}
	}

	// 顾客
	var customerID sql.NullInt64
	if in.CustomerCode != "" {
		var cid int64
		if err = tx.QueryRowContext(ctx, `SELECT id FROM customers WHERE access_code=$1`, in.CustomerCode).
			Scan(&cid); err == nil {
			customerID = sql.NullInt64{Int64: cid, Valid: true}
		}
	}
	var riderID sql.NullInt64
	if in.RiderCode != "" {
		var rid int64
		if err = tx.QueryRowContext(ctx, `SELECT id FROM riders WHERE rider_code=$1`, in.RiderCode).
			Scan(&rid); err == nil {
			riderID = sql.NullInt64{Int64: rid, Valid: true}
		}
	}

	// 汇总配方需求；校验库存与临时下架
	type need struct {
		ingID int64
		qty   float64
	}
	needs := map[int64]float64{}
	var totalAmount, totalCost float64
	type prodRow struct {
		id                        int64
		price, cost               float64
		needsStraw, needsUtensils bool
	}
	prods := make([]prodRow, 0, len(in.Items))
	for _, it := range in.Items {
		if it.Qty <= 0 {
			return 0, &BizError{http.StatusBadRequest, "商品数量必须大于0"}
		}
		var p prodRow
		var straw, utensils bool
		err = tx.QueryRowContext(ctx,
			`SELECT id, price, cost, needs_straw, needs_utensils FROM products WHERE sku=$1`, it.SKU).
			Scan(&p.id, &p.price, &p.cost, &straw, &utensils)
		if err == sql.ErrNoRows {
			return 0, &BizError{http.StatusBadRequest, "未知商品 SKU: " + it.SKU}
		}
		if err != nil {
			return 0, err
		}
		p.needsStraw, p.needsUtensils = straw, utensils
		prods = append(prods, p)
		totalAmount += p.price * float64(it.Qty)
		totalCost += p.cost * float64(it.Qty)

		// 临时下架检查
		var activeDelist int
		_ = tx.QueryRowContext(ctx,
			`SELECT count(*) FROM temporary_delistings WHERE store_id=$1 AND product_id=$2 AND active=true`,
			storeID, p.id).Scan(&activeDelist)
		if activeDelist > 0 {
			return 0, &BizError{http.StatusConflict, fmt.Sprintf("商品[%s]门店临时缺货下架中", it.SKU)}
		}

		rows, rerr := tx.QueryContext(ctx,
			`SELECT ingredient_id, qty FROM recipes WHERE product_id=$1`, p.id)
		if rerr != nil {
			return 0, rerr
		}
		for rows.Next() {
			var ing int64
			var q float64
			if err = rows.Scan(&ing, &q); err != nil {
				rows.Close()
				return 0, err
			}
			needs[ing] += q * float64(it.Qty)
		}
		rows.Close()
	}
	// 吸管/餐具作为独立耗材校验
	for ingID, needQty := range needs {
		var stock float64
		err = tx.QueryRowContext(ctx,
			`SELECT qty FROM inventory WHERE store_id=$1 AND ingredient_id=$2`, storeID, ingID).
			Scan(&stock)
		if err == sql.ErrNoRows || stock < needQty {
			var ingName string
			_ = tx.QueryRowContext(ctx, `SELECT name FROM ingredients WHERE id=$1`, ingID).Scan(&ingName)
			return 0, &BizError{http.StatusConflict,
				fmt.Sprintf("原料[%s]库存不足（需%.2f），本单无法承接", ingName, needQty)}
		}
		if err != nil {
			return 0, err
		}
	}

	// 多平台订单合并组（同店；演示中用 note 关联，实际可由平台聚合单号关联）
	var mergeGroupID sql.NullInt64
	if in.MergeGroupNote != "" {
		var gid int64
		err = tx.QueryRowContext(ctx,
			`SELECT id FROM merge_groups WHERE store_id=$1 AND note=$2
			 ORDER BY id DESC LIMIT 1`, storeID, in.MergeGroupNote).Scan(&gid)
		if err == sql.ErrNoRows {
			if err = tx.QueryRowContext(ctx,
				`INSERT INTO merge_groups (store_id,note) VALUES ($1,$2) RETURNING id`,
				storeID, in.MergeGroupNote).Scan(&gid); err != nil {
				return 0, err
			}
		} else if err != nil {
			return 0, err
		}
		mergeGroupID = sql.NullInt64{Int64: gid, Valid: true}
	}

	// 创建订单
	var orderID int64
	err = tx.QueryRowContext(ctx, `
		INSERT INTO orders (platform_id, platform_order_no, store_id, customer_id, rider_id,
			merge_group_id, status, total_amount, total_cost, ice_hot, sugar, cup_size,
			remark, ordered_at, rider_eta, accepted_at, expected_bags)
		VALUES ($1,$2,$3,$4,$5,$6,'accepted',$7,$8,$9,$10,$11,$12,$13,$14,$13,$15)
		RETURNING id`,
		platformID, in.PlatformOrderNo, storeID, customerID, riderID, mergeGroupID,
		totalAmount, totalCost, nz(in.IceHot, "normal"), nz(in.Sugar, "标准糖"),
		nz(in.CupSize, "中杯"), in.Remark, now, in.RiderETA, bagCount(in)).Scan(&orderID)
	if err != nil {
		if isUniqueViolation(err) {
			return 0, &BizError{http.StatusConflict, "平台单号重复，已接入过该订单"}
		}
		return 0, err
	}

	// 明细 + 杯 + 扣库存
	cupN := 0
	for i := range prods {
		it := in.Items[i]
		p := prods[i]
		var itemID int64
		if err = tx.QueryRowContext(ctx, `
			INSERT INTO order_items (order_id,product_id,qty,unit_price,ice_hot,sugar,cup_size,remark)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8) RETURNING id`,
			orderID, p.id, it.Qty, p.price,
			nz(nz(it.IceHot, in.IceHot), "normal"),
			nz(it.Sugar, in.Sugar), nz(it.CupSize, in.CupSize), it.Remark).Scan(&itemID); err != nil {
			return 0, err
		}
		for k := 0; k < it.Qty; k++ {
			cupN++
			var cupID int64
			if err = tx.QueryRowContext(ctx, `
				INSERT INTO cups (order_id,item_id,seq_in_order,seal_code,status,queue_seq,
				                  label_printed_at)
				VALUES ($1,$2,$3,'SL'||lpad(nextval('seal_code_seq')::text,8,'0'),
				        'queued',nextval('cup_queue_seq'),now())
				RETURNING id`, orderID, itemID, cupN).Scan(&cupID); err != nil {
				return 0, err
			}
			if _, err = tx.ExecContext(ctx,
				`INSERT INTO cup_label_prints (cup_id) VALUES ($1)`, cupID); err != nil {
				return 0, err
			}
		}
	}
	for ingID, qty := range needs {
		if _, err = tx.ExecContext(ctx,
			`UPDATE inventory SET qty=qty-$2, updated_at=now()
			 WHERE store_id=$1 AND ingredient_id=$3`, storeID, qty, ingID); err != nil {
			return 0, err
		}
		if _, err = tx.ExecContext(ctx,
			`INSERT INTO stock_movements (store_id,ingredient_id,delta,reason,order_id)
			 VALUES ($1,$2,$3,'order_consume',$4)`, storeID, ingID, -qty, orderID); err != nil {
			return 0, err
		}
	}

	// 班组：收入与毛利
	gp := totalAmount - totalCost
	if err = a.applyMetrics(ctx, tx, storeID, now, metricDelta{
		revenue:     &totalAmount,
		grossProfit: &gp,
	}); err != nil {
		return 0, err
	}

	if err = tx.Commit(); err != nil {
		return 0, err
	}
	// Redis：门店出杯队列（店员/骑手屏实时数据）
	a.RDB.RPush(ctx, fmt.Sprintf("store:%d:queue", storeID), orderID)
	_ = a.bumpStoreBoard(ctx, storeID)
	return orderID, nil
}

func nz(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

// bagCount 预估袋数：热饮/冷饮/轻食分袋；组合套餐至少两袋
func bagCount(in AcceptOrderInput) int {
	hot, cold, food := 0, 0, 0
	for _, it := range in.Items {
		switch {
		case it.SKU == "P005":
			food += it.Qty
		case it.SKU == "P006":
			cold += it.Qty
			food += it.Qty
		case it.IceHot == "hot" || in.IceHot == "hot":
			hot += it.Qty
		default:
			cold += it.Qty
		}
	}
	n := 0
	if hot > 0 {
		n++
	}
	if cold > 0 {
		n++
	}
	if food > 0 {
		n++
	}
	if n == 0 {
		n = 1
	}
	return n
}

func isUniqueViolation(err error) bool {
	return err != nil && contains(err.Error(), "duplicate key")
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	}())
}

// BizError 携带 HTTP 状态码的业务错误
type BizError struct {
	Code int
	Msg  string
}

func (e *BizError) Error() string { return e.Msg }

// WriteBizError 供 handler 统一错误输出
func WriteBizError(w http.ResponseWriter, err error) {
	var be *BizError
	if errors.As(err, &be) {
		auth.WriteError(w, be.Code, be.Msg)
		return
	}
	auth.WriteError(w, 500, err.Error())
}

func decode(r *http.Request, v interface{}) error {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}
