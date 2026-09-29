package service

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"time"
)

type CupView struct {
	CupID       int64   `json:"cup_id"`
	OrderID     int64   `json:"order_id"`
	PlatformNo  string  `json:"platform_order_no"`
	QueueSeq    int64   `json:"queue_seq"`
	SealCode    string  `json:"seal_code"`
	ProductName string  `json:"product_name"`
	SKU         string  `json:"sku"`
	IceHot      string  `json:"ice_hot"`
	Sugar       string  `json:"sugar"`
	CupSize     string  `json:"cup_size"`
	Remark      string  `json:"remark"`
	Status      string  `json:"status"`
	IsRemake    bool    `json:"is_remake"`
	Stale       bool    `json:"stale"`
	MadeBy      *string `json:"made_by,omitempty"`
	WaitSecs    int64   `json:"wait_secs,omitempty"`
}

// StaffQueue 门店出杯顺序队列（按 queue_seq）
func (a *App) StaffQueue(ctx context.Context, storeID int64) ([]CupView, error) {
	rows, err := a.DB.QueryContext(ctx, `
		SELECT c.id, c.order_id, o.platform_order_no, c.queue_seq, c.seal_code,
		       pr.name, pr.sku, COALESCE(oi.ice_hot,o.ice_hot,'normal'),
		       COALESCE(oi.sugar,o.sugar,'标准糖'), COALESCE(oi.cup_size,o.cup_size,'中杯'),
		       COALESCE(NULLIF(oi.remark,''), o.remark, ''),
		       c.status, c.is_remake, c.stale_flagged_at IS NOT NULL,
		       u.display_name
		FROM cups c
		JOIN orders o ON o.id=c.order_id
		JOIN order_items oi ON oi.id=c.item_id
		JOIN products pr ON pr.id=oi.product_id
		LEFT JOIN users u ON u.id=c.made_by
		WHERE o.store_id=$1 AND c.status IN ('queued','brewing','ready')
		ORDER BY c.queue_seq`, storeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	now := a.now(ctx)
	out := []CupView{}
	for rows.Next() {
		var v CupView
		var made sql.NullString
		if err := rows.Scan(&v.CupID, &v.OrderID, &v.PlatformNo, &v.QueueSeq, &v.SealCode,
			&v.ProductName, &v.SKU, &v.IceHot, &v.Sugar, &v.CupSize, &v.Remark,
			&v.Status, &v.IsRemake, &v.Stale, &made); err != nil {
			return nil, err
		}
		if made.Valid {
			v.MadeBy = &made.String
		}
		if v.Status == "ready" {
			_ = a.DB.QueryRowContext(ctx,
				`SELECT EXTRACT(EPOCH FROM ($2 - c.into_pickup_at))::bigint
				 FROM cups c WHERE c.id=$1`, v.CupID, now).Scan(&v.WaitSecs)
		}
		out = append(out, v)
	}
	return out, nil
}

// StartBrew 咖啡师按顺序开始制作
func (a *App) StartBrew(ctx context.Context, cupID, staffID int64) error {
	now := a.now(ctx)
	tx, err := a.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var storeID int64
	var status string
	err = tx.QueryRowContext(ctx, `
		SELECT o.store_id, c.status FROM cups c JOIN orders o ON o.id=c.order_id
		WHERE c.id=$1`, cupID).Scan(&storeID, &status)
	if err == sql.ErrNoRows {
		return &BizError{http.StatusNotFound, "杯不存在"}
	}
	if err != nil {
		return err
	}
	if status != "queued" {
		return &BizError{http.StatusConflict, "该杯不在待制作状态（当前: " + status + "）"}
	}
	if _, err = tx.ExecContext(ctx,
		`UPDATE cups SET status='brewing', brewing_started_at=$2, made_by=$3 WHERE id=$1`,
		cupID, now, staffID); err != nil {
		return err
	}
	// 订单进入制作中
	if _, err = tx.ExecContext(ctx,
		`UPDATE orders SET status='brewing' WHERE id=(SELECT order_id FROM cups WHERE id=$1)
		 AND status='accepted'`, cupID); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	a.pushEvent(ctx, storeID, "brew", fmt.Sprintf("封签杯 #%d 开始制作", cupID), now)
	return a.bumpStoreBoardAt(ctx, storeID, now)
}

// SealScan 店员扫描封签码：校验通过后饮品进入待取区
func (a *App) SealScan(ctx context.Context, sealCode string, staffID int64) (*CupView, error) {
	now := a.now(ctx)
	tx, err := a.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	var cupID, orderID, storeID, slaSecs int64
	var status string
	err = tx.QueryRowContext(ctx, `
		SELECT c.id, c.order_id, o.store_id, c.status, s.handover_sla_secs
		FROM cups c JOIN orders o ON o.id=c.order_id
		JOIN stores s ON s.id=o.store_id
		WHERE c.seal_code=$1`, sealCode).Scan(&cupID, &orderID, &storeID, &status, &slaSecs)
	if err == sql.ErrNoRows {
		return nil, &BizError{http.StatusNotFound, "封签码无效: " + sealCode}
	}
	if err != nil {
		return nil, err
	}
	if status != "brewing" {
		return nil, &BizError{http.StatusConflict,
			"该杯未在制作中，无法扫描封签（当前状态: " + status + "）"}
	}
	if _, err = tx.ExecContext(ctx,
		`UPDATE cups SET status='ready', sealed_at=$2, into_pickup_at=$2 WHERE id=$1`,
		cupID, now); err != nil {
		return nil, err
	}

	// 订单是否全部出杯
	var total, ready int
	if err = tx.QueryRowContext(ctx,
		`SELECT count(*), count(*) FILTER (WHERE status='ready')
		 FROM cups WHERE order_id=$1 AND status<>'discarded'`, orderID).
		Scan(&total, &ready); err != nil {
		return nil, err
	}
	slow := false
	if total == ready {
		if _, err = tx.ExecContext(ctx,
			`UPDATE orders SET status='ready', ready_at=$2 WHERE id=$1`, orderID, now); err != nil {
			return nil, err
		}
		// 出杯时效 SLA：下单 -> 全部入待取区
		var breach bool
		if err = tx.QueryRowContext(ctx,
			`SELECT ($2 - ordered_at) > make_interval(secs => $3) FROM orders WHERE id=$1`,
			orderID, now, float64(slaSecs)).Scan(&breach); err != nil {
			return nil, err
		}
		if breach {
			slow = true
			if _, err = tx.ExecContext(ctx,
				`UPDATE orders SET sla_breach=true WHERE id=$1`, orderID); err != nil {
				return nil, err
			}
			n := 1
			if err = a.applyMetrics(ctx, tx, storeID, now, metricDelta{slowOrders: &n}); err != nil {
				return nil, err
			}
			// 若骑手已在等且尚无等待异常，则记录骑手等待事件
			var arrived bool
			_ = tx.QueryRowContext(ctx,
				`SELECT rider_arrived_at IS NOT NULL FROM orders WHERE id=$1`, orderID).Scan(&arrived)
			if arrived {
				var exists int
				_ = tx.QueryRowContext(ctx,
					`SELECT count(*) FROM incidents WHERE order_id=$1
					 AND type='rider_early_wait' AND status<>'resolved'`, orderID).Scan(&exists)
				if exists == 0 {
					if _, err = a.openIncident(ctx, tx, openIncidentIn{
						OrderID: orderID, CupID: cupID, StoreID: storeID,
						Type: "rider_early_wait", Responsible: "store",
						Detail: "出杯超时，骑手提前到店后等待过久", ReportedRole: "staff", ReportedBy: staffID,
					}); err != nil {
						return nil, err
					}
				}
			}
		}
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	a.pushEvent(ctx, storeID, "seal",
		fmt.Sprintf("封签 %s 扫描入待取区（订单#%d）", sealCode, orderID), now)
	if err := a.sweepStaleAt(ctx, now); err != nil {
		return nil, err
	}
	if err := a.bumpStoreBoardAt(ctx, storeID, now); err != nil {
		return nil, err
	}
	v := &CupView{CupID: cupID, OrderID: orderID, SealCode: sealCode, Status: "ready"}
	if slow {
		v.Remark = "SLA_BREACH"
	}
	return v, nil
}

// SweepStale 立即扫描全部门店待取区（供手动检查/定时任务调用）
func (a *App) SweepStale(ctx context.Context) error {
	now := a.now(ctx)
	rows, err := a.DB.QueryContext(ctx, `SELECT id FROM stores WHERE status='active'`)
	if err != nil {
		return err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	rows.Close()
	for range ids {
		if err := a.sweepStaleAt(ctx, now); err != nil {
			return err
		}
	}
	return nil
}

// sweepStaleAt 标记待取区放置过久的杯并触发重做异常（不自动重做，需店员确认重做）
func (a *App) sweepStaleAt(ctx context.Context, now time.Time) error {
	rows, err := a.DB.QueryContext(ctx, `
		SELECT c.id, c.order_id, o.store_id, s.stale_after_secs
		FROM cups c
		JOIN orders o ON o.id=c.order_id
		JOIN stores s ON s.id=o.store_id
		WHERE c.status='ready' AND c.stale_flagged_at IS NULL
		  AND $1 - c.into_pickup_at > make_interval(secs => s.stale_after_secs)`,
		now)
	if err != nil {
		return err
	}
	type stale struct{ cupID, orderID, storeID int64 }
	var list []stale
	for rows.Next() {
		var s stale
		var secs int
		if err := rows.Scan(&s.cupID, &s.orderID, &s.storeID, &secs); err != nil {
			rows.Close()
			return err
		}
		list = append(list, s)
	}
	rows.Close()
	for _, s := range list {
		if err := a.flagStale(ctx, s.cupID, s.orderID, s.storeID, now); err != nil {
			return err
		}
	}
	return nil
}

func (a *App) flagStale(ctx context.Context, cupID, orderID, storeID int64, now time.Time) error {
	tx, err := a.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx,
		`UPDATE cups SET stale_flagged_at=$2 WHERE id=$1 AND stale_flagged_at IS NULL`,
		cupID, now); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx,
		`UPDATE orders SET status='oos_exception' WHERE id=$1 AND status='ready'`, orderID); err != nil {
		return err
	}
	if _, err = a.openIncident(ctx, tx, openIncidentIn{
		OrderID: orderID, CupID: cupID, StoreID: storeID,
		Type: "remake_stale", Responsible: "store",
		Detail: "饮品在待取区放置过久，需重做后再交付骑手", ReportedRole: "system",
	}); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	a.pushEvent(ctx, storeID, "stale",
		fmt.Sprintf("订单#%d 杯#%d 放置过久，已标记需重做", orderID, cupID), now)
	return a.bumpStoreBoardAt(ctx, storeID, now)
}

// RemakeCup 店员确认重做：旧杯报损（重做成本进班组复盘），生成新杯重新进入队列
func (a *App) RemakeCup(ctx context.Context, cupID, staffID int64, reason string) (int64, error) {
	now := a.now(ctx)
	tx, err := a.DB.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	var orderID, storeID, itemID, seq int64
	var status string
	var productCost float64
	err = tx.QueryRowContext(ctx, `
		SELECT c.order_id, o.store_id, c.item_id, c.seq_in_order, c.status, pr.cost
		FROM cups c JOIN orders o ON o.id=c.order_id
		JOIN order_items oi ON oi.id=c.item_id
		JOIN products pr ON pr.id=oi.product_id
		WHERE c.id=$1`, cupID).
		Scan(&orderID, &storeID, &itemID, &seq, &status, &productCost)
	if err == sql.ErrNoRows {
		return 0, &BizError{http.StatusNotFound, "杯不存在"}
	}
	if err != nil {
		return 0, err
	}
	if status == "picked_up" {
		return 0, &BizError{http.StatusConflict, "已被骑手取走的杯不能重做"}
	}

	// 旧杯报损丢弃
	if _, err = tx.ExecContext(ctx,
		`UPDATE cups SET status='discarded', discarded_at=$2 WHERE id=$1`, cupID, now); err != nil {
		return 0, err
	}
	r := reason
	if r == "" {
		r = "remake_stale"
	}
	if _, err = tx.ExecContext(ctx, `
		INSERT INTO stock_losses (store_id,product_id,qty,cost_amount,reason,order_id,cup_id,staff_id)
		SELECT o.store_id, oi.product_id,1,pr.cost,$3,o.id,$1,$2
		FROM cups cu JOIN orders o ON o.id=cu.order_id
		JOIN order_items oi ON oi.id=cu.item_id JOIN products pr ON pr.id=oi.product_id
		WHERE cu.id=$1`, cupID, staffID, r); err != nil {
		return 0, err
	}

	// 重做额外消耗原料
	rows, qerr := tx.QueryContext(ctx,
		`SELECT r.ingredient_id,r.qty FROM recipes r JOIN order_items oi ON oi.product_id=r.product_id
		 WHERE oi.id=$1`, itemID)
	if qerr != nil {
		return 0, qerr
	}
	type ing struct {
		id  int64
		qty float64
	}
	var ings []ing
	for rows.Next() {
		var x ing
		if err := rows.Scan(&x.id, &x.qty); err != nil {
			rows.Close()
			return 0, err
		}
		ings = append(ings, x)
	}
	rows.Close()
	for _, x := range ings {
		if _, err = tx.ExecContext(ctx,
			`UPDATE inventory SET qty=GREATEST(0,qty-$2), updated_at=now()
			 WHERE store_id=$1 AND ingredient_id=$3`, storeID, x.qty, x.id); err != nil {
			return 0, err
		}
		if _, err = tx.ExecContext(ctx,
			`INSERT INTO stock_movements (store_id,ingredient_id,delta,reason,order_id,staff_id)
			 VALUES ($1,$2,$3,'remake_consume',$4,$5)`, storeID, x.id, -x.qty, orderID, staffID); err != nil {
			return 0, err
		}
	}

	// 新杯
	var newCupID int64
	if err = tx.QueryRowContext(ctx, `
		INSERT INTO cups (order_id,item_id,seq_in_order,seal_code,status,queue_seq,
		                  is_remake,remake_of_cup_id)
		VALUES ($1,$2,$3,'SL'||lpad(nextval('seal_code_seq')::text,8,'0'),
		        'queued',nextval('cup_queue_seq'),true,$4)
		RETURNING id`, orderID, itemID, seq, cupID).Scan(&newCupID); err != nil {
		return 0, err
	}
	if _, err = tx.ExecContext(ctx,
		`INSERT INTO cup_label_prints (cup_id,staff_id) VALUES ($1,$2)`, newCupID, staffID); err != nil {
		return 0, err
	}
	if _, err = tx.ExecContext(ctx,
		`UPDATE orders SET status='accepted', ready_at=NULL WHERE id=$1`, orderID); err != nil {
		return 0, err
	}
	// 班组复盘：重做次数 + 重做成本
	n := 1
	cost := productCost
	if err = a.applyMetrics(ctx, tx, storeID, now, metricDelta{
		remake:     &n,
		remakeCost: &cost,
	}); err != nil {
		return 0, err
	}
	// 关联到已有重做异常（若有）
	if _, err = tx.ExecContext(ctx,
		`UPDATE incidents SET status='in_process', resolved_at=NULL
		 WHERE order_id=$1 AND type IN ('remake_stale','wrong_cup','remark_missed','leak_complaint')
		   AND status='resolved'`, orderID); err != nil {
		return 0, err
	}
	if err = tx.Commit(); err != nil {
		return 0, err
	}
	a.pushEvent(ctx, storeID, "remake",
		fmt.Sprintf("订单#%d 旧杯#%d 报损，重做新杯#%d", orderID, cupID, newCupID), now)
	if err := a.bumpStoreBoardAt(ctx, storeID, now); err != nil {
		return 0, err
	}
	return newCupID, nil
}
