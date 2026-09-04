// Package quota implements standalone quota parsing and polling helpers.
package quota

import (
	"context"
	"crypto/tls"
	"fmt"
	htmlpkg "html"
	"io"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	log "github.com/sirupsen/logrus"
)

// OpenCodeGoWindow holds normalized usage data for a single quota window.
type OpenCodeGoWindow struct {
	UsagePercent     float64   `json:"usagePercent"`
	PercentRemaining float64   `json:"percentRemaining"`
	ResetInSec       float64   `json:"resetInSec"`
	ResetTime        time.Time `json:"resetTime"`
}

// OpenCodeGoQuota holds the parsed quota data from the OpenCode Go dashboard.
type OpenCodeGoQuota struct {
	Rolling *OpenCodeGoWindow `json:"rolling,omitempty"`
	Weekly  *OpenCodeGoWindow `json:"weekly,omitempty"`
	Monthly *OpenCodeGoWindow `json:"monthly,omitempty"`
}

// DashboardFetchInput contains the request inputs needed to fetch a dashboard.
type DashboardFetchInput struct {
	WorkspaceID  string
	AuthCookie   string
	ProxyURL     string
	DashboardURL string
}

type scrapedWindowUsage struct {
	usagePercent float64
	resetInSec   float64
}

const (
	dashboardURLPrefix   = "https://opencode.ai/workspace/"
	dashboardURLSuffix   = "/go"
	scrapeUserAgent      = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) Gecko/20100101 Firefox/148.0"
	maxDashboardBodySize = int64(2 << 20)
	maxErrorBodySize     = int64(4 << 10)
	maxResetInSec        = float64((1<<63 - 1) / 1e9)
)

var scrapedNumberPattern = `([-+]?(?:\d+(?:\.\d*)?|\.\d+)(?:[eE][-+]?\d+)?)`

var (
	reRollingPctFirst   = regexp.MustCompile(`(?s)\brollingUsage\s*:\s*\$R\[\d+\]\s*=\s*\{[^}]*\busagePercent\s*:\s*` + scrapedNumberPattern + `[^}]*\bresetInSec\s*:\s*` + scrapedNumberPattern + `[^}]*\}`)
	reRollingResetFirst = regexp.MustCompile(`(?s)\brollingUsage\s*:\s*\$R\[\d+\]\s*=\s*\{[^}]*\bresetInSec\s*:\s*` + scrapedNumberPattern + `[^}]*\busagePercent\s*:\s*` + scrapedNumberPattern + `[^}]*\}`)

	reWeeklyPctFirst   = regexp.MustCompile(`(?s)\bweeklyUsage\s*:\s*\$R\[\d+\]\s*=\s*\{[^}]*\busagePercent\s*:\s*` + scrapedNumberPattern + `[^}]*\bresetInSec\s*:\s*` + scrapedNumberPattern + `[^}]*\}`)
	reWeeklyResetFirst = regexp.MustCompile(`(?s)\bweeklyUsage\s*:\s*\$R\[\d+\]\s*=\s*\{[^}]*\bresetInSec\s*:\s*` + scrapedNumberPattern + `[^}]*\busagePercent\s*:\s*` + scrapedNumberPattern + `[^}]*\}`)

	reMonthlyPctFirst   = regexp.MustCompile(`(?s)\bmonthlyUsage\s*:\s*\$R\[\d+\]\s*=\s*\{[^}]*\busagePercent\s*:\s*` + scrapedNumberPattern + `[^}]*\bresetInSec\s*:\s*` + scrapedNumberPattern + `[^}]*\}`)
	reMonthlyResetFirst = regexp.MustCompile(`(?s)\bmonthlyUsage\s*:\s*\$R\[\d+\]\s*=\s*\{[^}]*\bresetInSec\s*:\s*` + scrapedNumberPattern + `[^}]*\busagePercent\s*:\s*` + scrapedNumberPattern + `[^}]*\}`)

	reRollingMarker = regexp.MustCompile(`(?s)\brollingUsage\s*:\s*\$R\[\d+\]\s*=\s*\{[^}]*\}`)
	reWeeklyMarker  = regexp.MustCompile(`(?s)\bweeklyUsage\s*:\s*\$R\[\d+\]\s*=\s*\{[^}]*\}`)
	reMonthlyMarker = regexp.MustCompile(`(?s)\bmonthlyUsage\s*:\s*\$R\[\d+\]\s*=\s*\{[^}]*\}`)

	reDataSlotLabel = regexp.MustCompile(`(?is)data-slot=["']usage-label["'][^>]*>(.*?)<`)
	reDataSlotValue = regexp.MustCompile(`(?is)data-slot=["']usage-value["'][^>]*>.*?` + scrapedNumberPattern)
	reDataSlotReset = regexp.MustCompile(`(?is)data-slot=["'](reset-time|reset-now)["'][^>]*>(.*?)</span>`)

	reHumanDay    = regexp.MustCompile(`(?i)([-+]?(?:\d+(?:\.\d*)?|\.\d+))\s*days?`)
	reHumanHour   = regexp.MustCompile(`(?i)([-+]?(?:\d+(?:\.\d*)?|\.\d+))\s*hours?`)
	reHumanMinute = regexp.MustCompile(`(?i)([-+]?(?:\d+(?:\.\d*)?|\.\d+))\s*minutes?`)
	reHumanSecond = regexp.MustCompile(`(?i)([-+]?(?:\d+(?:\.\d*)?|\.\d+))\s*seconds?`)

	reHTMLTag      = regexp.MustCompile(`(?s)<[^>]+>`)
	reSolidComment = regexp.MustCompile(`<!--\$-->|<!--/-->`)
	reResetsIn     = regexp.MustCompile(`(?i)\bResets?\s*in\s*`)
	reWhitespace   = regexp.MustCompile(`\s+`)
)

