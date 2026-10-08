// Package probe — balance providers.
//
// Every external balance (LLM routers, Selectel) is read over plain HTTPS with
// the standard library only. The secret itself never lands in the registry:
// the registry names an env var, the adapter reads it at runtime.
//
// An empty key is a Warn, not a Fail — a missing key means "not wired up yet",
// not "the provider is down".
package probe

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/AlexanderKuzikov/OpsWatch/internal/registry"
)

// Default endpoints. Overridable per balance through Balance.URL, so a
// provider move does not need a code change.
const (
	defaultOpenRouterURL = "https://openrouter.ai/api/v1/credits"
	defaultRouterAIURL   = "https://routerai.ru/api/v1/credits"
	defaultSelectelURL   = "https://api.selectel.ru/v3/balances"
)

// BalanceCheck runs one balance entry and reports whether a result was
// produced. Manual balances stay silent while healthy and fresh — there is
// nothing to show for a number that is fine.
func BalanceCheck(ctx context.Context, b registry.Balance) (Result, bool) {
	switch b.Via {
	case "openrouter":
		return openrouterBalance(ctx, b), true
	case "routerai":
		return routeraiBalance(ctx, b), true
	case "selectel":
		return selectelBalance(ctx, b), true
	case "manual", "":
		if b.ViaRclone != "" {
			return RcloneAbout(b.ViaRclone), true
		}
		return manualBalance(b)
	default:
		return Fail("balance "+b.ID, "неизвестный via %q", b.Via), true
	}
}

// manualBalance warns when the hand-kept figure is at/below the floor or has
// gone stale. Healthy and fresh stays silent.
func manualBalance(b registry.Balance) (Result, bool) {
	name := "balance " + b.ID
	if b.WarnBelow > 0 && b.Known <= b.WarnBelow {
		return Warn(name, "%s: %.0f %s известно, порог %.0f — обнови вручную (%s)",
			b.Provider, b.Known, b.Currency, b.WarnBelow, orDashUpdate(b.LastUpdate)), true
	}
	if stale, age := isStale(b); stale {
		return Warn(name, "%s: %.0f %s, но цифра обновлялась %s — проверь в кабинете",
			b.Provider, b.Known, b.Currency, age), true
	}
	return Result{}, false
}

// staleAfterDays defaults to 30 when unset.
func isStale(b registry.Balance) (bool, string) {
	if b.LastUpdate == "" {
		return true, "неизвестно когда"
	}
	t, err := time.Parse("2006-01-02", b.LastUpdate)
	if err != nil {
		return true, "дата обновления не читается"
	}
	days := b.StaleAfterDays
	if days <= 0 {
		days = 30
	}
	age := int(time.Since(t).Hours() / 24)
	if age >= days {
		return true, fmt.Sprintf("%d дн. назад", age)
	}
	return false, ""
}

func orDashUpdate(s string) string {
	if strings.TrimSpace(s) == "" {
		return "дата неизвестна"
	}
	return "обновлено " + s
}

// token reads the secret for a balance. Empty means "not wired up yet".
func token(b registry.Balance) (string, bool) {
	if b.AuthEnvVar == "" {
		return "", false
	}
	return os.Getenv(b.AuthEnvVar), os.Getenv(b.AuthEnvVar) != ""
}

// authHeader defaults to Authorization, authScheme to Bearer. An empty scheme
// sends the raw token (Selectel X-Token style).
func authHeader(b registry.Balance) (header, scheme string) {
	header, scheme = b.AuthHeader, b.AuthScheme
	if header == "" {
		header = "Authorization"
	}
	if header == "Authorization" && scheme == "" {
		scheme = "Bearer"
	}
	return header, scheme
}

// getJSON performs one authenticated GET and decodes the body into a map.
// The HTTP status is returned as-is so callers can branch on 401/403.
func getJSON(ctx context.Context, url, header, scheme, tok string) (int, map[string]any, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, nil, err
	}
	if tok != "" {
		if scheme != "" {
			tok = scheme + " " + tok
		}
		req.Header.Set(header, tok)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return resp.StatusCode, nil, err
	}
	// Non-200 bodies are often empty or HTML; callers branch on status.
	if resp.StatusCode != 200 {
		return resp.StatusCode, nil, nil
	}
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		return resp.StatusCode, nil, fmt.Errorf("не JSON: %v", err)
	}
	return resp.StatusCode, m, nil
}

// numAt walks a dotted path (data.total_credits) through a decoded JSON map.
func numAt(m map[string]any, path string) (float64, bool) {
	var cur any = m
	for _, p := range strings.Split(path, ".") {
		obj, ok := cur.(map[string]any)
		if !ok {
			return 0, false
		}
		cur, ok = obj[p]
		if !ok {
			return 0, false
		}
	}
	n, ok := cur.(float64)
	return n, ok
}

func endpoint(b registry.Balance, def string) string {
	if b.URL != "" {
		return b.URL
	}
	return def
}

// belowFloor compares a live figure against the warn threshold.
func belowFloor(b registry.Balance, left float64) (Result, bool) {
	name := "balance " + b.ID
	if b.WarnBelow > 0 && left <= b.WarnBelow {
		return Warn(name, "%s: осталось %.2f %s, порог %.0f — пора пополнить",
			b.Provider, left, b.Currency, b.WarnBelow), true
	}
	return Pass(name, "%s: осталось %.2f %s", b.Provider, left, b.Currency), true
}

