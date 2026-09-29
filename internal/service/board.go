package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

// BoardEvent 到店屏/后厨屏事件流
type BoardEvent struct {
	At      time.Time `json:"at"`
	Kind    string    `json:"kind"`
	Message string    `json:"message"`
}

type BoardOrder struct {
	OrderID         int64      `json:"order_id"`
	PlatformOrderNo string     `json:"platform_order_no"`
	PlatformCode    string     `json:"platform_code"`
	RiderName       string     `json:"rider_name,omitempty"`
	RiderArrivedAt  *time.Time `json:"rider_arrived_at,omitempty"`
	RiderWaitSecs   int64      `json:"rider_wait_secs,omitempty"`
	ExpectedBags    int        `json:"expected_bags"`
	HotColdSplit    bool       `json:"hot_cold_split"`
	ReadyCups       int        `json:"ready_cups"`
	TotalCups       int        `json:"total_cups"`
	StaleCups       int        `json:"stale_cups"`
	EarliestReadyAt *time.Time `json:"earliest_ready_at,omitempty"`
	Status          string     `json:"status"`
}

type Board struct {
	StoreCode     string       `json:"store_code"`
	StoreName     string       `json:"store_name"`
	Acceptance    string       `json:"acceptance"`
	QueueDepth    int          `json:"queue_depth"`
	QueuedCups    int          `json:"queued_cups"`
	BrewingCups   int          `json:"brewing_cups"`
	ReadyOrders   []BoardOrder `json:"ready_orders"`
	WaitingRiders []BoardOrder `json:"waiting_riders"` // 已到店但未取齐
	StaleCups     int          `json:"stale_cups"`
	GeneratedAt   time.Time    `json:"generated_at"`
}

func boardKey(storeID int64) string  { return fmt.Sprintf("store:%d:board", storeID) }
func eventsKey(storeID int64) string { return fmt.Sprintf("store:%d:events", storeID) }

func (a *App) pushEvent(ctx context.Context, storeID int64, kind, msg string, t time.Time) {
	ev := BoardEvent{At: t, Kind: kind, Message: msg}
	b, _ := json.Marshal(ev)
	a.RDB.LPush(ctx, eventsKey(storeID), b)
	a.RDB.LTrim(ctx, eventsKey(storeID), 0, 49)
}

// bumpStoreBoard 以数据库为准重算门店实时屏并写入 Redis
func (a *App) bumpStoreBoard(ctx context.Context, storeID int64) error {
	return a.bumpStoreBoardAt(ctx, storeID, a.now(ctx))
}

func (a *App) bumpStoreBoardAt(ctx context.Context, storeID int64, now time.Time) error {
	var code, name, acceptance string
	var staleAfterSecs int
	if err := a.DB.QueryRowContext(ctx,
		`SELECT code,name,acceptance,stale_after_secs FROM stores WHERE id=$1`, storeID).
		Scan(&code, &name, &acceptance, &staleAfterSecs); err != nil {
		return err
	}
	var queued, brewing, stale int
	_ = a.DB.QueryRowContext(ctx, `
		SELECT
		 count(*) FILTER (WHERE c.status='queued'),
		 count(*) FILTER (WHERE c.status='brewing'),
		 count(*) FILTER (WHERE c.status='ready'
		                   AND $2 - c.into_pickup_at > make_interval(secs => $3))
		FROM cups c JOIN orders o ON o.id=c.order_id
		WHERE o.store_id=$1 AND c.status IN ('queued','brewing','ready')`,
		storeID, now, float64(staleAfterSecs)).Scan(&queued, &brewing, &stale)

	b := Board{
		StoreCode: code, StoreName: name, Acceptance: acceptance,
		QueuedCups: queued, BrewingCups: brewing, QueueDepth: queued + brewing,
		GeneratedAt: now, ReadyOrders: []BoardOrder{}, WaitingRiders: []BoardOrder{},
	}

	// 待取区订单（含已到店骑手等待）
	rows, err := a.DB.QueryContext(ctx, `
		SELECT o.id, o.platform_order_no, p.code, COALESCE(r.name,''),
		       o.rider_arrived_at, COALESCE(o.expected_bags,1), COALESCE(o.hot_cold_split,false),
		       count(*) FILTER (WHERE c.status='ready'),
		       count(*),
		       count(*) FILTER (WHERE c.status='ready' AND c.stale_flagged_at IS NOT NULL),
		       min(c.into_pickup_at) FILTER (WHERE c.status='ready'),
		       o.status
		FROM orders o
		JOIN platforms p ON p.id=o.platform_id
		LEFT JOIN riders r ON r.id=o.rider_id
		JOIN cups c ON c.order_id=o.id
		WHERE o.store_id=$1 AND o.status IN ('accepted','oos_exception','brewing','ready')
		GROUP BY o.id,p.code,r.name
		HAVING count(*) FILTER (WHERE c.status='ready') > 0
		       OR o.rider_arrived_at IS NOT NULL
		ORDER BY min(c.into_pickup_at), o.rider_arrived_at`, storeID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var bo BoardOrder
		var riderName, status string
		var arrived, earliest sql.NullTime
		if err := rows.Scan(&bo.OrderID, &bo.PlatformOrderNo, &bo.PlatformCode, &riderName,
			&arrived, &bo.ExpectedBags, &bo.HotColdSplit,
			&bo.ReadyCups, &bo.TotalCups, &bo.StaleCups, &earliest, &status); err != nil {
			return err
		}
		bo.RiderName = riderName
		bo.Status = status
		if arrived.Valid {
			bo.RiderArrivedAt = &arrived.Time
			bo.RiderWaitSecs = int64(now.Sub(arrived.Time).Seconds())
		}
		if earliest.Valid {
			bo.EarliestReadyAt = &earliest.Time
		}
		b.StaleCups += bo.StaleCups
		if bo.ReadyCups >= bo.TotalCups {
			b.ReadyOrders = append(b.ReadyOrders, bo)
		}
		if arrived.Valid && bo.ReadyCups < bo.TotalCups {
			b.WaitingRiders = append(b.WaitingRiders, bo)
		}
	}
	data, _ := json.Marshal(b)
	a.RDB.Set(ctx, boardKey(storeID), data, 0)
	return nil
}

func (a *App) GetBoard(ctx context.Context, storeID int64) (Board, error) {
	var b Board
	data, err := a.RDB.Get(ctx, boardKey(storeID)).Bytes()
	if err != nil {
		if err := a.bumpStoreBoard(ctx, storeID); err != nil {
			return b, err
		}
		data, err = a.RDB.Get(ctx, boardKey(storeID)).Bytes()
		if err != nil {
			return b, err
		}
	}
	err = json.Unmarshal(data, &b)
	return b, err
}

func (a *App) GetEvents(ctx context.Context, storeID int64, limit int64) ([]BoardEvent, error) {
	raw, err := a.RDB.LRange(ctx, eventsKey(storeID), 0, limit-1).Result()
	if err != nil {
		return nil, err
	}
	out := make([]BoardEvent, 0, len(raw))
	for _, s := range raw {
		var ev BoardEvent
		if json.Unmarshal([]byte(s), &ev) == nil {
			out = append(out, ev)
		}
	}
	return out, nil
}
