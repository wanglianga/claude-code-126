package service

import (
	"context"
	"database/sql"
	"net/http"
	"strconv"
)

// ApproveComp 财务审批赔付方案；通过即计入班组补偿成本
func (a *App) ApproveComp(ctx context.Context, compID, financeID int64, approve bool, note string) error {
	now := a.now(ctx)
	tx, err := a.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var (
		orderID, storeID int64
		amount           float64
		kind, status     string
	)
	err = tx.QueryRowContext(ctx, `
		SELECT c.order_id,o.store_id,c.amount,c.kind,c.status
		FROM compensations c JOIN orders o ON o.id=c.order_id
		WHERE c.id=$1`, compID).Scan(&orderID, &storeID, &amount, &kind, &status)
	if err == sql.ErrNoRows {
		return &BizError{http.StatusNotFound, "赔付方案不存在"}
	}
	if err != nil {
		return err
	}
	if status != "proposed" {
		return &BizError{http.StatusConflict, "赔付方案已处理，不能重复审批"}
	}
	newStatus := "rejected"
	if approve {
		newStatus = "approved"
	}
	if _, err = tx.ExecContext(ctx, `
		UPDATE compensations SET status=$2, approved_by=$3, approved_at=$4, note=COALESCE(NULLIF($5,''),note)
		WHERE id=$1`, compID, newStatus, financeID, now, note); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `
		INSERT INTO incident_threads (incident_id,author_role,author_name,message)
		SELECT incident_id,'finance',$2,
		  CASE WHEN $3 THEN '财务已批准赔付 ¥'||amount||'（'||kind||'）'
		       ELSE '财务驳回赔付方案（'||kind||'）' END
		FROM compensations WHERE id=$1`, compID, "财务小付", approve); err != nil {
		return err
	}
	// 批准的门店承担赔付（顾客退款/重做）进班组补偿成本；骑手等待补贴也计入
	if approve {
		cost := 0.0
		switch kind {
		case "customer_refund", "rework_cost":
			cost = amount
		case "rider_wait":
			cost = amount
		}
		if cost > 0 {
			d := metricDelta{}
			if kind == "rider_wait" {
				d.riderWaitComp = &cost
			} else {
				d.compensationCost = &cost
			}
			if err = a.applyMetrics(ctx, tx, storeID, now, d); err != nil {
				return err
			}
		}
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	a.pushEvent(ctx, storeID, "finance",
		"赔付方案#"+strconv.FormatInt(compID, 10)+(map[bool]string{true: "已批准", false: "已驳回"}[approve]), now)
	return nil
}

// PayComp 财务放款（批准后）
func (a *App) PayComp(ctx context.Context, compID, financeID int64) error {
	now := a.now(ctx)
	var storeID, orderID int64
	err := a.DB.QueryRowContext(ctx,
		`SELECT o.store_id,c.order_id FROM compensations c JOIN orders o ON o.id=c.order_id
		 WHERE c.id=$1 AND c.status='approved'`, compID).Scan(&storeID, &orderID)
	if err == sql.ErrNoRows {
		return &BizError{http.StatusConflict, "赔付方案不存在或未批准"}
	}
	if err != nil {
		return err
	}
	if _, err = a.DB.ExecContext(ctx,
		`UPDATE compensations SET status='paid', paid_at=$2 WHERE id=$1`, compID, now); err != nil {
		return err
	}
	if _, err = a.DB.ExecContext(ctx, `
		INSERT INTO incident_threads (incident_id,author_role,author_name,message)
		SELECT incident_id,'finance','财务小付','赔付已放款到账'
		FROM compensations WHERE id=$1`, compID); err != nil {
		return err
	}
	a.pushEvent(ctx, storeID, "finance", "赔付方案#"+strconv.FormatInt(compID, 10)+"已放款", now)
	return nil
}

// ProposeRiderWaitComp 客服为提前到店等待的骑手提议等待补贴
func (a *App) ProposeRiderWaitComp(ctx context.Context, incidentID int64, amount float64, supportID int64) (int64, error) {
	if amount <= 0 {
		return 0, &BizError{http.StatusBadRequest, "补贴金额必须大于0"}
	}
	var orderID, storeID int64
	err := a.DB.QueryRowContext(ctx,
		`SELECT order_id,store_id FROM incidents WHERE id=$1`, incidentID).
		Scan(&orderID, &storeID)
	if err == sql.ErrNoRows {
		return 0, &BizError{http.StatusNotFound, "异常事件不存在"}
	}
	if err != nil {
		return 0, err
	}
	tx, err := a.DB.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	id, err := a.proposeComp(ctx, tx, incidentID, orderID, "rider_wait",
		amount, "platform", supportID, "骑手提前到店等待补贴")
	if err != nil {
		return 0, err
	}
	if _, err = tx.ExecContext(ctx, `
		INSERT INTO incident_threads (incident_id,author_role,author_name,message)
		VALUES ($1,'support','平台客服',$2)`, incidentID,
		"已发起骑手等待补贴 ¥"+strconv.FormatFloat(amount, 'f', -1, 64)); err != nil {
		return 0, err
	}
	if err = tx.Commit(); err != nil {
		return 0, err
	}
	a.pushEvent(ctx, storeID, "incident", "骑手等待补贴方案已提交财务", a.now(ctx))
	return id, nil
}
