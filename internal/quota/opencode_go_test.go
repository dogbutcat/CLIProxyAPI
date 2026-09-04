package quota

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestParseWindowUsageSSR(t *testing.T) {
	tests := []struct {
		name      string
		html      string
		pctFirst  bool
		wantUsage float64
		wantReset float64
	}{
		{
			name:      "usage first",
			html:      `rollingUsage:$R[3]={usagePercent:42.5,resetInSec:3600}`,
			pctFirst:  true,
			wantUsage: 42.5,
			wantReset: 3600,
		},
		{
			name:      "reset first",
			html:      `weeklyUsage:$R[7]={resetInSec:86400,usagePercent:75}`,
			pctFirst:  false,
			wantUsage: 75,
			wantReset: 86400,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got *scrapedWindowUsage
			if tt.pctFirst {
				got = parseWindowUsageSSR(tt.html, reRollingPctFirst, reRollingResetFirst)
			} else {
				got = parseWindowUsageSSR(tt.html, reWeeklyPctFirst, reWeeklyResetFirst)
			}
			if got == nil {
				t.Fatal("expected parsed window")
			}
			if got.usagePercent != tt.wantUsage || got.resetInSec != tt.wantReset {
				t.Fatalf("window = %+v, want usage %.1f reset %.1f", got, tt.wantUsage, tt.wantReset)
			}
		})
	}
}

func TestParseHumanReadableTime(t *testing.T) {
	tests := []struct {
		input string
		want  float64
		ok    bool
	}{
		{"1 hour 56 minutes", 6960, true},
		{"6 days 2 hours", 525600, true},
		{"26 days 17 hours", 2307600, true},
		{"30 seconds", 30, true},
		{"reset now", 0, true},
		{"now", 0, true},
		{"-2 hours", 0, false},
		{"no time here", 0, false},
	}
	for _, tt := range tests {
		got, ok := ParseHumanReadableTime(tt.input)
		if ok != tt.ok {
			t.Fatalf("ParseHumanReadableTime(%q) ok = %v, want %v", tt.input, ok, tt.ok)
		}
		if ok && got != tt.want {
			t.Fatalf("ParseHumanReadableTime(%q) = %f, want %f", tt.input, got, tt.want)
		}
	}
}

func TestParseOpenCodeGoHTML_SSR(t *testing.T) {
	now := time.Unix(1700000000, 0).UTC()
	quota, err := parseOpenCodeGoHTMLAt(validSSRDashboard(), now)
	if err != nil {
		t.Fatalf("ParseOpenCodeGoHTML() error = %v", err)
	}

	assertWindow(t, "rolling", quota.Rolling, 10, 90, 1800, now.Add(1800*time.Second))
	assertWindow(t, "weekly", quota.Weekly, 25, 75, 604800, now.Add(604800*time.Second))
	assertWindow(t, "monthly", quota.Monthly, 50, 50, 2592000, now.Add(2592000*time.Second))
}

func TestParseOpenCodeGoHTML_DataSlotFallback(t *testing.T) {
	quota, err := ParseOpenCodeGoHTML(`<section>
		<div data-slot="usage-item">
			<span data-slot="usage-label">Rolling Usage</span>
			<span data-slot="usage-value">65%</span>
			<span data-slot="reset-time"><!--$-->Resets in 2 hours 30 minutes<!--/--></span>
		</div>
		<div data-slot="usage-item">
			<span data-slot="usage-label">Weekly Usage</span>
			<span data-slot="usage-value">30%</span>
			<span data-slot="reset-now">now</span>
		</div>
		<div data-slot="usage-item">
			<span data-slot="usage-label">Monthly Usage</span>
			<span data-slot="usage-value">99.5%</span>
			<span data-slot="reset-time">Resets in 1 day 1 hour</span>
		</div>
	</section>`)
	if err != nil {
		t.Fatalf("ParseOpenCodeGoHTML() error = %v", err)
	}
	assertWindow(t, "rolling", quota.Rolling, 65, 35, 9000, quota.Rolling.ResetTime)
	assertWindow(t, "weekly", quota.Weekly, 30, 70, 0, quota.Weekly.ResetTime)
	assertWindow(t, "monthly", quota.Monthly, 99.5, 0.5, 90000, quota.Monthly.ResetTime)
}

