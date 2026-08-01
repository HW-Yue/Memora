package adminapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/HW-Yue/Memora/internal/msql/executor"
	"github.com/HW-Yue/Memora/internal/result"
	"github.com/HW-Yue/Memora/internal/security"
	"github.com/HW-Yue/Memora/internal/testkit"
)

type executeCall struct {
	dataDir    string
	source     string
	statements []executor.StatementInput
}

type executeRecorder struct {
	mu       sync.Mutex
	calls    []executeCall
	envelope result.Envelope
	err      error
}

func (recorder *executeRecorder) execute(
	_ context.Context, dataDir, source string, statements []executor.StatementInput,
) (result.Envelope, error) {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	recorder.calls = append(recorder.calls, executeCall{dataDir, source, statements})
	return recorder.envelope, recorder.err
}

func (recorder *executeRecorder) count() int {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	return len(recorder.calls)
}

func TestGatewayBootstrapsOnceAndReturnsDaemonEnvelopeWithFixedScope(t *testing.T) {
	t.Parallel()

	statement := result.NewStatement(0, "SHOW", "SHOW TABLES FROM work COMPACT")
	statement.Rows = []result.Row{{"name": "notes"}}
	recorder := &executeRecorder{envelope: result.NewEnvelope("request-1", statement)}
	gateway := startGateway(t, recorder, []string{"work"})
	receipt, cookie := bootstrap(t, gateway.Descriptor())

	parsed, err := url.Parse(gateway.Descriptor().URL)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Fragment == "" || parsed.RawQuery != "" {
		t.Fatalf("bootstrap URL = %q, token must only be in fragment", parsed)
	}
	response := callMSQL(t, gateway.Descriptor(), cookie, receipt.CSRFToken,
		`{"source":"SHOW TABLES FROM work COMPACT","statements":[{"parameters":{"named":{}}}]}`)
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("MSQL status = %d, body = %s", response.StatusCode, readBody(t, response))
	}
	gotBody := readBody(t, response)
	wantBody, err := json.Marshal(recorder.envelope)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(gotBody) != string(wantBody) {
		t.Fatalf("API envelope = %s, want daemon envelope = %s", gotBody, wantBody)
	}
	if recorder.count() != 1 {
		t.Fatalf("daemon calls = %d", recorder.count())
	}
	call := recorder.calls[0]
	if call.dataDir != "/fixture" || len(call.statements) != 1 {
		t.Fatalf("call = %#v", call)
	}
	authorization := call.statements[0].Authorization
	if authorization.Version != security.AuthorizationVersion || authorization.Actor != "user:admin" ||
		len(authorization.AuthorizedDatabases) != 1 || authorization.AuthorizedDatabases[0] != "work" {
		t.Fatalf("fixed Authorization = %#v", authorization)
	}

	token := strings.TrimPrefix(parsed.Fragment, "token=")
	replay, err := newRequest(http.MethodPost, gateway.Descriptor().Origin+"/api/v1/session", "{}")
	if err != nil {
		t.Fatal(err)
	}
	replay.Header.Set("Origin", gateway.Descriptor().Origin)
	replay.Header.Set("Authorization", "Bearer "+token)
	replayed, err := http.DefaultClient.Do(replay)
	if err != nil {
		t.Fatal(err)
	}
	defer replayed.Body.Close()
	if replayed.StatusCode != http.StatusUnauthorized {
		t.Fatalf("bootstrap replay status = %d", replayed.StatusCode)
	}
}

func TestGatewayServesEmbeddedShellAndKeepsUnknownAPISeparate(t *testing.T) {
	t.Parallel()

	gateway := startGateway(t, &executeRecorder{}, []string{"work"})
	for _, path := range []string{"/", "/catalog/work"} {
		response, err := http.Get(gateway.Descriptor().Origin + path)
		if err != nil {
			t.Fatal(err)
		}
		body := readBody(t, response)
		response.Body.Close()
		if response.StatusCode != http.StatusOK || !strings.Contains(body, "Memora Admin") ||
			response.Header.Get("Content-Security-Policy") == "" {
			t.Fatalf("GET %s status=%d headers=%#v body=%q", path, response.StatusCode, response.Header, body)
		}
	}
	response, err := http.Get(gateway.Descriptor().Origin + "/api/v1/missing")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusNotFound || strings.Contains(readBody(t, response), "Memora Admin") {
		t.Fatalf("unknown API status=%d", response.StatusCode)
	}
}

