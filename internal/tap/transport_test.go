package tap

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	http "github.com/bogdanfinn/fhttp"
	"github.com/bogdanfinn/fhttp/cookiejar"
	"golang.org/x/time/rate"

	"airtravel/internal/client"
	"airtravel/internal/collect"
	"airtravel/internal/config"
)

// ---------------------------------------------------------------------------
// Dublê do cliente HTTP
// ---------------------------------------------------------------------------

// stubDoer devolve respostas pré-programadas e registra as requisições.
type stubDoer struct {
	responses []*http.Response
	errs      []error
	calls     atomic.Int32
	requests  []*http.Request
}

func (s *stubDoer) Do(req *http.Request) (*http.Response, error) {
	i := int(s.calls.Add(1)) - 1
	s.requests = append(s.requests, req)

	if i < len(s.errs) && s.errs[i] != nil {
		return nil, s.errs[i]
	}
	if i < len(s.responses) {
		return s.responses[i], nil
	}
	return respond(http.StatusOK, `{"status":"200"}`), nil
}

// respond monta uma resposta com corpo legível.
func respond(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

// newTestScraper monta o adapter com o dublê, sem tocar a rede.
//
// O rate limiter é aberto de propósito: os testes não devem esperar.
func newTestScraper(t *testing.T, stub *stubDoer) *Scraper {
	t.Helper()

	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("failed to build jar: %v", err)
	}

	cfg := config.Default()
	cfg.MaxRetries = 3
	// Backoff curto: o que se verifica é a política, não a espera.
	cfg.RetryBackoff = time.Millisecond

	engine, err := client.EngineFor(cfg.TLSProfile)
	if err != nil {
		t.Fatalf("failed to resolve engine: %v", err)
	}

	s, err := New(cfg, nil, jar, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("failed to build scraper: %v", err)
	}
	s.fps = fixed{fp: &client.Fingerprint{
		Profile: cfg.TLSProfile, Engine: engine, Client: stub,
	}}
	s.limiter = rate.NewLimiter(rate.Inf, 1)
	return s
}

// ---------------------------------------------------------------------------
// Classificação de resposta
// ---------------------------------------------------------------------------

// TestClassifyResponseMapsToUseCaseErrors cobre a tradução de status HTTP em
// erros do caso de uso.
func TestClassifyResponseMapsToUseCaseErrors(t *testing.T) {
	blockPage, err := os.ReadFile(accessDeniedFixture)
	if err != nil {
		t.Fatalf("failed to read fixture: %v", err)
	}

	tests := []struct {
		name   string
		status int
		body   string
		want   error
	}{
		{"200 passa", http.StatusOK, `{"status":"200"}`, nil},
		{"401", http.StatusUnauthorized, ``, collect.ErrUnauthorized},
		{"429", http.StatusTooManyRequests, ``, collect.ErrRateLimited},
		{"403 do WAF", http.StatusForbidden, string(blockPage), collect.ErrBlocked},
		{"desafio da Cloudflare", http.StatusForbidden, `<html>Just a moment...</html>`, collect.ErrBlocked},
	}

	for _, tc := range tests {
		got := classifyResponse(respond(tc.status, ""), "/rota", []byte(tc.body))

		if tc.want == nil {
			if got != nil {
				t.Errorf("%s: err = %v, esperado nil", tc.name, got)
			}
			continue
		}
		if !errors.Is(got, tc.want) {
			t.Errorf("%s: err = %v, esperado %v", tc.name, got, tc.want)
		}
	}
}