func parseWindowUsageSSR(html string, rePctFirst, reResetFirst *regexp.Regexp) *scrapedWindowUsage {
	if m := rePctFirst.FindStringSubmatch(html); m != nil {
		w, err := parseScrapedWindow("SSR usage window", m[1], m[2])
		if err == nil {
			return w
		}
	}
	if m := reResetFirst.FindStringSubmatch(html); m != nil {
		w, err := parseScrapedWindow("SSR usage window", m[2], m[1])
		if err == nil {
			return w
		}
	}
	return nil
}

func parseWindowUsageSSRDetailed(html, name string, marker, rePctFirst, reResetFirst *regexp.Regexp) (*scrapedWindowUsage, bool, error) {
	if m := rePctFirst.FindStringSubmatch(html); m != nil {
		w, err := parseScrapedWindow(name+" SSR window", m[1], m[2])
		return w, true, err
	}
	if m := reResetFirst.FindStringSubmatch(html); m != nil {
		w, err := parseScrapedWindow(name+" SSR window", m[2], m[1])
		return w, true, err
	}
	if marker.MatchString(html) {
		return nil, true, fmt.Errorf("%s SSR window: missing usagePercent or resetInSec", name)
	}
	return nil, false, nil
}

func parseScrapedWindow(contextName, usageRaw, resetRaw string) (*scrapedWindowUsage, error) {
	usagePercent, err := parseFiniteNumber(contextName+".usagePercent", usageRaw)
	if err != nil {
		return nil, err
	}
	resetInSec, err := parseFiniteNumber(contextName+".resetInSec", resetRaw)
	if err != nil {
		return nil, err
	}
	if usagePercent < 0 || usagePercent > 100 {
		return nil, fmt.Errorf("%s.usagePercent: must be between 0 and 100, got %s", contextName, usageRaw)
	}
	if resetInSec < 0 || resetInSec > maxResetInSec {
		return nil, fmt.Errorf("%s.resetInSec: must be between 0 and %.0f, got %s", contextName, maxResetInSec, resetRaw)
	}
	return &scrapedWindowUsage{usagePercent: usagePercent, resetInSec: resetInSec}, nil
}

func parseFiniteNumber(contextName, raw string) (float64, error) {
	v, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
	if err != nil {
		return 0, fmt.Errorf("%s: parse finite number: %w", contextName, err)
	}
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 0, fmt.Errorf("%s: must be finite", contextName)
	}
	return v, nil
}

// ParseHumanReadableTime parses dashboard strings such as "1 hour 56 minutes".
func ParseHumanReadableTime(timeStr string) (float64, bool) {
	normalized := strings.ToLower(strings.TrimSpace(timeStr))
	normalized = reWhitespace.ReplaceAllString(normalized, " ")

	switch normalized {
	case "reset-now", "reset now", "now", "resets now":
		return 0, true
	}

	var total float64
	hasDuration := false

	for _, part := range []struct {
		re         *regexp.Regexp
		multiplier float64
	}{
		{reHumanDay, 86400},
		{reHumanHour, 3600},
		{reHumanMinute, 60},
		{reHumanSecond, 1},
	} {
		m := part.re.FindStringSubmatch(normalized)
		if m == nil {
			continue
		}
		v, err := parseFiniteNumber("human duration", m[1])
		if err != nil || v < 0 {
			return 0, false
		}
		total += v * part.multiplier
		hasDuration = true
	}
	if !hasDuration || total > maxResetInSec || math.IsInf(total, 0) || math.IsNaN(total) {
		return 0, false
	}
	return total, true
}

