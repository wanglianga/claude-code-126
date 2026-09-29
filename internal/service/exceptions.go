package service

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
)

type nameID struct {
	id   int64
	name string
}

type ReportInput struct {
	OrderID int64   `json:"order_id"`
	CupID   int64   `json:"cup_id"`
	Type    string  `json:"type"`
	Detail  string  `json:"detail"`
	Amount  float64 `json:"amount"` // 漏液等自定义补偿金额（可选）
}

var validReportTypes = map[string]bool{
	"remark_missed": true, "wrong_cup": true, "food_oos": true,
	"platform_cancel": true, "leak_complaint": true,
}

// ReportIncident 统一异常上报入口（店员/骑手/顾客/平台客服均可）
func (a *App) ReportIncident(ctx context.Context, in ReportInput, s Session_) (int64, error) {
	if !validReportTypes[in.Type] {
		return 0, &BizError{http.StatusBadRequest,
			"type 必须是 remark_missed/wrong_cup/food_oos/platform_cancel/leak_complaint"}
	}
	switch in.Type {
	case "food_oos":
		return a.reportFoodOOS(ctx, in, s)
	case "platform_cancel":
		return a.reportPlatformCancel(ctx, in, s)
	case "leak_complaint":
		return a.reportLeak(ctx, in, s)
	default:
		return a.reportSimple(ctx, in, s)
	}
}

func (a *App) loadOrder(ctx context.Context, tx *sql.Tx, orderID int64) (storeID int64, status string, totalAmount, totalCost float64, err error) {
	err = tx.QueryRowContext(ctx,
		`SELECT store_id,status,total_amount,total_cost FROM orders WHERE id=$1`, orderID).
		Scan(&storeID, &status, &totalAmount, &totalCost)
	if err == sql.ErrNoRows {
		err = &BizError{http.StatusNotFound, "订单不存在"}
	}
	return
}

// reportSimple 备注漏看 / 错杯：开异常；错杯计入班组评分，建议重做
func (a *App) reportSimple(ctx context.Context, in ReportInput, s Session_) (int64, error) {
	now := a.now(ctx)
	tx, err := a.DB.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	storeID, _, _, _, err := a.loadOrder(ctx, tx, in.OrderID)
	if err != nil {
		return 0, err
	}
	responsible := "staff"
	if in.Type == "remark_missed" {
		responsible = "staff"
	}
	id, err := a.openIncident(ctx, tx, openIncidentIn{
		OrderID: in.OrderID, CupID: in.CupID, StoreID: storeID, Type: in.Type,
		Responsible: responsible, Detail: in.Detail,
		ReportedRole: roleCN2(s.Role), ReportedBy: s.ID,
	})
	if err != nil {
		return 0, err
	}
	d := metricDelta{}
	if in.Type == "wrong_cup" {
		n := 1
		d.wrongCup = &n
	}
	if d.wrongCup != nil {
		if err = a.applyMetrics(ctx, tx, storeID, now, d); err != nil {
			return 0, err
		}
	}
	if err = tx.Commit(); err != nil {
		return 0, err
	}
	a.pushEvent(ctx, storeID, "incident",
		fmt.Sprintf("订单#%d 上报异常：%s（#%d）", in.OrderID, incidentCN(in.Type), id), now)
	return id, a.bumpStoreBoard(ctx, storeID)
}

func incidentCN(t string) string {
	return map[string]string{
		"rider_early_wait": "骑手提前到店等待", "remake_stale": "饮品过久重做",
		"remark_missed": "备注漏看", "wrong_cup": "错杯", "food_oos": "轻食缺货",
		"wrong_bag": "取错袋/交接不符", "platform_cancel": "平台取消",
		"leak_complaint": "漏液投诉", "missing_utensils": "漏餐具",
	}[t]
}