func TestGatewayResumesActiveCookieSessionWithoutReusingBootstrapToken(t *testing.T) {
	t.Parallel()

	gateway := startGateway(t, &executeRecorder{}, []string{"work"})
	first, cookie := bootstrap(t, gateway.Descriptor())
	request, err := newRequest(http.MethodPost, gateway.Descriptor().Origin+"/api/v1/session", "{}")
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Origin", gateway.Descriptor().Origin)
	request.AddCookie(cookie)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("resume status=%d body=%s", response.StatusCode, readBody(t, response))
	}
	var resumed SessionReceipt
	if err := json.NewDecoder(response.Body).Decode(&resumed); err != nil {
		t.Fatal(err)
	}
	if resumed.Version != SessionVersion || resumed.CSRFToken == "" || resumed.CSRFToken != first.CSRFToken {
		t.Fatalf("resumed session = %#v, first = %#v", resumed, first)
	}
}

func TestGatewayRejectsExpiredSessionWithoutCallingDaemon(t *testing.T) {
	t.Parallel()

	clock := testkit.NewFakeClock(time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC))
	recorder := &executeRecorder{envelope: result.NewEnvelope("unused")}
	gateway, err := Start(context.Background(), Config{
		DataDir: "/fixture", Scopes: []string{"work"}, Execute: recorder.execute,
		Random: bytes.NewReader(bytes.Repeat([]byte{0x24}, 96)), Now: clock.Now,
		SessionTTL: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = gateway.Close() })
	receipt, cookie := bootstrap(t, gateway.Descriptor())
	clock.Advance(time.Minute)
	response := callMSQL(t, gateway.Descriptor(), cookie, receipt.CSRFToken, `{"source":"SHOW DATABASES"}`)
	defer response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized || recorder.count() != 0 {
		t.Fatalf("expired status=%d calls=%d body=%s", response.StatusCode, recorder.count(), readBody(t, response))
	}
}

func TestGatewayRejectsMutationClientAuthorityAndCrossSiteRequestsBeforeDaemon(t *testing.T) {
	t.Parallel()

	recorder := &executeRecorder{envelope: result.NewEnvelope("unused")}
	gateway := startGateway(t, recorder, []string{"work"})
	receipt, cookie := bootstrap(t, gateway.Descriptor())

	tests := []struct {
		name   string
		body   string
		origin string
		csrf   string
		cookie *http.Cookie
		want   int
	}{
		{"mutation", `{"source":"UPDATE work.notes SET title = 'unsafe'"}`, gateway.Descriptor().Origin, receipt.CSRFToken, cookie, http.StatusBadRequest},
		{"recovered mutation", `{"source":"SELECT * work.notes; DELETE FROM work.notes WHERE row_id = 'row_1'"}`, gateway.Descriptor().Origin, receipt.CSRFToken, cookie, http.StatusBadRequest},
		{"client Authorization", `{"source":"SHOW DATABASES","statements":[{"authorization":{"version":"memora.authorization/v1","actor":"attacker","authorized_databases":["secret"]}}]}`, gateway.Descriptor().Origin, receipt.CSRFToken, cookie, http.StatusBadRequest},
		{"cross origin", `{"source":"SHOW DATABASES"}`, "https://evil.example", receipt.CSRFToken, cookie, http.StatusForbidden},
		{"missing csrf", `{"source":"SHOW DATABASES"}`, gateway.Descriptor().Origin, "", cookie, http.StatusForbidden},
		{"missing cookie", `{"source":"SHOW DATABASES"}`, gateway.Descriptor().Origin, receipt.CSRFToken, nil, http.StatusUnauthorized},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request, err := newRequest(http.MethodPost, gateway.Descriptor().Origin+"/api/v1/msql", test.body)
			if err != nil {
				t.Fatal(err)
			}
			request.Header.Set("Origin", test.origin)
			request.Header.Set("X-Memora-CSRF", test.csrf)
			if test.cookie != nil {
				request.AddCookie(test.cookie)
			}
			response, err := http.DefaultClient.Do(request)
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			if response.StatusCode != test.want {
				t.Fatalf("status = %d, want %d, body = %s", response.StatusCode, test.want, readBody(t, response))
			}
		})
	}
	if recorder.count() != 0 {
		t.Fatalf("rejected requests reached daemon: %d", recorder.count())
	}

	request, _ := newRequest(http.MethodPost, gateway.Descriptor().Origin+"/api/v1/msql", `{"source":"SHOW DATABASES"}`)
	request.Host = "localhost:1"
	request.Header.Set("Origin", gateway.Descriptor().Origin)
	request.Header.Set("X-Memora-CSRF", receipt.CSRFToken)
	request.AddCookie(cookie)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusForbidden || recorder.count() != 0 {
		t.Fatalf("wrong Host status=%d calls=%d", response.StatusCode, recorder.count())
	}
}