// ParseDataSlotFormat parses the dashboard data-slot HTML fallback format.
func ParseDataSlotFormat(html string) map[string]*scrapedWindowUsage {
	result, _ := parseDataSlotFormatDetailed(html)
	return result
}

func parseDataSlotFormatDetailed(html string) (map[string]*scrapedWindowUsage, []error) {
	result := make(map[string]*scrapedWindowUsage)
	var errs []error
	items := strings.Split(html, `data-slot="usage-item"`)
	if len(items) == 1 {
		items = strings.Split(html, `data-slot='usage-item'`)
	}

	for i := 1; i < len(items); i++ {
		content := items[i]
		labelMatch := reDataSlotLabel.FindStringSubmatch(content)
		if labelMatch == nil {
			continue
		}

		label := cleanDashboardText(labelMatch[1])
		windowKey := windowKeyFromLabel(label)
		if windowKey == "" {
			continue
		}

		usageMatch := reDataSlotValue.FindStringSubmatch(content)
		if usageMatch == nil {
			errs = append(errs, fmt.Errorf("%s data-slot window: missing usage value", windowKey))
			continue
		}

		resetMatch := reDataSlotReset.FindStringSubmatch(content)
		if resetMatch == nil {
			errs = append(errs, fmt.Errorf("%s data-slot window: missing reset time", windowKey))
			continue
		}

		resetInSec := float64(0)
		if resetMatch[1] != "reset-now" {
			sec, ok := ParseHumanReadableTime(cleanDashboardText(resetMatch[2]))
			if !ok {
				errs = append(errs, fmt.Errorf("%s data-slot window: invalid reset time", windowKey))
				continue
			}
			resetInSec = sec
		}

		w, err := parseScrapedWindow(windowKey+" data-slot window", usageMatch[1], strconv.FormatFloat(resetInSec, 'f', -1, 64))
		if err != nil {
			errs = append(errs, err)
			continue
		}
		result[windowKey] = w
	}

	return result, errs
}

func cleanDashboardText(s string) string {
	s = reSolidComment.ReplaceAllString(s, "")
	s = reHTMLTag.ReplaceAllString(s, " ")
	s = htmlpkg.UnescapeString(s)
	s = reResetsIn.ReplaceAllString(s, "")
	s = reWhitespace.ReplaceAllString(strings.TrimSpace(s), " ")
	return s
}

func windowKeyFromLabel(label string) string {
	label = strings.ToLower(strings.TrimSpace(label))
	switch {
	case strings.Contains(label, "rolling"):
		return "rolling"
	case strings.Contains(label, "weekly"):
		return "weekly"
	case strings.Contains(label, "monthly"):
		return "monthly"
	default:
		return ""
	}
}

// ParseOpenCodeGoHTML parses OpenCode Go dashboard HTML into normalized quota data.
func ParseOpenCodeGoHTML(html string) (*OpenCodeGoQuota, error) {
	return parseOpenCodeGoHTMLAt(html, time.Now())
}

func parseOpenCodeGoHTMLAt(html string, now time.Time) (*OpenCodeGoQuota, error) {
	windows := make(map[string]*scrapedWindowUsage, 3)
	var errs []error

	ssrWindows := []struct {
		name       string
		marker     *regexp.Regexp
		pctFirst   *regexp.Regexp
		resetFirst *regexp.Regexp
	}{
		{"rolling", reRollingMarker, reRollingPctFirst, reRollingResetFirst},
		{"weekly", reWeeklyMarker, reWeeklyPctFirst, reWeeklyResetFirst},
		{"monthly", reMonthlyMarker, reMonthlyPctFirst, reMonthlyResetFirst},
	}
	for _, spec := range ssrWindows {
		w, found, err := parseWindowUsageSSRDetailed(html, spec.name, spec.marker, spec.pctFirst, spec.resetFirst)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if found && w != nil {
			windows[spec.name] = w
		}
	}

	dataSlot, dataSlotErrs := parseDataSlotFormatDetailed(html)
	for _, name := range []string{"rolling", "weekly", "monthly"} {
		if windows[name] == nil && dataSlot[name] != nil {
			windows[name] = dataSlot[name]
		}
	}
	for _, err := range dataSlotErrs {
		windowName := strings.SplitN(err.Error(), " ", 2)[0]
		if windows[windowName] == nil {
			errs = append(errs, err)
		}
	}

	if len(windows) == 0 {
		if len(errs) > 0 {
			return nil, fmt.Errorf("parse OpenCode Go dashboard: %s", joinErrors(errs))
		}
		return nil, fmt.Errorf("parse OpenCode Go dashboard: no usage windows found")
	}

	missing := missingWindowNames(windows)
	if len(missing) > 0 {
		message := fmt.Sprintf("partial usage data: missing %s", strings.Join(missing, ", "))
		if len(errs) > 0 {
			message += "; " + joinErrors(errs)
		}
		return nil, fmt.Errorf("parse OpenCode Go dashboard: %s", message)
	}
	if len(errs) > 0 {
		return nil, fmt.Errorf("parse OpenCode Go dashboard: %s", joinErrors(errs))
	}

	return &OpenCodeGoQuota{
		Rolling: normalizeWindow(windows["rolling"], now),
		Weekly:  normalizeWindow(windows["weekly"], now),
		Monthly: normalizeWindow(windows["monthly"], now),
	}, nil
}

