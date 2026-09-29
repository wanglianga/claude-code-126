package service

import (
	"testing"
	"time"
)

func mustTime(hour int) time.Time {
	return time.Date(2026, 9, 29, hour, 0, 0, 0, time.UTC)
}

func TestBagCount(t *testing.T) {
	cases := []struct {
		name string
		in   AcceptOrderInput
		want int
	}{
		{"单冷饮", AcceptOrderInput{Items: []AcceptItemInput{{SKU: "P001", Qty: 2}}}, 1},
		{"单热饮", AcceptOrderInput{IceHot: "hot", Items: []AcceptItemInput{{SKU: "P001", IceHot: "hot", Qty: 1}}}, 1},
		{"冷热双袋", AcceptOrderInput{Items: []AcceptItemInput{
			{SKU: "P001", IceHot: "hot", Qty: 1}, {SKU: "P002", IceHot: "ice", Qty: 1}}}, 2},
		{"冷饮+轻食三袋", AcceptOrderInput{Items: []AcceptItemInput{
			{SKU: "P001", IceHot: "ice", Qty: 1}, {SKU: "P005", Qty: 1}}}, 2},
		{"热饮+冷饮+轻食三袋", AcceptOrderInput{Items: []AcceptItemInput{
			{SKU: "P001", IceHot: "ice", Qty: 1},
			{SKU: "P001", IceHot: "hot", Qty: 1},
			{SKU: "P005", Qty: 1}}}, 3},
		{"组合套餐(冷饮+轻食)至少两袋", AcceptOrderInput{Items: []AcceptItemInput{{SKU: "P006", Qty: 1}}}, 2},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := bagCount(c.in); got != c.want {
				t.Errorf("bagCount() = %d, want %d", got, c.want)
			}
		})
	}
}

func TestShiftNameFor(t *testing.T) {
	cases := map[int]string{6: "morning", 10: "morning", 11: "mid", 15: "mid", 16: "evening", 22: "evening", 5: "evening"}
	for hour, want := range cases {
		if got := shiftNameFor(mustTime(hour)); got != want {
			t.Errorf("hour %d => %s, want %s", hour, got, want)
		}
	}
}
