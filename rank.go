package main

import (
	"sort"
	"time"
)

// lagPenalty trades speed for freshness: each hour a mirror is behind the
// newest one counts like 3ms of extra latency, so a mirror a day behind
// has to be ~70ms faster to win. Mirrors over 48h behind are marked stale
// and always lose (see markStale).
const lagPenalty = 3 * time.Millisecond

// rank marks stale mirrors and sorts best first: working before broken,
// fresh before stale, then by latency plus lag.
func rank(res []Result) {
	markStale(res)
	var newest time.Time
	for _, r := range res {
		if r.OK && r.Date.After(newest) {
			newest = r.Date
		}
	}
	score := func(r Result) time.Duration {
		s := r.Latency
		if !r.Date.IsZero() {
			s += time.Duration(newest.Sub(r.Date).Hours() * float64(lagPenalty))
		}
		return s
	}
	sort.SliceStable(res, func(i, j int) bool {
		a, b := res[i], res[j]
		if a.OK != b.OK {
			return a.OK
		}
		if a.Stale != b.Stale {
			return !a.Stale
		}
		return score(a) < score(b)
	})
}