func missingWindowNames(windows map[string]*scrapedWindowUsage) []string {
	var missing []string
	for _, name := range []string{"rolling", "weekly", "monthly"} {
		if windows[name] == nil {
			missing = append(missing, name)
		}
	}
	return missing
}

func joinErrors(errs []error) string {
	parts := make([]string, 0, len(errs))
	for _, err := range errs {
		if err != nil {
			parts = append(parts, err.Error())
		}
	}
	sort.Strings(parts)
	return strings.Join(parts, "; ")
}

func normalizeWindow(w *scrapedWindowUsage, now time.Time) *OpenCodeGoWindow {
	return &OpenCodeGoWindow{
		UsagePercent:     w.usagePercent,
		PercentRemaining: 100 - w.usagePercent,
		ResetInSec:       w.resetInSec,
		ResetTime:        now.Add(time.Duration(w.resetInSec * float64(time.Second))),
	}
}

// DashboardURL returns the URL for an OpenCode Go workspace dashboard.
func DashboardURL(workspaceID string) string {
	return dashboardURLPrefix + url.PathEscape(strings.TrimSpace(workspaceID)) + dashboardURLSuffix
}

// FetchOpenCodeGoDashboard fetches dashboard HTML using caller-provided context.
func FetchOpenCodeGoDashboard(ctx context.Context, input DashboardFetchInput) (string, error) {
	workspaceID := strings.TrimSpace(input.WorkspaceID)
	if workspaceID == "" && strings.TrimSpace(input.DashboardURL) == "" {
		return "", fmt.Errorf("workspace id is required")
	}
	if strings.TrimSpace(input.AuthCookie) == "" {
		return "", fmt.Errorf("auth cookie is required")
	}

	dashboardURL := strings.TrimSpace(input.DashboardURL)
	if dashboardURL == "" {
		dashboardURL = DashboardURL(workspaceID)
	}
	return fetchDashboard(ctx, dashboardURL, input.AuthCookie, input.ProxyURL)
}

func fetchDashboard(ctx context.Context, dashboardURL, authCookie, proxyURL string) (string, error) {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}

	proxyURL = strings.TrimSpace(proxyURL)
	if proxyURL != "" {
		parsedProxy, err := url.Parse(proxyURL)
		if err != nil || parsedProxy.Scheme == "" || parsedProxy.Host == "" {
			return "", fmt.Errorf("parse proxy URL: invalid proxy URL")
		}
		transport.Proxy = http.ProxyURL(parsedProxy)
	}

	client := &http.Client{Transport: transport}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, dashboardURL, nil)
	if err != nil {
		return "", fmt.Errorf("create dashboard request: %w", err)
	}
	req.Header.Set("User-Agent", scrapeUserAgent)
	req.Header.Set("Accept", "text/html")
	req.Header.Set("Cookie", authCookie)

	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("fetch dashboard: %w", err)
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.WithError(errClose).Warn("opencode-go quota: failed to close dashboard response body")
		}
	}()

	if resp.StatusCode != http.StatusOK {
		body, errRead := readBounded(resp.Body, maxErrorBodySize)
		if errRead != nil {
			return "", fmt.Errorf("dashboard returned HTTP %d; read error body: %w", resp.StatusCode, errRead)
		}
		text := strings.TrimSpace(string(body))
		if len(text) > 160 {
			text = text[:160]
		}
		return "", fmt.Errorf("dashboard returned HTTP %d: %s", resp.StatusCode, text)
	}

	body, err := readBounded(resp.Body, maxDashboardBodySize)
	if err != nil {
		return "", fmt.Errorf("read dashboard body: %w", err)
	}
	return string(body), nil
}

func readBounded(r io.Reader, maxBytes int64) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(r, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > maxBytes {
		return nil, fmt.Errorf("response body exceeds %d bytes", maxBytes)
	}
	return body, nil
}