// reportFoodOOS 轻食制作时缺货：临时下架该商品，异常联动客服处理顾客方案
func (a *App) reportFoodOOS(ctx context.Context, in ReportInput, s Session_) (int64, error) {
	now := a.now(ctx)
	tx, err := a.DB.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	storeID, status, _, _, err := a.loadOrder(ctx, tx, in.OrderID)
	if err != nil {
		return 0, err
	}
	// 找到订单中的轻食/套餐商品
	rows, qerr := tx.QueryContext(ctx, `
		SELECT DISTINCT oi.product_id, p.name
		FROM order_items oi JOIN products p ON p.id=oi.product_id
		WHERE oi.order_id=$1 AND p.category IN ('light_food','combo')`, in.OrderID)
	if qerr != nil {
		return 0, qerr
	}
	type pp = nameID
	var prods []pp
	for rows.Next() {
		var x pp
		if err := rows.Scan(&x.id, &x.name); err != nil {
			rows.Close()
			return 0, err
		}
		prods = append(prods, x)
	}
	rows.Close()
	if len(prods) == 0 {
		return 0, &BizError{http.StatusBadRequest, "该订单不含轻食/套餐，无需缺货下架"}
	}
	// 标记订单异常态（仍可出饮品部分）
	if status == "accepted" || status == "brewing" {
		if _, err = tx.ExecContext(ctx,
			`UPDATE orders SET status='oos_exception' WHERE id=$1`, in.OrderID); err != nil {
			return 0, err
		}
	}
	id, err := a.openIncident(ctx, tx, openIncidentIn{
		OrderID: in.OrderID, StoreID: storeID, Type: "food_oos", Responsible: "store",
		Detail:       fmt.Sprintf("轻食缺货：%v；已触发临时下架", namesOf(prods)),
		ReportedRole: roleCN2(s.Role), ReportedBy: s.ID,
	})
	if err != nil {
		return 0, err
	}
	for _, p := range prods {
		if _, err = tx.ExecContext(ctx, `
			INSERT INTO temporary_delistings (store_id,product_id,reason,incident_id,created_by)
			VALUES ($1,$2,'原料缺货临时下架',$3,$4)`, storeID, p.id, id, nullableInt64(s.ID)); err != nil {
			return 0, err
		}
		// 订单内该商品标记缺货
		if _, err = tx.ExecContext(ctx,
			`UPDATE order_items SET shortage=true WHERE order_id=$1 AND product_id=$2`, in.OrderID, p.id); err != nil {
			return 0, err
		}
	}
	if err = tx.Commit(); err != nil {
		return 0, err
	}
	a.pushEvent(ctx, storeID, "delist",
		fmt.Sprintf("轻食缺货，%d 个商品临时下架（异常#%d）", len(prods), id), now)
	return id, a.bumpStoreBoard(ctx, storeID)
}

func namesOf(prods []nameID) []string {
	out := make([]string, 0, len(prods))
	for _, p := range prods {
		out = append(out, p.name)
	}
	return out
}

// reportPlatformCancel 平台取消：释放未制作杯对应原料、报损在制品、损失毛利计入班组与门店利润
func (a *App) reportPlatformCancel(ctx context.Context, in ReportInput, s Session_) (int64, error) {
	now := a.now(ctx)
	tx, err := a.DB.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	storeID, status, totalAmount, totalCost, err := a.loadOrder(ctx, tx, in.OrderID)
	if err != nil {
		return 0, err
	}
	if status == "cancelled" || status == "completed" || status == "handed_over" {
		return 0, &BizError{http.StatusConflict, "订单当前状态不能取消: " + status}
	}

	id, err := a.openIncident(ctx, tx, openIncidentIn{
		OrderID: in.OrderID, StoreID: storeID, Type: "platform_cancel", Responsible: "platform",
		Detail: in.Detail, ReportedRole: roleCN2(s.Role), ReportedBy: s.ID,
	})
	if err != nil {
		return 0, err
	}

	// 回补原料：未制作(queued)杯 × 配方（每条配方仅对应一种原料，不会重复）
	type ing struct {
		id  int64
		qty float64
	}
	release := map[int64]float64{}
	rows, _ := tx.QueryContext(ctx, `
		SELECT r.ingredient_id, r.qty *
		       (SELECT count(*) FROM cups cu
		         WHERE cu.item_id=oi.id AND cu.status='queued')
		FROM order_items oi JOIN recipes r ON r.product_id=oi.product_id
		WHERE oi.order_id=$1`, in.OrderID)
	for rows.Next() {
		var idd int64
		var releaseQty float64
		if err := rows.Scan(&idd, &releaseQty); err != nil {
			rows.Close()
			return 0, err
		}
		if releaseQty > 0 {
			release[idd] += releaseQty
		}
	}
	rows.Close()

	// 在制/已出杯按商品汇总报损成本（独立查询，避免与配方 JOIN 造成杯数翻倍）
	type wip struct {
		productID int64
		qty       int
		unitCost  float64
	}
	wipList := []wip{}
	wrows, _ := tx.QueryContext(ctx, `
		SELECT oi.product_id,
		       (SELECT count(*) FROM cups cu
		         WHERE cu.item_id=oi.id AND cu.status IN ('brewing','ready')),
		       pr.cost
		FROM order_items oi JOIN products pr ON pr.id=oi.product_id
		WHERE oi.order_id=$1`, in.OrderID)
	for wrows.Next() {
		var w wip
		if err := wrows.Scan(&w.productID, &w.qty, &w.unitCost); err != nil {
			wrows.Close()
			return 0, err
		}
		if w.qty > 0 {
			wipList = append(wipList, w)
		}
	}
	wrows.Close()

	if _, err = tx.ExecContext(ctx,
		`UPDATE cups SET status='discarded', discarded_at=$2
		 WHERE order_id=$1 AND status IN ('queued','brewing','ready')`, in.OrderID, now); err != nil {
		return 0, err
	}
	for ingID, qty := range release {
		if _, err = tx.ExecContext(ctx,
			`UPDATE inventory SET qty=qty+$2, updated_at=now()
			 WHERE store_id=$1 AND ingredient_id=$3`, storeID, qty, ingID); err != nil {
			return 0, err
		}
		if _, err = tx.ExecContext(ctx,
			`INSERT INTO stock_movements (store_id,ingredient_id,delta,reason,order_id)
			 VALUES ($1,$2,$3,'cancel_release',$4)`, storeID, ingID, qty, in.OrderID); err != nil {
			return 0, err
		}
	}
	for _, w := range wipList {
		if _, err = tx.ExecContext(ctx, `
			INSERT INTO stock_losses (store_id,product_id,qty,cost_amount,reason,order_id,incident_id)
			VALUES ($1,$2,$3,$4,'cancel_in_progress',$5,$6)`,
			storeID, w.productID, w.qty, w.unitCost*float64(w.qty), in.OrderID, id); err != nil {
			return 0, err
		}
	}

	if _, err = tx.ExecContext(ctx,
		`UPDATE orders SET status='cancelled', cancelled_at=$2,
		        cancel_reason=$3, lost_margin=$4 WHERE id=$1`,
		in.OrderID, now, nz(in.Detail, "平台取消"), totalAmount-totalCost); err != nil {
		return 0, err
	}
	// 班组：冲回收入/毛利，计入平台取消与毛利损失
	negRev, negGP := -totalAmount, -(totalAmount - totalCost)
	loss := totalAmount - totalCost
	n := 1
	if err = a.applyMetrics(ctx, tx, storeID, now, metricDelta{
		platformCancels: &n, cancelLoss: &loss,
		revenue: &negRev, grossProfit: &negGP,
	}); err != nil {
		return 0, err
	}
	// 平台承担取消赔付（预期毛利），进入财务审批
	if _, err = a.proposeComp(ctx, tx, id, in.OrderID, "platform_penalty",
		totalAmount-totalCost, "platform", s.ID, "平台取消导致门店预期毛利损失"); err != nil {
		return 0, err
	}
	if err = tx.Commit(); err != nil {
		return 0, err
	}
	a.pushEvent(ctx, storeID, "cancel",
		fmt.Sprintf("订单#%d 被平台取消，释放原料并报损在制品", in.OrderID), now)
	return id, a.bumpStoreBoard(ctx, storeID)
}

