package tap

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	http "github.com/bogdanfinn/fhttp"
	"github.com/bogdanfinn/fhttp/cookiejar"
	"golang.org/x/time/rate"

	"airtravel/internal/client"
	"airtravel/internal/collect"
	"airtravel/internal/config"
)

// stubRotator é um rotator programável: entrega as combinações em ordem e registra
// os bloqueios relatados.
type stubRotator struct {
	fps      []*client.Fingerprint
	current  int
	reported []string
	// exhausted faz Blocked devolver false, simulando pool esgotado.
	exhausted bool
}

func (r *stubRotator) Current() *client.Fingerprint { return r.fps[r.current] }

func (r *stubRotator) Blocked(profile string, _ error) bool {
	r.reported = append(r.reported, profile)
	if r.exhausted || r.current >= len(r.fps)-1 {
		return false
	}
	r.current++
	return true
}

// newRotatingScraper monta o adapter sobre combinações com dublês distintos, para
// que se possa afirmar QUAL delas enviou cada requisição.
func newRotatingScraper(t *testing.T, stubs ...*stubDoer) (*Scraper, *stubRotator) {
	t.Helper()

	profiles := []string{"firefox_148", "safari_ios_18_5", "firefox_135"}
	if len(stubs) > len(profiles) {
		t.Fatalf("o teste pede %d combinações; há %d perfis disponíveis", len(stubs), len(profiles))
	}

	fps := make([]*client.Fingerprint, 0, len(stubs))
	for i, stub := range stubs {
		engine, err := client.EngineFor(profiles[i])
		if err != nil {
			t.Fatalf("failed to resolve engine: %v", err)
		}
		jar, err := cookiejar.New(nil)
		if err != nil {
			t.Fatalf("failed to build jar: %v", err)
		}
		fps = append(fps, &client.Fingerprint{
			Profile: profiles[i], Engine: engine, Client: stub, Jar: jar,
		})
	}

	rot := &stubRotator{fps: fps}

	cfg := config.Default()
	cfg.MaxRetries = 3
	// Backoff curto: o que se verifica é a política, não a espera.
	cfg.RetryBackoff = time.Millisecond

	s, err := NewWithRotation(cfg, rot, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("failed to build scraper: %v", err)
	}
	s.limiter = rate.NewLimiter(rate.Inf, 1)
	return s, rot
}

func blockedResponse(t *testing.T) *http.Response {
	t.Helper()
	return respond(http.StatusForbidden, string(loadAccessDenied(t)))
}

// TestBlockRotatesInsteadOfFailing é o comportamento novo: antes, um 403 encerrava
// a coleta. Agora a requisição é refeita com a combinação seguinte.
func TestBlockRotatesInsteadOfFailing(t *testing.T) {
	blocked := &stubDoer{responses: []*http.Response{blockedResponse(t)}}
	working := &stubDoer{responses: []*http.Response{respond(http.StatusOK, `{"status":"200"}`)}}

	s, rot := newRotatingScraper(t, blocked, working)

	payload, err := s.doWithRetry(context.Background(), http.MethodGet, "/rota", nil, nil, "tok")
	if err != nil {
		t.Fatalf("err = %v; a segunda combinação deveria ter respondido", err)
	}
	if string(payload) != `{"status":"200"}` {
		t.Errorf("corpo = %q", payload)
	}

	// Cada combinação recebeu exatamente uma tentativa.
	if n := blocked.calls.Load(); n != 1 {
		t.Errorf("a combinação bloqueada recebeu %d requisições, esperado 1", n)
	}
	if n := working.calls.Load(); n != 1 {
		t.Errorf("a combinação seguinte recebeu %d requisições, esperado 1", n)
	}
	// O relato nomeia quem falhou, não quem a substituiu.
	if len(rot.reported) != 1 || rot.reported[0] != "firefox_148" {
		t.Errorf("bloqueios relatados = %v, esperado [firefox_148]", rot.reported)
	}
}

