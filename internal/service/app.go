package service

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

// App 持有数据库、Redis 与业务辅助方法，供各 handler 使用
type App struct {
	DB  *sql.DB
	RDB *redis.Client
}

func New(db *sql.DB, rdb *redis.Client) *App {
	return &App{DB: db, RDB: rdb}
}

// now 返回业务当前时间。支持演示时钟偏移（POST /api/demo/clock），
// 便于演示"骑手提前到店""饮品放置过久"等时间相关场景
func (a *App) now(ctx context.Context) time.Time {
	var offSecs int64
	if v, err := a.RDB.Get(ctx, "demo:clock_offset_secs").Result(); err == nil {
		offSecs, _ = strconv.ParseInt(v, 10, 64)
	}
	return time.Now().Add(time.Duration(offSecs) * time.Second)
}

func (a *App) SetClockOffset(ctx context.Context, secs int64) error {
	if secs == 0 {
		return a.RDB.Del(ctx, "demo:clock_offset_secs").Err()
	}
	return a.RDB.Set(ctx, "demo:clock_offset_secs", secs, 0).Err()
}

func shiftNameFor(t time.Time) string {
	h := t.Hour()
	switch {
	case h >= 6 && h < 11:
		return "morning"
	case h >= 11 && h < 16:
		return "mid"
	default:
		return "evening"
	}
}

// ensureShift 返回（必要时创建）门店当时所属班组
func (a *App) ensureShift(ctx context.Context, tx *sql.Tx, storeID int64, t time.Time) (int64, error) {
	day := t.Format("2006-01-02")
	name := shiftNameFor(t)
	var id int64
	err := tx.QueryRowContext(ctx,
		`INSERT INTO shifts (store_id, work_date, shift_name)
		 VALUES ($1,$2,$3)
		 ON CONFLICT (store_id, work_date, shift_name) DO UPDATE SET work_date=EXCLUDED.work_date
		 RETURNING id`, storeID, day, name).Scan(&id)
	return id, err
}

// metricDelta 班组指标增量（负值用于冲回）
type metricDelta struct {
	slowOrders       *int
	wrongCup         *int
	missingUtensils  *int
	remake           *int
	platformCancels  *int
	leak             *int
	riderWait        *int
	remakeCost       *float64
	compensationCost *float64
	riderWaitComp    *float64
	cancelLoss       *float64
	revenue          *float64
	grossProfit      *float64
}

// applyMetrics 在事务内累计班组指标并重算评分
func (a *App) applyMetrics(ctx context.Context, tx *sql.Tx, storeID int64, t time.Time, d metricDelta) error {
	shiftID, err := a.ensureShift(ctx, tx, storeID, t)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO shift_metrics (shift_id) VALUES ($1)
		ON CONFLICT (shift_id) DO NOTHING`, shiftID)
	if err != nil {
		return err
	}
	q := `UPDATE shift_metrics SET
		slow_orders       = slow_orders + $2,
		wrong_cup_count   = wrong_cup_count + $3,
		missing_utensils  = GREATEST(0, missing_utensils + $4),
		remake_count      = remake_count + $5,
		platform_cancels  = platform_cancels + $6,
		leak_count        = leak_count + $7,
		rider_wait_count  = rider_wait_count + $8,
		remake_cost       = remake_cost + $9,
		compensation_cost= compensation_cost + $10,
		rider_wait_comp  = rider_wait_comp + $11,
		cancel_loss       = cancel_loss + $12,
		revenue           = revenue + $13,
		gross_profit      = gross_profit + $14,
		updated_at       = now()
	  WHERE shift_id = $1`
	ival := func(p *int) int {
		if p != nil {
			return *p
		}
		return 0
	}
	fval := func(p *float64) float64 {
		if p != nil {
			return *p
		}
		return 0
	}
	if _, err = tx.ExecContext(ctx, q, shiftID,
		ival(d.slowOrders), ival(d.wrongCup), ival(d.missingUtensils),
		ival(d.remake), ival(d.platformCancels), ival(d.leak), ival(d.riderWait),
		fval(d.remakeCost), fval(d.compensationCost), fval(d.riderWaitComp),
		fval(d.cancelLoss), fval(d.revenue), fval(d.grossProfit)); err != nil {
		return err
	}
	return a.recalcScore(ctx, tx, shiftID)
}

// recalcScore 班组评分：满分100，按异常扣分，保底0
func (a *App) recalcScore(ctx context.Context, tx *sql.Tx, shiftID int64) error {
	_, err := tx.ExecContext(ctx, `
		UPDATE shift_metrics SET score = GREATEST(0, LEAST(100,
			100 - slow_orders*2 - wrong_cup_count*5 - missing_utensils*2
			    - remake_count*3 - platform_cancels*4 - leak_count*5
		)) WHERE shift_id = $1`, shiftID)
	return err
}

var errConflict = fmt.Errorf("conflict")
