package usage

import (
	"testing"

	plusstore "github.com/router-for-me/CLIProxyAPI/v7/internal/usage/plusvendor/store"
)

func TestMonitoringCursorRoundTripAndFailedTriState(t *testing.T) {
	cursor := plusstore.AnalyticsEventCursor{TimestampMS: 1785660000000, ID: 42}
	encoded := encodeMonitoringCursor(cursor)
	decoded, err := decodeMonitoringCursor(encoded)
	if err != nil {
		t.Fatalf("decode cursor: %v", err)
	}
	if decoded.TimestampMS != cursor.TimestampMS || decoded.ID != cursor.ID {
		t.Fatalf("decoded cursor = %#v, want %#v", decoded, cursor)
	}
	if _, err := decodeMonitoringCursor("not-a-cursor"); err == nil {
		t.Fatalf("decode invalid cursor succeeded")
	}

	failed, ok := parseMonitoringFailed("failed")
	if !ok || failed == nil || !*failed {
		t.Fatalf("failed parse = %#v ok=%v, want true", failed, ok)
	}
	succeeded, ok := parseMonitoringFailed("success")
	if !ok || succeeded == nil || *succeeded {
		t.Fatalf("success parse = %#v ok=%v, want false", succeeded, ok)
	}
	all, ok := parseMonitoringFailed("all")
	if !ok || all != nil {
		t.Fatalf("all parse = %#v ok=%v, want nil", all, ok)
	}
}