// TestRotationSendsTheNewIdentity: rotacionar o perfil TLS sem rotacionar o
// User-Agent produziria a incoerência que engine.go existe para impedir.
func TestRotationSendsTheNewIdentity(t *testing.T) {
	blocked := &stubDoer{responses: []*http.Response{blockedResponse(t)}}
	working := &stubDoer{responses: []*http.Response{respond(http.StatusOK, `{"status":"200"}`)}}

	s, _ := newRotatingScraper(t, blocked, working)

	if _, err := s.doWithRetry(context.Background(), http.MethodGet, "/rota", nil, nil, "tok"); err != nil {
		t.Fatalf("erro inesperado: %v", err)
	}

	// A primeira combinação é Gecko: sem client hints, com te: trailers.
	first := blocked.requests[0]
	if ua := first.Header.Get("user-agent"); !strings.Contains(ua, "Firefox") {
		t.Errorf("primeira requisição: user-agent = %q, esperado Firefox", ua)
	}
	if got := first.Header.Get("sec-ch-ua"); got != "" {
		t.Errorf("Gecko não deve anunciar sec-ch-ua, obtido %q", got)
	}

	// A segunda é WebKit: outro User-Agent, e continua sem client hints.
	second := working.requests[0]
	ua := second.Header.Get("user-agent")
	if !strings.Contains(ua, "Safari") || strings.Contains(ua, "Firefox") {
		t.Errorf("segunda requisição: user-agent = %q, esperado Safari", ua)
	}
	if first.Header.Get("user-agent") == ua {
		t.Error("a identidade HTTP não mudou junto com o perfil TLS")
	}
}

// TestExhaustedPoolIsTerminal: quando não há para onde rotacionar, o erro sai como
// bloqueio — insistir gastaria requisição e pioraria o bot score.
func TestExhaustedPoolIsTerminal(t *testing.T) {
	blocked := &stubDoer{responses: []*http.Response{
		blockedResponse(t),
		respond(http.StatusOK, `{"status":"200"}`), // não deve ser alcançada
	}}

	s, rot := newRotatingScraper(t, blocked)
	rot.exhausted = true

	_, err := s.doWithRetry(context.Background(), http.MethodGet, "/rota", nil, nil, "tok")
	if !errors.Is(err, collect.ErrBlocked) {
		t.Errorf("err = %v, esperado ErrBlocked", err)
	}
	if n := blocked.calls.Load(); n != 1 {
		t.Errorf("%d tentativas; sem alternativa o bloqueio é terminal", n)
	}
}

// TestRotationDoesNotSpendTheRetryBudget é a razão de os dois orçamentos serem
// separados: com MaxRetries=3, se cada bloqueio consumisse uma tentativa, um pool
// de quatro combinações nunca seria percorrido.
func TestRotationDoesNotSpendTheRetryBudget(t *testing.T) {
	a := &stubDoer{responses: []*http.Response{blockedResponse(t)}}
	b := &stubDoer{responses: []*http.Response{blockedResponse(t)}}
	c := &stubDoer{responses: []*http.Response{respond(http.StatusOK, `{"status":"200"}`)}}

	s, _ := newRotatingScraper(t, a, b, c)
	s.cfg.MaxRetries = 1 // orçamento de retry esgotado por UMA falha transitória

	payload, err := s.doWithRetry(context.Background(), http.MethodGet, "/rota", nil, nil, "tok")
	if err != nil {
		t.Fatalf("err = %v; duas rotações não deveriam consumir o orçamento de retry", err)
	}
	if string(payload) != `{"status":"200"}` {
		t.Errorf("corpo = %q", payload)
	}
	for i, stub := range []*stubDoer{a, b, c} {
		if n := stub.calls.Load(); n != 1 {
			t.Errorf("combinação %d recebeu %d requisições, esperado 1", i, n)
		}
	}
}

// TestRotationIsImmediate: bloqueio não é falha transitória. Esperar o backoff
// antes de trocar de identidade só atrasaria a coleta.
func TestRotationIsImmediate(t *testing.T) {
	blocked := &stubDoer{responses: []*http.Response{blockedResponse(t)}}
	working := &stubDoer{responses: []*http.Response{respond(http.StatusOK, `{"status":"200"}`)}}

	s, _ := newRotatingScraper(t, blocked, working)

	start := time.Now()
	if _, err := s.doWithRetry(context.Background(), http.MethodGet, "/rota", nil, nil, "tok"); err != nil {
		t.Fatalf("erro inesperado: %v", err)
	}
	// O backoff da primeira repetição transitória é de 2 s; a rotação não deve
	// passar por ele.
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("a rotação levou %s; deveria ser imediata", elapsed.Round(time.Millisecond))
	}
}