// TestClassifyUnexpectedStatusIncludesBody: um status desconhecido precisa
// carregar o corpo, senão o diagnóstico fica impossível.
func TestClassifyUnexpectedStatusIncludesBody(t *testing.T) {
	err := classifyResponse(respond(http.StatusTeapot, ""), "/rota", []byte("detalhe do erro"))

	if err == nil {
		t.Fatal("status inesperado foi aceito")
	}
	if !strings.Contains(err.Error(), "detalhe do erro") {
		t.Errorf("erro não inclui o corpo: %v", err)
	}
	if !strings.Contains(err.Error(), "/rota") {
		t.Errorf("erro não inclui a rota: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Política de retry
// ---------------------------------------------------------------------------

// TestBlockIsTerminal fixa a decisão: repetir um 403 do WAF não resolve — só
// trocar de perfil resolve. Insistir gasta requisição e piora o bot score.
func TestBlockIsTerminal(t *testing.T) {
	blockPage, _ := os.ReadFile(accessDeniedFixture)
	stub := &stubDoer{responses: []*http.Response{
		respond(http.StatusForbidden, string(blockPage)),
		respond(http.StatusOK, `{"status":"200"}`), // não deve ser alcançada
	}}
	s := newTestScraper(t, stub)

	_, err := s.doWithRetry(context.Background(), http.MethodGet, "/rota", nil, nil, "tok")

	if !errors.Is(err, collect.ErrBlocked) {
		t.Errorf("err = %v, esperado ErrBlocked", err)
	}
	if n := stub.calls.Load(); n != 1 {
		t.Errorf("%d tentativas; bloqueio deve ser terminal", n)
	}
}

// TestUnauthorizedIsTerminalInsideRetry: o 401 é tratado um nível acima, em
// doAuthed, que renova o token. Repetir com o mesmo token seria inútil.
func TestUnauthorizedIsTerminalInsideRetry(t *testing.T) {
	stub := &stubDoer{responses: []*http.Response{
		respond(http.StatusUnauthorized, ``),
		respond(http.StatusOK, `{"status":"200"}`),
	}}
	s := newTestScraper(t, stub)

	_, err := s.doWithRetry(context.Background(), http.MethodGet, "/rota", nil, nil, "tok")

	if !errors.Is(err, collect.ErrUnauthorized) {
		t.Errorf("err = %v, esperado ErrUnauthorized", err)
	}
	if n := stub.calls.Load(); n != 1 {
		t.Errorf("%d tentativas; o 401 é resolvido em doAuthed, não aqui", n)
	}
}

// TestTransientErrorRetries: erro de rede é transitório e merece nova tentativa.
func TestTransientErrorRetries(t *testing.T) {
	stub := &stubDoer{
		errs:      []error{errors.New("connection reset"), nil},
		responses: []*http.Response{nil, respond(http.StatusOK, `{"status":"200"}`)},
	}
	s := newTestScraper(t, stub)

	// O backoff da primeira repetição é de 2 s; o contexto precisa acomodá-lo.
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()

	payload, err := s.doWithRetry(ctx, http.MethodGet, "/rota", nil, nil, "tok")
	if err != nil {
		t.Fatalf("err = %v, esperado sucesso na segunda tentativa", err)
	}
	if !strings.Contains(string(payload), `"200"`) {
		t.Errorf("corpo = %q", payload)
	}
	if n := stub.calls.Load(); n != 2 {
		t.Errorf("%d tentativas, esperado 2", n)
	}
}

// TestRetryStopsOnCanceledContext: cancelamento não deve virar espera inútil.
func TestRetryStopsOnCanceledContext(t *testing.T) {
	stub := &stubDoer{errs: []error{errors.New("falha")}}
	s := newTestScraper(t, stub)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := s.doWithRetry(ctx, http.MethodGet, "/rota", nil, nil, "tok"); err == nil {
		t.Error("contexto cancelado foi ignorado")
	}
}

// ---------------------------------------------------------------------------
// Cabeçalhos
// ---------------------------------------------------------------------------

// TestHeadersFollowEngine confirma que a identidade enviada é a do motor do
// perfil, e que a ordem anunciada só contém cabeçalhos presentes.
func TestHeadersFollowEngine(t *testing.T) {
	stub := &stubDoer{}
	s := newTestScraper(t, stub)

	if _, err := s.do(context.Background(), http.MethodPost, pathAvailability, nil, []byte(`{}`), "tok"); err != nil {
		t.Fatalf("erro inesperado: %v", err)
	}
	if len(stub.requests) != 1 {
		t.Fatalf("%d requisições registradas", len(stub.requests))
	}
	req := stub.requests[0]

	// O padrão é Gecko: sem client hints, com te: trailers.
	if got := req.Header.Get("user-agent"); !strings.Contains(got, "Firefox/148") {
		t.Errorf("user-agent = %q, esperado Firefox", got)
	}
	if got := req.Header.Get("sec-ch-ua"); got != "" {
		t.Errorf("Gecko não deve anunciar sec-ch-ua, obtido %q", got)
	}
	if got := req.Header.Get("te"); got != "trailers" {
		t.Errorf("te = %q, esperado trailers", got)
	}
	if got := req.Header.Get("authorization"); got != "Bearer tok" {
		t.Errorf("authorization = %q", got)
	}

	// A ordem anunciada não pode citar cabeçalho ausente.
	for _, name := range req.Header[http.HeaderOrderKey] {
		switch name {
		case "content-length", "cookie": // definidos pelo transporte
		default:
			if req.Header.Get(name) == "" {
				t.Errorf("ordem anuncia %q, que não foi enviado", name)
			}
		}
	}
	if want := []string{":method", ":authority", ":scheme", ":path"}; strings.Join(req.Header[http.PHeaderOrderKey], ",") != strings.Join(want, ",") {
		t.Errorf("pseudo-headers = %v, esperado %v", req.Header[http.PHeaderOrderKey], want)
	}
}

// TestDynatraceHeadersOnlyWhereBrowserSendsThem fixa a divergência por endpoint:
// a SPA não envia x-dtreferer nem x-dtpc em session/create.
func TestDynatraceHeadersOnlyWhereBrowserSendsThem(t *testing.T) {
	tests := []struct {
		path        string
		wantDT      bool
		wantReferer string
	}{
		{pathSessionCreate, false, "/booking"},
		{pathAvailability, true, "/booking/flights"},
		{pathCalendar, true, "/booking"},
	}

	for _, tc := range tests {
		stub := &stubDoer{}
		s := newTestScraper(t, stub)

		if _, err := s.do(context.Background(), http.MethodPost, tc.path, nil, []byte(`{}`), "tok"); err != nil {
			t.Fatalf("%s: erro inesperado: %v", tc.path, err)
		}
		req := stub.requests[0]

		hasDT := req.Header.Get("x-dtpc") != ""
		if hasDT != tc.wantDT {
			t.Errorf("%s: x-dtpc presente = %v, esperado %v", tc.path, hasDT, tc.wantDT)
		}
		if got := req.Header.Get("referer"); !strings.HasSuffix(got, tc.wantReferer) {
			t.Errorf("%s: referer = %q, esperado terminar em %q", tc.path, got, tc.wantReferer)
		}
	}
}

// TestQueryStringIsBuilt confirma que os parâmetros chegam na URL.
func TestQueryStringIsBuilt(t *testing.T) {
	stub := &stubDoer{}
	s := newTestScraper(t, stub)

	query := map[string][]string{"payWithMiles": {"false"}, "starAlliance": {"true"}}
	if _, err := s.do(context.Background(), http.MethodPost, pathAvailability, query, []byte(`{}`), "tok"); err != nil {
		t.Fatalf("erro inesperado: %v", err)
	}

	url := stub.requests[0].URL.String()
	for _, want := range []string{"payWithMiles=false", "starAlliance=true"} {
		if !strings.Contains(url, want) {
			t.Errorf("URL %q não contém %q", url, want)
		}
	}
}