// openrouterBalance reads total minus usage. /credits needs a management key;
// a plain key gets 401, in which case we fall back to /auth/key, which works
// with any key and reports usage against an optional limit.
func openrouterBalance(ctx context.Context, b registry.Balance) Result {
	name := "balance " + b.ID
	tok, ok := token(b)
	if !ok {
		return Warn(name, "нет ключа в %s — положи ключ, проверка пропущена", orDashEnv(b.AuthEnvVar))
	}
	header, scheme := authHeader(b)
	url := endpoint(b, defaultOpenRouterURL)
	st, m, err := getJSON(ctx, url, header, scheme, tok)
	if err != nil {
		return Fail(name, "%s: %v", url, err)
	}
	if st == 401 || st == 403 {
		return openrouterViaKey(ctx, b, strings.TrimSuffix(url, "/credits")+"/auth/key", header, scheme, tok)
	}
	if st != 200 {
		return Fail(name, "%s: HTTP %d", url, st)
	}
	total, ok1 := numAt(m, "data.total_credits")
	used, ok2 := numAt(m, "data.total_usage")
	if !ok1 || !ok2 {
		return Fail(name, "неожиданный ответ %s: нет data.total_credits/total_usage", url)
	}
	r, _ := belowFloor(b, total-used)
	return r
}

// openrouterViaKey is the fallback for plain API keys: usage vs limit.
func openrouterViaKey(ctx context.Context, b registry.Balance, url, header, scheme, tok string) Result {
	name := "balance " + b.ID
	st, m, err := getJSON(ctx, url, header, scheme, tok)
	if err != nil {
		return Fail(name, "%s: %v", url, err)
	}
	if st != 200 {
		return Fail(name, "%s: HTTP %d (нужен ключ с доступом к балансу)", url, st)
	}
	usage, uok := numAt(m, "data.usage")
	if !uok {
		return Fail(name, "неожиданный ответ %s: нет data.usage", url)
	}
	limit, lok := numAt(m, "data.limit")
	if !lok {
		return Warn(name, "лимит не задан, потрачено %.2f USD — остаток неизвестен", usage)
	}
	r, _ := belowFloor(b, limit-usage)
	return r
}

// routeraiBalance reads data.credits — a plain key is enough here.
func routeraiBalance(ctx context.Context, b registry.Balance) Result {
	name := "balance " + b.ID
	tok, ok := token(b)
	if !ok {
		return Warn(name, "нет ключа в %s — положи ключ, проверка пропущена", orDashEnv(b.AuthEnvVar))
	}
	header, scheme := authHeader(b)
	url := endpoint(b, defaultRouterAIURL)
	st, m, err := getJSON(ctx, url, header, scheme, tok)
	if err != nil {
		return Fail(name, "%s: %v", url, err)
	}
	if st != 200 {
		return Fail(name, "%s: HTTP %d", url, st)
	}
	left, ok := numAt(m, "data.credits")
	if !ok {
		return Fail(name, "неожиданный ответ %s: нет data.credits", url)
	}
	r, _ := belowFloor(b, left)
	return r
}

// selectelBalance sums final_sum across billings and, when a prediction URL
// is configured, appends how long the money is expected to last.
func selectelBalance(ctx context.Context, b registry.Balance) Result {
	name := "balance " + b.ID
	tok, ok := token(b)
	if !ok {
		return Warn(name, "нет токена в %s — положи токен, проверка пропущена", orDashEnv(b.AuthEnvVar))
	}
	header, scheme := authHeader(b)
	if header == "Authorization" && b.AuthHeader == "" {
		header = "X-Token" // Selectel speaks X-Token by default
		scheme = ""
	}
	url := endpoint(b, defaultSelectelURL)
	st, m, err := getJSON(ctx, url, header, scheme, tok)
	if err != nil {
		return Fail(name, "%s: %v", url, err)
	}
	if st != 200 {
		return Fail(name, "%s: HTTP %d", url, st)
	}
	total, debt, n, ok := selectelTotals(m)
	if !ok {
		return Fail(name, "неожиданный ответ %s: нет data.billings", url)
	}
	extra := ""
	if b.PredictionURL != "" {
		if days, pok := selectelPrediction(ctx, b.PredictionURL, header, scheme, tok); pok {
			extra = fmt.Sprintf(", хватит примерно на %d дн.", days)
		}
	}
	if debt > 0 {
		return Warn(name, "баланс %.0f ₽ по %d биллингам, долг %.0f ₽%s", total, n, debt, extra)
	}
	r, _ := belowFloor(b, total)
	if extra != "" {
		r.Detail += extra
	}
	return r
}

// selectelTotals sums final_sum and debt_sum over data.billings.
func selectelTotals(m map[string]any) (total, debt float64, n int, ok bool) {
	data, ok := m["data"].(map[string]any)
	if !ok {
		return 0, 0, 0, false
	}
	list, ok := data["billings"].([]any)
	if !ok {
		return 0, 0, 0, false
	}
	for _, item := range list {
		bm, ok := item.(map[string]any)
		if !ok {
			continue
		}
		n++
		if v, ok := bm["final_sum"].(float64); ok {
			total += v
		}
		if v, ok := bm["debt_sum"].(float64); ok {
			debt += v
		}
	}
	return total, debt, n, n > 0
}

// selectelPrediction reads data.primary (hours of runway) and converts to days.
func selectelPrediction(ctx context.Context, url, header, scheme, tok string) (int, bool) {
	st, m, err := getJSON(ctx, url, header, scheme, tok)
	if err != nil || st != 200 {
		return 0, false
	}
	hours, ok := numAt(m, "data.primary")
	if !ok || hours < 0 {
		return 0, false
	}
	return int(hours / 24), true
}

func orDashEnv(s string) string {
	if strings.TrimSpace(s) == "" {
		return "env не задан в реестре"
	}
	return s
}
