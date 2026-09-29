package service

import (
	"context"
	"database/sql"
	"net/http"
	"strconv"
)

type StoreDailyMetric struct {
	StoreID          int64   `json:"store_id"`
	StoreCode        string  `json:"store_code"`
	StoreName        string  `json:"store_name"`
	Day              string  `json:"day"`
	SlowOrders       int     `json:"slow_orders"`
	WrongCupCount    int     `json:"wrong_cup_count"`
	MissingUtensils  int     `json:"missing_utensils"`
	RemakeCount      int     `json:"remake_count"`
	PlatformCancels  int     `json:"platform_cancels"`
	LeakCount        int     `json:"leak_count"`
	RiderWaitCount   int     `json:"rider_wait_count"`
	RemakeCost       float64 `json:"remake_cost"`
	CompensationCost float64 `json:"compensation_cost"`
	RiderWaitComp    float64 `json:"rider_wait_comp"`
	CancelLoss       float64 `json:"cancel_loss"`
	Revenue          float64 `json:"revenue"`
	GrossProfit      float64 `json:"gross_profit"`
	NetProfit        float64 `json:"net_profit"`
	// 异常对利润的总影响（重做成本+顾客补偿+骑手等待补贴+取消毛利损失）
	ProfitImpactTotal float64 `json:"profit_impact_total"`
}

// StoreDailyMetrics 区域经理按门店看出杯慢/错杯/漏餐具/重做/平台取消对利润的影响
func (a *App) StoreDailyMetrics(ctx context.Context, regionID, storeID int64, from, to string) ([]StoreDailyMetric, error) {
	rows, err := a.DB.QueryContext(ctx, `
		SELECT v.store_id,s.code,s.name,to_char(v.day,'YYYY-MM-DD'),
		       v.slow_orders,v.wrong_cup_count,v.missing_utensils,v.remake_count,
		       v.platform_cancels,v.leak_count,v.rider_wait_count,
		       v.remake_cost,v.compensation_cost,v.rider_wait_comp,v.cancel_loss,
		       v.revenue,v.gross_profit,v.net_profit
		FROM v_store_daily_metrics v
		JOIN stores s ON s.id=v.store_id
		WHERE ($1=0 OR s.region_id=$1)
		  AND ($2=0 OR v.store_id=$2)
		  AND ($3='' OR v.day >= $3::date)
		  AND ($4='' OR v.day <= $4::date)
		ORDER BY v.day DESC, v.store_id`, regionID, storeID, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []StoreDailyMetric{}
	for rows.Next() {
		var m StoreDailyMetric
		if err := rows.Scan(&m.StoreID, &m.StoreCode, &m.StoreName, &m.Day,
			&m.SlowOrders, &m.WrongCupCount, &m.MissingUtensils, &m.RemakeCount,
			&m.PlatformCancels, &m.LeakCount, &m.RiderWaitCount,
			&m.RemakeCost, &m.CompensationCost, &m.RiderWaitComp, &m.CancelLoss,
			&m.Revenue, &m.GrossProfit, &m.NetProfit); err != nil {
			return nil, err
		}
		m.ProfitImpactTotal = m.RemakeCost + m.CompensationCost + m.RiderWaitComp + m.CancelLoss
		out = append(out, m)
	}
	return out, nil
}

type ShiftScore struct {
	ShiftID          int64   `json:"shift_id"`
	StoreCode        string  `json:"store_code"`
	StoreName        string  `json:"store_name"`
	Day              string  `json:"day"`
	ShiftName        string  `json:"shift_name"`
	LeaderName       string  `json:"leader_name"`
	SlowOrders       int     `json:"slow_orders"`
	WrongCupCount    int     `json:"wrong_cup_count"`
	MissingUtensils  int     `json:"missing_utensils"`
	RemakeCount      int     `json:"remake_count"`
	PlatformCancels  int     `json:"platform_cancels"`
	LeakCount        int     `json:"leak_count"`
	RemakeCost       float64 `json:"remake_cost"`
	CompensationCost float64 `json:"compensation_cost"`
	CancelLoss       float64 `json:"cancel_loss"`
	Score            float64 `json:"score"`
	ReviewCount      int     `json:"review_count"`
}

// ShiftScores 门店班组评分（区域督导同步复盘用）
func (a *App) ShiftScores(ctx context.Context, regionID, storeID int64, from, to string) ([]ShiftScore, error) {
	rows, err := a.DB.QueryContext(ctx, `
		SELECT sh.id,s.code,s.name,to_char(sh.work_date,'YYYY-MM-DD'),sh.shift_name,
		       COALESCE(u.display_name,'-'),
		       m.slow_orders,m.wrong_cup_count,m.missing_utensils,m.remake_count,
		       m.platform_cancels,m.leak_count,
		       m.remake_cost,m.compensation_cost,m.cancel_loss,m.score,
		       (SELECT count(*) FROM shift_reviews r WHERE r.shift_id=sh.id)
		FROM shifts sh
		JOIN stores s ON s.id=sh.store_id
		JOIN shift_metrics m ON m.shift_id=sh.id
		LEFT JOIN users u ON u.id=sh.leader_id
		WHERE ($1=0 OR s.region_id=$1)
		  AND ($2=0 OR sh.store_id=$2)
		  AND ($3='' OR sh.work_date >= $3::date)
		  AND ($4='' OR sh.work_date <= $4::date)
		ORDER BY sh.work_date DESC, sh.shift_name, s.code`, regionID, storeID, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ShiftScore{}
	for rows.Next() {
		var v ShiftScore
		var leader sql.NullString
		if err := rows.Scan(&v.ShiftID, &v.StoreCode, &v.StoreName, &v.Day, &v.ShiftName,
			&leader, &v.SlowOrders, &v.WrongCupCount, &v.MissingUtensils, &v.RemakeCount,
			&v.PlatformCancels, &v.LeakCount, &v.RemakeCost, &v.CompensationCost,
			&v.CancelLoss, &v.Score, &v.ReviewCount); err != nil {
			return nil, err
		}
		v.LeaderName = leader.String
		out = append(out, v)
	}
	return out, nil
}

// AddShiftReview 督导对班组写复盘（重做成本等已在指标中体现）
func (a *App) AddShiftReview(ctx context.Context, shiftID, supervisorID int64, content string) error {
	if content == "" {
		return &BizError{http.StatusBadRequest, "复盘内容不能为空"}
	}
	var storeID int64
	err := a.DB.QueryRowContext(ctx, `SELECT store_id FROM shifts WHERE id=$1`, shiftID).
		Scan(&storeID)
	if err == sql.ErrNoRows {
		return &BizError{http.StatusNotFound, "班组不存在"}
	}
	if err != nil {
		return err
	}
	if _, err = a.DB.ExecContext(ctx,
		`INSERT INTO shift_reviews (shift_id,supervisor_id,content) VALUES ($1,$2,$3)`,
		shiftID, supervisorID, content); err != nil {
		return err
	}
	a.pushEvent(ctx, storeID, "review",
		"区域督导已提交班组复盘（班组#"+strconv.FormatInt(shiftID, 10)+"）", a.now(ctx))
	return nil
}