func TestParseOpenCodeGoHTML_InvalidAndPartialResponses(t *testing.T) {
	tests := []struct {
		name    string
		html    string
		wantErr string
	}{
		{
			name:    "empty",
			html:    `<html><body>no data here</body></html>`,
			wantErr: "no usage windows found",
		},
		{
			name:    "partial",
			html:    `rollingUsage:$R[1]={usagePercent:10,resetInSec:1800}weeklyUsage:$R[2]={usagePercent:25,resetInSec:604800}`,
			wantErr: "partial usage data: missing monthly",
		},
		{
			name:    "out of bounds",
			html:    `rollingUsage:$R[1]={usagePercent:101,resetInSec:1800}weeklyUsage:$R[2]={usagePercent:25,resetInSec:604800}monthlyUsage:$R[3]={usagePercent:50,resetInSec:2592000}`,
			wantErr: "usagePercent",
		},
		{
			name:    "incomplete SSR marker",
			html:    `rollingUsage:$R[1]={usagePercent:10}weeklyUsage:$R[2]={usagePercent:25,resetInSec:604800}monthlyUsage:$R[3]={usagePercent:50,resetInSec:2592000}`,
			wantErr: "rolling SSR window: missing usagePercent or resetInSec",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParseOpenCodeGoHTML(tt.html)
			if err == nil {
				t.Fatal("expected error")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error = %q, want substring %q", err.Error(), tt.wantErr)
			}
		})
	}
}

func TestFetchInputsValidateAndReadsAreBounded(t *testing.T) {
	_, err := FetchOpenCodeGoDashboard(context.Background(), DashboardFetchInput{AuthCookie: "cookie"})
	if err == nil || !strings.Contains(err.Error(), "workspace id") {
		t.Fatalf("FetchOpenCodeGoDashboard() error = %v, want workspace validation", err)
	}

	_, err = FetchOpenCodeGoDashboard(context.Background(), DashboardFetchInput{WorkspaceID: "workspace"})
	if err == nil || !strings.Contains(err.Error(), "auth cookie") {
		t.Fatalf("FetchOpenCodeGoDashboard() error = %v, want auth cookie validation", err)
	}

	body, err := readBounded(strings.NewReader("hello"), 5)
	if err != nil || string(body) != "hello" {
		t.Fatalf("readBounded() = %q, %v; want hello, nil", string(body), err)
	}
	_, err = readBounded(strings.NewReader(strings.Repeat("x", 6)), 5)
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("readBounded() error = %v, want bounded read error", err)
	}
}

func TestDashboardURL(t *testing.T) {
	got := DashboardURL("my-workspace-123")
	want := "https://opencode.ai/workspace/my-workspace-123/go"
	if got != want {
		t.Fatalf("DashboardURL() = %q, want %q", got, want)
	}
}

func assertWindow(t *testing.T, name string, got *OpenCodeGoWindow, usage, remaining, reset float64, resetTime time.Time) {
	t.Helper()
	if got == nil {
		t.Fatalf("%s window is nil", name)
	}
	if got.UsagePercent != usage || got.PercentRemaining != remaining || got.ResetInSec != reset {
		t.Fatalf("%s window = %+v, want usage %.1f remaining %.1f reset %.1f", name, got, usage, remaining, reset)
	}
	if !got.ResetTime.Equal(resetTime) {
		t.Fatalf("%s reset time = %v, want %v", name, got.ResetTime, resetTime)
	}
}

func validSSRDashboard() string {
	return `rollingUsage:$R[1]={usagePercent:10,resetInSec:1800}
weeklyUsage:$R[2]={resetInSec:604800,usagePercent:25}
monthlyUsage:$R[3]={usagePercent:50,resetInSec:2592000}`
}