// reportLeak 顾客收货后投诉漏液：客服开单，提议顾客退款/券，门店承担（可复议为平台）
func (a *App) reportLeak(ctx context.Context, in ReportInput, s Session_) (int64, error) {
	now := a.now(ctx)
	tx, err := a.DB.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	storeID, _, totalAmount, _, err := a.loadOrder(ctx, tx, in.OrderID)
	if err != nil {
		return 0, err
	}
	id, err := a.openIncident(ctx, tx, openIncidentIn{
		OrderID: in.OrderID, CupID: in.CupID, StoreID: storeID, Type: "leak_complaint",
		Responsible: "store", Detail: in.Detail,
		ReportedBy: s.ID, ReportedRole: roleCN2(s.Role),
	})
	if err != nil {
		return 0, err
	}
	amount := in.Amount
	if amount <= 0 {
		amount = totalAmount // 默认整单退款
	}
	if _, err = a.proposeComp(ctx, tx, id, in.OrderID, "customer_refund",
		amount, "store", s.ID, "漏液投诉顾客退款方案"); err != nil {
		return 0, err
	}
	coupon := 10.0
	if _, err = a.proposeComp(ctx, tx, id, in.OrderID, "customer_coupon",
		coupon, "platform", s.ID, "平台代发安慰券"); err != nil {
		return 0, err
	}
	n := 1
	if err = a.applyMetrics(ctx, tx, storeID, now, metricDelta{leak: &n}); err != nil {
		return 0, err
	}
	if err = tx.Commit(); err != nil {
		return 0, err
	}
	a.pushEvent(ctx, storeID, "complaint",
		fmt.Sprintf("订单#%d 顾客投诉漏液，已生成赔付方案（异常#%d）", in.OrderID, id), now)
	return id, nil
}

// 语法糖：填充 openIncidentIn 字段（兼容 ReportedRole 字段名）
func roleCN2(role string) string {
	switch role {
	case "store_staff":
		return "staff"
	case "platform_support":
		return "support"
	case "rider":
		return "rider"
	case "customer":
		return "customer"
	}
	return role
}
