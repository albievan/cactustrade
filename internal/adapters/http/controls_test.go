package httpapi

import (
	"strings"
	"testing"
)

// PLAN 6.14: GET /risk/limits carries canEdit and, when false, editBlockedReason, as the use
// case decides.
func TestRiskLimitsCanEditFromTheServer(t *testing.T) {
	x := newAgentTC(t)
	code, m, _ := x.do("GET", "/api/v1/risk/limits", "")
	if code != 200 || m["canEdit"] != true || m["editBlockedReason"] != nil || m["maxShortBp"] == nil {
		t.Fatalf("limits %d %v", code, m)
	}
	if code, m, _ := x.do("POST", "/api/v1/agent/kill", `{"reason":"test"}`, x.csrfHdr()...); code != 200 {
		t.Fatalf("kill %d %v", code, m)
	}
	code, m, _ = x.do("GET", "/api/v1/risk/limits", "")
	if reason, _ := m["editBlockedReason"].(string); code != 200 || m["canEdit"] != false || !strings.Contains(reason, "kill switch is on") {
		t.Fatalf("limits while killed %d %v", code, m)
	}
}

// PLAN 6.13: without the agent loop, the panel says why start is off and a start is 503.
func TestStartRefusedWithoutTheAgentLoop(t *testing.T) {
	x := newAgentTC(t)
	x.svc.SetAgentLoop(false)
	code, m, _ := x.do("GET", "/api/v1/agent", "")
	if code != 200 {
		t.Fatalf("agent %d %v", code, m)
	}
	ctl := m["controls"].(map[string]any)
	if reason, _ := ctl["startBlockedReason"].(string); ctl["start"] != false || ctl["kill"] != true || !strings.Contains(reason, "restart cactusTrade") {
		t.Fatalf("controls %v", ctl)
	}
	code, m, _ = x.do("POST", "/api/v1/agent/start", "", x.csrfHdr()...)
	if detail, _ := m["detail"].(string); code != 503 || !strings.Contains(detail, "the agent loop is not running") {
		t.Fatalf("start %d %v", code, m)
	}
	x.svc.SetAgentLoop(true)
	if _, m, _ := x.do("GET", "/api/v1/agent", ""); m["controls"].(map[string]any)["startBlockedReason"] != nil {
		t.Fatalf("still blocked: %v", m["controls"])
	}
}