func TestGatewayBoundsJSONRedactsUpstreamFailureAndReleasesPort(t *testing.T) {
	t.Parallel()

	recorder := &executeRecorder{err: errors.New("upstream exposed secret-token")}
	ctx, cancel := context.WithCancel(context.Background())
	gateway, err := Start(ctx, Config{
		DataDir: "/fixture", Scopes: []string{"work"}, Execute: recorder.execute,
		Random: bytes.NewReader(bytes.Repeat([]byte{0x5a}, 96)), SessionTTL: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	receipt, cookie := bootstrap(t, gateway.Descriptor())
	response := callMSQL(t, gateway.Descriptor(), cookie, receipt.CSRFToken, `{"source":"SHOW DATABASES"}`)
	body := readBody(t, response)
	response.Body.Close()
	if response.StatusCode != http.StatusBadGateway || strings.Contains(body, "secret-token") {
		t.Fatalf("upstream response status=%d body=%q", response.StatusCode, body)
	}

	oversized := `{"source":"` + strings.Repeat("x", MaxRequestBytes) + `"}`
	response = callMSQL(t, gateway.Descriptor(), cookie, receipt.CSRFToken, oversized)
	response.Body.Close()
	if response.StatusCode != http.StatusRequestEntityTooLarge || recorder.count() != 1 {
		t.Fatalf("oversized status=%d calls=%d", response.StatusCode, recorder.count())
	}

	address := strings.TrimPrefix(gateway.Descriptor().Origin, "http://")
	cancel()
	if err := gateway.Wait(); err != nil {
		t.Fatalf("Wait() = %v", err)
	}
	listener, err := net.Listen("tcp4", address)
	if err != nil {
		t.Fatalf("port was not released: %v", err)
	}
	_ = listener.Close()
}

func TestStartRejectsInvalidScopeAndRandomFailureBeforeListening(t *testing.T) {
	t.Parallel()

	listenCalls := 0
	listen := func(string, string) (net.Listener, error) {
		listenCalls++
		return nil, errors.New("must not listen")
	}
	_, err := Start(context.Background(), Config{
		DataDir: "/fixture", Scopes: []string{"work"}, Execute: (&executeRecorder{}).execute,
		Random: errorReader{}, Listen: listen,
	})
	if err == nil || listenCalls != 0 {
		t.Fatalf("Start() error=%v listen calls=%d", err, listenCalls)
	}
	_, err = Start(context.Background(), Config{
		DataDir: "/fixture", Scopes: []string{"work", "WORK"}, Execute: (&executeRecorder{}).execute,
		Random: bytes.NewReader(bytes.Repeat([]byte{1}, 96)), Listen: listen,
	})
	if err == nil || listenCalls != 0 {
		t.Fatalf("duplicate scope error=%v listen calls=%d", err, listenCalls)
	}
}

type errorReader struct{}

func (errorReader) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }

func startGateway(t *testing.T, recorder *executeRecorder, scopes []string) *Gateway {
	t.Helper()
	gateway, err := Start(context.Background(), Config{
		DataDir: "/fixture", Scopes: scopes, Execute: recorder.execute,
		Random: bytes.NewReader(bytes.Repeat([]byte{0x42}, 96)), SessionTTL: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = gateway.Close() })
	return gateway
}

func bootstrap(t *testing.T, descriptor Descriptor) (SessionReceipt, *http.Cookie) {
	t.Helper()
	parsed, err := url.Parse(descriptor.URL)
	if err != nil {
		t.Fatal(err)
	}
	token := strings.TrimPrefix(parsed.Fragment, "token=")
	request, err := newRequest(http.MethodPost, descriptor.Origin+"/api/v1/session", "{}")
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Origin", descriptor.Origin)
	request.Header.Set("Authorization", "Bearer "+token)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("bootstrap status = %d, body = %s", response.StatusCode, readBody(t, response))
	}
	var receipt SessionReceipt
	if err := json.NewDecoder(response.Body).Decode(&receipt); err != nil {
		t.Fatal(err)
	}
	if receipt.Version != SessionVersion || receipt.CSRFToken == "" {
		t.Fatalf("session receipt = %#v", receipt)
	}
	cookies := response.Cookies()
	if len(cookies) != 1 || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteStrictMode {
		t.Fatalf("session cookies = %#v", cookies)
	}
	return receipt, cookies[0]
}

func callMSQL(t *testing.T, descriptor Descriptor, cookie *http.Cookie, csrf, body string) *http.Response {
	t.Helper()
	request, err := newRequest(http.MethodPost, descriptor.Origin+"/api/v1/msql", body)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Origin", descriptor.Origin)
	request.Header.Set("X-Memora-CSRF", csrf)
	request.AddCookie(cookie)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func newRequest(method, target, body string) (*http.Request, error) {
	request, err := http.NewRequest(method, target, strings.NewReader(body))
	if err == nil {
		request.Header.Set("Content-Type", "application/json")
	}
	return request, err
}

func readBody(t *testing.T, response *http.Response) string {
	t.Helper()
	value, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(value)
}