// TestTransientErrorStillBacksOff confirma que o caminho antigo não foi perdido: o
// erro de rede continua merecendo espera, e sem rotação.
func TestTransientErrorStillBacksOff(t *testing.T) {
	stub := &stubDoer{
		errs:      []error{errors.New("connection reset"), nil},
		responses: []*http.Response{nil, respond(http.StatusOK, `{"status":"200"}`)},
	}
	s, rot := newRotatingScraper(t, stub)

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()

	if _, err := s.doWithRetry(ctx, http.MethodGet, "/rota", nil, nil, "tok"); err != nil {
		t.Fatalf("err = %v, esperado sucesso na segunda tentativa", err)
	}
	if n := stub.calls.Load(); n != 2 {
		t.Errorf("%d tentativas, esperado 2", n)
	}
	if len(rot.reported) != 0 {
		t.Errorf("erro de rede relatou bloqueio: %v", rot.reported)
	}
}

// TestProfileAndEngineReportTheCurrentCombination: o Capture da API usa estes
// acessores, e depois de uma rotação eles precisam dizer a verdade.
func TestProfileAndEngineReportTheCurrentCombination(t *testing.T) {
	blocked := &stubDoer{responses: []*http.Response{blockedResponse(t)}}
	working := &stubDoer{responses: []*http.Response{respond(http.StatusOK, `{"status":"200"}`)}}

	s, _ := newRotatingScraper(t, blocked, working)

	if got := s.Profile(); got != "firefox_148" {
		t.Errorf("Profile() inicial = %q, esperado firefox_148", got)
	}
	if got := s.Engine().Name; got != "gecko" {
		t.Errorf("Engine() inicial = %q, esperado gecko", got)
	}

	if _, err := s.doWithRetry(context.Background(), http.MethodGet, "/rota", nil, nil, "tok"); err != nil {
		t.Fatalf("erro inesperado: %v", err)
	}

	if got := s.Profile(); got != "safari_ios_18_5" {
		t.Errorf("Profile() depois da rotação = %q, esperado safari_ios_18_5", got)
	}
	if got := s.Engine().Name; got != "webkit" {
		t.Errorf("Engine() depois da rotação = %q, esperado webkit", got)
	}
}

// globalStub é um rotator que rotaciona uma vez e então acusa bloqueio por volume,
// como o client.Pool faz quando várias famílias de motor são recusadas seguidas.
type globalStub struct {
	stubRotator
	global bool
}

func (g *globalStub) GlobalBlockSuspected() bool { return g.global }

// TestGlobalBlockSaysToWaitNotToRotate: a mensagem de erro precisa distinguir
// "acabaram os fingerprints" de "o WAF está recusando tudo agora".
//
// São diagnósticos opostos: o primeiro manda arranjar outro perfil, o segundo manda
// esperar. Medido em 2026-08-04, quando os seis perfis passaram a tomar 403 por
// volume e a mensagem antiga — "esgotadas as combinações" — mandava procurar no
// lugar errado.
func TestGlobalBlockSaysToWaitNotToRotate(t *testing.T) {
	for _, tc := range []struct {
		name       string
		global     bool
		wantSubstr string
		notSubstr  string
	}{
		{
			name:       "bloqueio por volume",
			global:     true,
			wantSubstr: "bloqueio por volume",
			notSubstr:  "esgotadas as combinações",
		},
		{
			name:       "recusa de identidade",
			global:     false,
			wantSubstr: "esgotadas as combinações",
			notSubstr:  "bloqueio por volume",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := &stubDoer{responses: []*http.Response{blockedResponse(t)}}
			b := &stubDoer{responses: []*http.Response{blockedResponse(t)}}

			s, rot := newRotatingScraper(t, a, b)
			g := &globalStub{stubRotator: *rot, global: tc.global}
			s.fps = g

			_, err := s.doWithRetry(context.Background(), http.MethodGet, "/rota", nil, nil, "tok")
			if err == nil {
				t.Fatal("erro esperado")
			}
			if !errors.Is(err, collect.ErrBlocked) {
				t.Errorf("err = %v, esperado embrulhar ErrBlocked", err)
			}
			if !strings.Contains(err.Error(), tc.wantSubstr) {
				t.Errorf("mensagem não menciona %q:\n  %v", tc.wantSubstr, err)
			}
			if strings.Contains(err.Error(), tc.notSubstr) {
				t.Errorf("mensagem menciona %q, que é o diagnóstico oposto:\n  %v", tc.notSubstr, err)
			}
		})
	}
}
