package service

import (
	"context"
	"net/http"
	"time"
)

// SetAcceptance 门店爆单保护：临时限制接单（paused）/限流（throttled）/恢复（open）
func (a *App) SetAcceptance(ctx context.Context, storeID int64, mode, note string, userID int64) error {
	now := a.now(ctx)
	if mode != "open" && mode != "throttled" && mode != "paused" {
		return &BizError{http.StatusBadRequest, "mode 必须为 open/throttled/paused"}
	}
	tx, err := a.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx,
		`UPDATE stores SET acceptance=$2, updated_at=now() WHERE id=$1`, storeID, mode)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return &BizError{http.StatusNotFound, "门店不存在"}
	}
	evType := map[string]string{
		"paused": "manual_pause", "open": "manual_resume", "throttled": "triggered",
	}[mode]
	if mode == "open" {
		evType = "released"
	}
	var depth int
	_ = tx.QueryRowContext(ctx,
		`SELECT count(*) FROM cups c JOIN orders o ON o.id=c.order_id
		 WHERE o.store_id=$1 AND c.status IN ('queued','brewing')`, storeID).Scan(&depth)
	if _, err = tx.ExecContext(ctx,
		`INSERT INTO overload_events (store_id,type,queue_depth,note,created_by)
		 VALUES ($1,$2,$3,$4,$5)`, storeID, evType, depth, note, nullableInt64(userID)); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	a.pushEvent(ctx, storeID, "acceptance",
		"门店接单状态变更为："+modeCN(mode)+"（"+note+"）", now)
	return a.bumpStoreBoardAt(ctx, storeID, now)
}

func modeCN(m string) string {
	return map[string]string{"open": "正常接单", "throttled": "爆单限流", "paused": "暂停接单"}[m]
}

// RestoreDelisting 恢复临时下架（补货后店长手动恢复）
func (a *App) RestoreDelisting(ctx context.Context, delistingID int64) error {
	now := a.now(ctx)
	var storeID int64
	err := a.DB.QueryRowContext(ctx,
		`UPDATE temporary_delistings SET active=false, restored_at=$2
		 WHERE id=$1 AND active=true RETURNING store_id`, delistingID, now).Scan(&storeID)
	if err != nil {
		return &BizError{http.StatusNotFound, "下架记录不存在或已恢复"}
	}
	if err != nil {
		return err
	}
	a.pushEvent(ctx, storeID, "relist", "商品已补货，恢复上架", now)
	return nil
}

// Replenish 门店原料补货
func (a *App) Replenish(ctx context.Context, storeID, ingredientID int64, qty float64, staffID int64) error {
	if qty <= 0 {
		return &BizError{http.StatusBadRequest, "补货数量必须大于0"}
	}
	tx, err := a.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var exists int
	_ = tx.QueryRowContext(ctx,
		`SELECT count(*) FROM inventory WHERE store_id=$1 AND ingredient_id=$2`,
		storeID, ingredientID).Scan(&exists)
	if exists == 0 {
		if _, err = tx.ExecContext(ctx,
			`INSERT INTO inventory (store_id,ingredient_id,qty) VALUES ($1,$2,$3)`,
			storeID, ingredientID, qty); err != nil {
			return err
		}
	} else if _, err = tx.ExecContext(ctx,
		`UPDATE inventory SET qty=qty+$3, updated_at=now()
		 WHERE store_id=$1 AND ingredient_id=$2`, storeID, ingredientID, qty); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx,
		`INSERT INTO stock_movements (store_id,ingredient_id,delta,reason,staff_id)
		 VALUES ($1,$2,$3,'replenish',$4)`, storeID, ingredientID, qty, nullableInt64(staffID)); err != nil {
		return err
	}
	return tx.Commit()
}

var _ = time.Time{}
