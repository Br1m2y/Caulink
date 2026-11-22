package v1

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func validEntry() IPPoolEntry {
	return IPPoolEntry{
		ID:          "eth0@10.3.21.7",
		Interface:   "eth0",
		SourceIP:    "10.3.21.7",
		AuthStatus:  AuthOnline,
		HealthScore: 0.92,
		RTTMs:       13.4,
		LossRate:    0.001,
		LastSeen:    time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC),
	}
}

func TestValidateAcceptsValidEntry(t *testing.T) {
	if err := validEntry().Validate(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateRejectsBadEntries(t *testing.T) {
	cases := map[string]func(*IPPoolEntry){
		"empty id":        func(e *IPPoolEntry) { e.ID = "" },
		"empty interface": func(e *IPPoolEntry) { e.Interface = "" },
		"bad ip":          func(e *IPPoolEntry) { e.SourceIP = "10.3.21" },
		"unspecified ip":  func(e *IPPoolEntry) { e.SourceIP = "0.0.0.0" },
		"bad status":      func(e *IPPoolEntry) { e.AuthStatus = "onlineish" },
		"health too high": func(e *IPPoolEntry) { e.HealthScore = 1.5 },
		"health negative": func(e *IPPoolEntry) { e.HealthScore = -0.1 },
		"negative rtt":    func(e *IPPoolEntry) { e.RTTMs = -1 },
		"loss too high":   func(e *IPPoolEntry) { e.LossRate = 3 },
		"zero last seen":  func(e *IPPoolEntry) { e.LastSeen = time.Time{} },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			e := validEntry()
			mutate(&e)
			if err := e.Validate(); err == nil {
				t.Fatalf("expected error for %s, got nil", name)
			}
		})
	}
}

func TestJSONRoundTripKeepsSnakeCaseKeys(t *testing.T) {
	raw, err := json.Marshal(validEntry())
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, key := range []string{
		`"id"`, `"interface"`, `"source_ip"`, `"auth_status"`,
		`"health_score"`, `"rtt_ms"`, `"loss_rate"`, `"last_seen"`,
	} {
		if !strings.Contains(string(raw), key) {
			t.Errorf("missing json key %s in %s", key, raw)
		}
	}
	var back IPPoolEntry
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if err := back.Validate(); err != nil {
		t.Fatalf("round-tripped entry invalid: %v", err)
	}
}
