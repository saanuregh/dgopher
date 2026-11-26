package redisserver

import "testing"

func TestPickServers(t *testing.T) {
	for name, want := range map[string]string{"": "redis", "redis": "redis", "valkey": "valkey"} {
		if s, err := pick(name); err != nil || s.Engine != want || s.Valkey() != (want == "valkey") {
			t.Errorf("DGOPHER_REDIS=%q: %+v, %v", name, s, err)
		}
	}
	if _, err := pick("keydb"); err == nil {
		t.Fatal("DGOPHER_REDIS=keydb was accepted")
	}
}
