package assistantwire

// CacheKeepalive is permission to refresh an idle main or registered lead's Claude
// request. Nil means no permission; FollowsSetting also includes background supervision.
type CacheKeepalive struct {
	Enabled         bool `json:"enabled"`
	IntervalMinutes int  `json:"interval_minutes"`
	MaxRequests     int  `json:"max_requests"`
}

func (p CacheKeepalive) Valid() bool {
	return p.IntervalMinutes >= 1 && p.IntervalMinutes <= 55 && p.MaxRequests >= 1 && p.MaxRequests <= 144
}
