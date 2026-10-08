package probe

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/AlexanderKuzikov/OpsWatch/internal/registry"
)

func testServer(body string, status int) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		w.Write([]byte(body))
	}))
}

func withKey(t *testing.T, key, val string) {
	t.Helper()
	t.Setenv(key, val)
}

func TestRouterAIBalance(t *testing.T) {
	srv := testServer(`{"data":{"credits":79.02}}`, 200)
	defer srv.Close()
	withKey(t, "TEST_ROUTERAI", "k")
	b := registry.Balance{ID: "routerai", Provider: "RouterAI", Currency: "RUB",
		WarnBelow: 50, Via: "routerai", URL: srv.URL, AuthEnvVar: "TEST_ROUTERAI"}
	r, ok := BalanceCheck(context.Background(), b)
	if !ok || !r.OK || r.Warn {
		t.Fatalf("want pass, got %+v ok=%v", r, ok)
	}
	b.WarnBelow = 100
	r, _ = BalanceCheck(context.Background(), b)
	if !r.Warn {
		t.Fatalf("want warn below floor, got %+v", r)
	}
}

func TestOpenRouterCredits(t *testing.T) {
	srv := testServer(`{"data":{"total_credits":11,"total_usage":0.24}}`, 200)
	defer srv.Close()
	withKey(t, "TEST_OR", "k")
	b := registry.Balance{ID: "openrouter", Provider: "OpenRouter", Currency: "USD",
		WarnBelow: 5, Via: "openrouter", URL: srv.URL, AuthEnvVar: "TEST_OR"}
	r, _ := BalanceCheck(context.Background(), b)
	if !r.OK || r.Warn {
		t.Fatalf("want pass with 10.76 left, got %+v", r)
	}
}

func TestOpenRouterFallbackToKey(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/credits", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
		w.Write([]byte(`{"error":"management key required"}`))
	})
	mux.HandleFunc("/auth/key", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"data":{"usage":3.5,"limit":10}}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	withKey(t, "TEST_OR2", "k")
	b := registry.Balance{ID: "openrouter", Provider: "OpenRouter", Currency: "USD",
		WarnBelow: 5, Via: "openrouter", URL: srv.URL + "/credits", AuthEnvVar: "TEST_OR2"}
	r, _ := BalanceCheck(context.Background(), b)
	if !r.OK || r.Warn {
		t.Fatalf("want pass via fallback with 6.5 left, got %+v", r)
	}
}

func TestOpenRouterNoLimitIsWarn(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/credits", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(403)
	})
	mux.HandleFunc("/auth/key", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"data":{"usage":3.5,"limit":null}}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	withKey(t, "TEST_OR3", "k")
	b := registry.Balance{ID: "openrouter", Provider: "OpenRouter", Currency: "USD",
		Via: "openrouter", URL: srv.URL + "/credits", AuthEnvVar: "TEST_OR3"}
	r, _ := BalanceCheck(context.Background(), b)
	if !r.Warn {
		t.Fatalf("want warn when limit is null, got %+v", r)
	}
}

func TestSelectelBalance(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/v3/balances", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Token") == "" {
			w.WriteHeader(401)
			return
		}
		w.Write([]byte(`{"status":"ok","data":{"billings":[{"billing_type":"cloud","final_sum":1200,"debt_sum":0,"balances":[]}]}}`))
	})
	mux.HandleFunc("/v2/billing/prediction", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"status":"ok","data":{"primary":720}}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	withKey(t, "TEST_SEL", "tok")
	b := registry.Balance{ID: "selectel", Provider: "Selectel", Currency: "RUB",
		WarnBelow: 650, Via: "selectel", URL: srv.URL + "/v3/balances",
		AuthEnvVar: "TEST_SEL", AuthHeader: "X-Token",
		PredictionURL: srv.URL + "/v2/billing/prediction"}
	r, _ := BalanceCheck(context.Background(), b)
	if !r.OK || r.Warn {
		t.Fatalf("want pass with 1200 left, got %+v", r)
	}
}

func TestEmptyKeyIsWarnNotFail(t *testing.T) {
	for _, via := range []string{"openrouter", "routerai", "selectel"} {
		b := registry.Balance{ID: "x", Provider: "P", Currency: "RUB", Via: via, AuthEnvVar: "TEST_DEFINITELY_ABSENT"}
		r, ok := BalanceCheck(context.Background(), b)
		if !ok || !r.OK || !r.Warn {
			t.Fatalf("via %s: want warn, got %+v ok=%v", via, r, ok)
		}
	}
}

func TestManualStale(t *testing.T) {
	old := time.Now().AddDate(0, 0, -45).Format("2006-01-02")
	b := registry.Balance{ID: "vsellm", Provider: "VseLLM", Currency: "RUB",
		Known: 1000, WarnBelow: 100, LastUpdate: old}
	r, ok := BalanceCheck(context.Background(), b)
	if !ok || !r.Warn {
		t.Fatalf("want stale warn, got %+v ok=%v", r, ok)
	}
	fresh := registry.Balance{ID: "vsellm", Provider: "VseLLM", Currency: "RUB",
		Known: 1000, WarnBelow: 100, LastUpdate: time.Now().Format("2006-01-02")}
	if _, ok := BalanceCheck(context.Background(), fresh); ok {
		t.Fatal("fresh healthy manual must stay silent")
	}
}

func TestUnknownVia(t *testing.T) {
	b := registry.Balance{ID: "x", Via: "telepathy"}
	r, ok := BalanceCheck(context.Background(), b)
	if !ok || r.OK {
		t.Fatalf("want fail, got %+v ok=%v", r, ok)
	}
}
