package service

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
)

// RiderArrive 骑手到店打卡：记录到店时间；若饮品未出杯，生成"提前到店等待"异常
func (a *App) RiderArrive(ctx context.Context, orderID, riderID int64) error {
	now := a.now(ctx)
	tx, err := a.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var storeID int64
	var status string
	var assignedRider sql.NullInt64
	var arrived sql.NullTime
	err = tx.QueryRowContext(ctx,
		`SELECT store_id,status,rider_id,rider_arrived_at FROM orders WHERE id=$1`,
		orderID).Scan(&storeID, &status, &assignedRider, &arrived)
	if err == sql.ErrNoRows {
		return &BizError{http.StatusNotFound, "订单不存在"}
	}
	if err != nil {
		return err
	}
	if status == "cancelled" {
		return &BizError{http.StatusConflict, "订单已被平台取消，骑手无需取餐"}
	}
	if status == "handed_over" || status == "completed" {
		return &BizError{http.StatusConflict, "订单已完成交接"}
	}
	if arrived.Valid {
		return &BizError{http.StatusConflict, "骑手已打卡到店"}
	}
	if _, err = tx.ExecContext(ctx,
		`UPDATE orders SET rider_arrived_at=$2, rider_id=COALESCE(rider_id,$3) WHERE id=$1`,
		orderID, now, riderID); err != nil {
		return err
	}
	// 是否已全部出杯
	var ready, total int
	if err = tx.QueryRowContext(ctx,
		`SELECT count(*) FILTER (WHERE status='ready'), count(*)
		 FROM cups WHERE order_id=$1 AND status<>'discarded'`, orderID).Scan(&ready, &total); err != nil {
		return err
	}
	if ready < total {
		if _, err = a.openIncident(ctx, tx, openIncidentIn{
			OrderID: orderID, StoreID: storeID,
			Type: "rider_early_wait", Responsible: "none",
			Detail:       fmt.Sprintf("骑手已到店，仅 %d/%d 杯出杯，骑手等待中", ready, total),
			ReportedRole: "rider", ReportedBy: riderID,
		}); err != nil {
			return err
		}
		n := 1
		if err = a.applyMetrics(ctx, tx, storeID, now, metricDelta{riderWait: &n}); err != nil {
			return err
		}
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	a.pushEvent(ctx, storeID, "rider",
		fmt.Sprintf("骑手已到店取订单#%d（待取 %d/%d）", orderID, ready, total), now)
	return a.bumpStoreBoardAt(ctx, storeID, now)
}

type HandoverInput struct {
	OrderID       int64 `json:"order_id"`
	BagsActual    int   `json:"bags_actual"`
	HotColdSplit  bool  `json:"hot_cold_split"`
	StrawUtensils bool  `json:"straw_utensils_ok"`
}

type HandoverResult struct {
	Passed        bool   `json:"passed"`
	OrderNoMatch  bool   `json:"order_no_match"`
	BagsMatch     bool   `json:"bags_match"`
	HotColdSplit  bool   `json:"hot_cold_split_ok"`
	StrawUtensils bool   `json:"straw_utensils_ok"`
	Message       string `json:"message"`
	IncidentID    *int64 `json:"incident_id,omitempty"`
}

// HandoverCheck 店员与骑手共同核验：订单号、袋数、冷热分袋、吸管餐具。
// 通过则完成交接、骑手取走全部杯；不通过留痕并针对缺失项开异常。
func (a *App) HandoverCheck(ctx context.Context, in HandoverInput, staffID, riderID int64) (*HandoverResult, error) {
	now := a.now(ctx)
	tx, err := a.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	var (
		storeID                     int64
		status                      string
		expectedBags                int
		expectStraw, expectUtensils bool
		orderRiderID                int64
	)
	err = tx.QueryRowContext(ctx, `
		SELECT o.store_id,o.status,COALESCE(o.expected_bags,1),
		       EXISTS(SELECT 1 FROM order_items oi JOIN products p ON p.id=oi.product_id
		              WHERE oi.order_id=o.id AND p.needs_straw),
		       EXISTS(SELECT 1 FROM order_items oi JOIN products p ON p.id=oi.product_id
		              WHERE oi.order_id=o.id AND p.needs_utensils),
		       COALESCE(o.rider_id,0)
		FROM orders o WHERE o.id=$1`, in.OrderID).
		Scan(&storeID, &status, &expectedBags, &expectStraw, &expectUtensils, &orderRiderID)
	if err == sql.ErrNoRows {
		return nil, &BizError{http.StatusNotFound, "订单不存在"}
	}
	if err != nil {
		return nil, err
	}
	if riderID > 0 && orderRiderID > 0 && riderID != orderRiderID {
		return nil, &BizError{http.StatusBadRequest, "当前骑手与订单指派骑手不一致，防止取错袋"}
	}
	if status == "cancelled" {
		return nil, &BizError{http.StatusConflict, "订单已取消，不能交接"}
	}

	// 订单号核验隐含：URL/Body 中订单存在且待取。全部杯必须在待取区
	var readyCups, totalCups int
	if err = tx.QueryRowContext(ctx,
		`SELECT count(*) FILTER (WHERE status='ready'), count(*)
		 FROM cups WHERE order_id=$1 AND status<>'discarded'`, in.OrderID).
		Scan(&readyCups, &totalCups); err != nil {
		return nil, err
	}
	if readyCups < totalCups {
		// 饮品尚未出齐，不能交接；若骑手已在等则补开等待异常（责任在门店出杯慢）
		var arrived bool
		_ = tx.QueryRowContext(ctx, `SELECT rider_arrived_at IS NOT NULL FROM orders WHERE id=$1`,
			in.OrderID).Scan(&arrived)
		if arrived {
			var exists int
			_ = tx.QueryRowContext(ctx,
				`SELECT count(*) FROM incidents WHERE order_id=$1 AND type='rider_early_wait'
				 AND status<>'resolved'`, in.OrderID).Scan(&exists)
			if exists == 0 {
				if _, e := a.openIncident(ctx, tx, openIncidentIn{
					OrderID: in.OrderID, StoreID: storeID, Type: "rider_early_wait",
					Responsible:  "store",
					Detail:       fmt.Sprintf("骑手到店但仅 %d/%d 杯出杯，暂不能交接", readyCups, totalCups),
					ReportedRole: "rider", ReportedBy: riderID,
				}); e != nil {
					return nil, e
				}
			}
		}
		return nil, &BizError{http.StatusConflict,
			fmt.Sprintf("饮品尚未出齐（%d/%d），暂不能交接，请等待出杯", readyCups, totalCups)}
	}
	orderNoMatch := true
	bagsMatch := in.BagsActual == expectedBags
	// 若订单含热饮+冷饮才要求分袋；单一温度无需分袋也算通过
	var needsSplit bool
	_ = tx.QueryRowContext(ctx, `
		SELECT EXISTS(
		  SELECT 1 FROM order_items oi WHERE oi.order_id=$1
		    AND COALESCE(oi.ice_hot,o2.ice_hot,'normal')='hot')
		  AND EXISTS(
		  SELECT 1 FROM order_items oi2 WHERE oi2.order_id=$1
		    AND COALESCE(oi2.ice_hot,o2.ice_hot,'normal')='ice')
		FROM orders o2 WHERE o2.id=$1`, in.OrderID).Scan(&needsSplit)
	hotColdOK := !needsSplit || in.HotColdSplit
	strawOK := true
	if expectStraw || expectUtensils {
		strawOK = in.StrawUtensils
	}
	passed := orderNoMatch && bagsMatch && hotColdOK && strawOK

	note := ""
	var incidentID sql.NullInt64
	if !passed {
		// 取错袋/漏项留痕 + 开异常
		switch {
		case !orderNoMatch:
			note = fmt.Sprintf("订单号/杯数不符：待取区仅 %d/%d 杯，疑似骑手取错袋或未出齐", readyCups, totalCups)
			id, e := a.openIncident(ctx, tx, openIncidentIn{
				OrderID: in.OrderID, StoreID: storeID, Type: "wrong_bag",
				Responsible: "rider", Detail: note, ReportedRole: "staff", ReportedBy: staffID,
			})
			if e != nil {
				return nil, e
			}
			incidentID = sql.NullInt64{Int64: id, Valid: true}
		case !bagsMatch:
			note = fmt.Sprintf("袋数不符：应收 %d 袋，实收 %d 袋", expectedBags, in.BagsActual)
			id, e := a.openIncident(ctx, tx, openIncidentIn{
				OrderID: in.OrderID, StoreID: storeID, Type: "wrong_bag",
				Responsible: "store", Detail: note, ReportedRole: "staff", ReportedBy: staffID,
			})
			if e != nil {
				return nil, e
			}
			incidentID = sql.NullInt64{Int64: id, Valid: true}
		case !hotColdOK:
			note = "冷热饮未分袋，需重新分装后交接"
			id, e := a.openIncident(ctx, tx, openIncidentIn{
				OrderID: in.OrderID, StoreID: storeID, Type: "wrong_bag",
				Responsible: "store", Detail: note, ReportedRole: "staff", ReportedBy: staffID,
			})
			if e != nil {
				return nil, e
			}
			incidentID = sql.NullInt64{Int64: id, Valid: true}
		case !strawOK:
			note = "漏放吸管/餐具，补齐后交接"
			id, e := a.openIncident(ctx, tx, openIncidentIn{
				OrderID: in.OrderID, StoreID: storeID, Type: "missing_utensils",
				Responsible: "staff", Detail: note, ReportedRole: "staff", ReportedBy: staffID,
			})
			if e != nil {
				return nil, e
			}
			incidentID = sql.NullInt64{Int64: id, Valid: true}
			n := 1
			if e = a.applyMetrics(ctx, tx, storeID, now, metricDelta{missingUtensils: &n}); e != nil {
				return nil, e
			}
		}
	}

	if _, err = tx.ExecContext(ctx, `
		INSERT INTO handover_attempts
		  (order_id,rider_id,staff_id,order_no_match,bags_match,hot_cold_split_ok,
		   straw_utensils_ok,passed,note)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
		in.OrderID, nullableRider(riderID), nullableInt64(staffID),
		orderNoMatch, bagsMatch, hotColdOK, strawOK, passed, note); err != nil {
		return nil, err
	}

	if passed {
		if _, err = tx.ExecContext(ctx,
			`UPDATE orders SET status='handed_over', handed_over_at=$2,
			       actual_bags=$3, hot_cold_split=$4, straw_utensils_ok=true WHERE id=$1`,
			in.OrderID, now, in.BagsActual, in.HotColdSplit); err != nil {
			return nil, err
		}
		if _, err = tx.ExecContext(ctx,
			`UPDATE cups SET status='picked_up', picked_up_at=$2
			 WHERE order_id=$1 AND status='ready'`, in.OrderID, now); err != nil {
			return nil, err
		}
		// 提前到店等待异常随交接关闭
		if _, err = tx.ExecContext(ctx,
			`UPDATE incidents SET status='resolved', resolved_at=$2
			 WHERE order_id=$1 AND type='rider_early_wait' AND status<>'resolved'`,
			in.OrderID, now); err != nil {
			return nil, err
		}
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	a.pushEvent(ctx, storeID, "handover",
		fmt.Sprintf("订单#%d 交接核验%s", in.OrderID, map[bool]string{true: "通过", false: "未通过"}[passed]), now)
	if err := a.bumpStoreBoardAt(ctx, storeID, now); err != nil {
		return nil, err
	}
	res := &HandoverResult{
		Passed: passed, OrderNoMatch: orderNoMatch, BagsMatch: bagsMatch,
		HotColdSplit: hotColdOK, StrawUtensils: strawOK,
		Message: map[bool]string{true: "核验通过，骑手取餐", false: note}[passed],
	}
	if incidentID.Valid {
		res.IncidentID = &incidentID.Int64
	}
	return res, nil
}

func nullableRider(v int64) interface{} { return nullableInt64(v) }

// ConfirmDelivered 骑手确认送达（订单进入 completed）
func (a *App) ConfirmDelivered(ctx context.Context, orderID, riderID int64) error {
	now := a.now(ctx)
	var status string
	var assigned int64
	if err := a.DB.QueryRowContext(ctx,
		`SELECT status,COALESCE(rider_id,0) FROM orders WHERE id=$1`, orderID).
		Scan(&status, &assigned); err != nil {
		if err == sql.ErrNoRows {
			return &BizError{http.StatusNotFound, "订单不存在"}
		}
		return err
	}
	if riderID > 0 && assigned > 0 && riderID != assigned {
		return &BizError{http.StatusForbidden, "非本单骑手不能确认送达"}
	}
	if status != "handed_over" {
		return &BizError{http.StatusConflict, "仅已交接订单可确认送达（当前: " + status + "）"}
	}
	var storeID int64
	_ = a.DB.QueryRowContext(ctx, `UPDATE orders SET status='completed' WHERE id=$1 RETURNING store_id`,
		orderID).Scan(&storeID)
	a.pushEvent(ctx, storeID, "delivered", fmt.Sprintf("订单#%d 已送达顾客", orderID), now)
	return nil
}
