package tap

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	http "github.com/bogdanfinn/fhttp"

	"airtravel/internal/models"
)

// ---------------------------------------------------------------------------
// Apoio
// ---------------------------------------------------------------------------

// validToken monta um JWT com exp no futuro. A assinatura é irrelevante: o
// adapter só lê o claim exp para saber quando renovar.
func validToken(t *testing.T) string {
	t.Helper()

	claims, err := json.Marshal(map[string]int64{"exp": time.Now().Add(5 * time.Hour).Unix()})
	if err != nil {
		t.Fatalf("failed to marshal claims: %v", err)
	}
	return "e30." + base64.RawURLEncoding.EncodeToString(claims) + ".sig"
}

// sessionResponse imita /bfm/rest/session/create, que devolve o JWT no campo
// "id" — e não em access_token, como seria de esperar.
func sessionResponse(t *testing.T) *http.Response {
	t.Helper()
	return respond(http.StatusOK, fmt.Sprintf(`{"status":"200","id":%q}`, validToken(t)))
}

// authenticated pré-carrega o token para que o teste gaste chamadas do dublê
// apenas na rota sob teste.
func authenticated(t *testing.T, s *Scraper) *Scraper {
	t.Helper()

	s.mu.Lock()
	s.token, s.tokenExp = validToken(t), time.Now().Add(5*time.Hour)
	s.mu.Unlock()
	return s
}

// sentBody devolve o corpo enviado na requisição registrada.
func sentBody(t *testing.T, req *http.Request) map[string]any {
	t.Helper()

	if req.Body == nil {
		t.Fatal("requisição sem corpo")
	}
	raw, err := io.ReadAll(req.Body)
	if err != nil {
		t.Fatalf("failed to read request body: %v", err)
	}

	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("corpo enviado não é JSON (%q): %v", raw, err)
	}
	return out
}

func calendarParams() models.SearchParams {
	return models.SearchParams{
		Origin: "LIS", Destination: "RIO", DepartDate: "01092026",
		Adults: 1, CabinClass: "E", TripType: "R",
	}
}

// calendarPayload monta uma resposta de calendário com as datas informadas.
func calendarPayload(dates ...models.BestPriceForDate) string {
	body, err := json.Marshal(models.CalendarResponse{
		Data: models.CalendarData{BestPriceForDates: dates},
	})
	if err != nil {
		panic(err)
	}
	return string(body)
}

// ---------------------------------------------------------------------------
// Calendar
// ---------------------------------------------------------------------------

// TestCalendarAuthenticatesBeforeQuerying fixa a ordem das requisições: sem JWT,
// o adapter passa por session/create antes de consultar o calendário.
func TestCalendarAuthenticatesBeforeQuerying(t *testing.T) {
	stub := &stubDoer{responses: []*http.Response{
		sessionResponse(t),
		respond(http.StatusOK, calendarPayload(models.BestPriceForDate{
			DepartureDate: "2026-09-01", BestTotalPrice: 487.21, Currency: "EUR",
		})),
	}}
	s := newTestScraper(t, stub)

	resp, raw, err := s.Calendar(context.Background(), calendarParams())
	if err != nil {
		t.Fatalf("erro inesperado: %v", err)
	}
	if n := stub.calls.Load(); n != 2 {
		t.Fatalf("%d requisições, esperado 2 (sessão + calendário)", n)
	}
	if got := stub.requests[0].URL.Path; !strings.HasSuffix(got, pathSessionCreate) {
		t.Errorf("primeira requisição foi para %q, esperado %q", got, pathSessionCreate)
	}
	if got := stub.requests[1].URL.Path; !strings.HasSuffix(got, pathCalendar) {
		t.Errorf("segunda requisição foi para %q, esperado %q", got, pathCalendar)
	}
	if len(resp.Data.BestPriceForDates) != 1 {
		t.Errorf("%d datas decodificadas, esperado 1", len(resp.Data.BestPriceForDates))
	}

	// O bruto devolvido é o que vai para o Redis: precisa ser o payload intacto.
	if !strings.Contains(string(raw), "487.21") {
		t.Errorf("bruto = %q, esperado o payload original", raw)
	}
}

// TestCalendarRequestBodyMatchesCapturedTraffic fixa o payload medido no
// navegador: data em DDMMYYYY e tripType derivado dos parâmetros.
func TestCalendarRequestBodyMatchesCapturedTraffic(t *testing.T) {
	stub := &stubDoer{responses: []*http.Response{
		respond(http.StatusOK, calendarPayload(models.BestPriceForDate{DepartureDate: "2026-09-01", BestTotalPrice: 1})),
	}}
	s := authenticated(t, newTestScraper(t, stub))

	if _, _, err := s.Calendar(context.Background(), calendarParams()); err != nil {
		t.Fatalf("erro inesperado: %v", err)
	}

	body := sentBody(t, stub.requests[0])
	for field, want := range map[string]any{
		"origin":        "LIS",
		"destination":   "RIO",
		"departureDate": "01092026", // DDMMYYYY, ao contrário de calendarReturns
		"tripType":      "R",
		"cabinClass":    "E",
		"market":        "PT",
		"language":      "pt",
	} {
		if got := fmt.Sprint(body[field]); got != want {
			t.Errorf("%s = %q, esperado %q", field, got, want)
		}
	}
}

// TestCalendarTripTypeFallsBackToOneWay: sem TripType explícito o padrão é O, e
// ele muda o preço devolvido — uma ida-e-volta não custa o dobro de uma ida.
func TestCalendarTripTypeFallsBackToOneWay(t *testing.T) {
	stub := &stubDoer{responses: []*http.Response{
		respond(http.StatusOK, calendarPayload(models.BestPriceForDate{DepartureDate: "2026-09-01", BestTotalPrice: 1})),
	}}
	s := authenticated(t, newTestScraper(t, stub))

	p := calendarParams()
	p.TripType = ""

	if _, _, err := s.Calendar(context.Background(), p); err != nil {
		t.Fatalf("erro inesperado: %v", err)
	}
	if got := fmt.Sprint(sentBody(t, stub.requests[0])["tripType"]); got != "O" {
		t.Errorf("tripType = %q, esperado O", got)
	}
}

// TestCalendarEmptyBodyIsError cobre o comportamento real da rota: ela responde
// 200 com corpo VAZIO quando o payload não é aceito. Sem esta guarda o erro
// apareceria como "unexpected end of JSON input", que não diz nada.
func TestCalendarEmptyBodyIsError(t *testing.T) {
	stub := &stubDoer{responses: []*http.Response{respond(http.StatusOK, ``)}}
	s := authenticated(t, newTestScraper(t, stub))

	_, _, err := s.Calendar(context.Background(), calendarParams())
	if !errors.Is(err, ErrAPIStatus) {
		t.Fatalf("err = %v, esperado ErrAPIStatus", err)
	}
	if !strings.Contains(err.Error(), "vazio") {
		t.Errorf("erro não explica a causa: %v", err)
	}
}

// TestCalendarWithoutDatesStillReturnsPayload: um calendário sem datas é erro,
// mas a resposta e o bruto vêm junto — é o que permite gravar a captura e
// investigar depois, em vez de perder a requisição que já foi gasta.
func TestCalendarWithoutDatesStillReturnsPayload(t *testing.T) {
	stub := &stubDoer{responses: []*http.Response{respond(http.StatusOK, calendarPayload())}}
	s := authenticated(t, newTestScraper(t, stub))

	resp, raw, err := s.Calendar(context.Background(), calendarParams())
	if !errors.Is(err, ErrAPIStatus) {
		t.Fatalf("err = %v, esperado ErrAPIStatus", err)
	}
	if resp == nil {
		t.Error("resposta nil; o payload decodificado deveria acompanhar o erro")
	}
	if len(raw) == 0 {
		t.Error("bruto vazio; a captura seria perdida")
	}
}

// TestCalendarMalformedJSONKeepsRaw: o mesmo princípio quando o corpo não
// decodifica — sem a resposta, mas com o bruto para diagnóstico.
func TestCalendarMalformedJSONKeepsRaw(t *testing.T) {
	stub := &stubDoer{responses: []*http.Response{respond(http.StatusOK, `{"data":`)}}
	s := authenticated(t, newTestScraper(t, stub))

	resp, raw, err := s.Calendar(context.Background(), calendarParams())
	if err == nil {
		t.Fatal("JSON malformado foi aceito")
	}
	if resp != nil {
		t.Error("resposta não deveria ser devolvida se não decodificou")
	}
	if string(raw) != `{"data":` {
		t.Errorf("bruto = %q, esperado o corpo original", raw)
	}
}

// TestCalendarRejectsBadParamsBeforeAnyRequest: parâmetro inválido não pode
// custar requisição — cada uma pesa no bot score.
func TestCalendarRejectsBadParamsBeforeAnyRequest(t *testing.T) {
	for name, mutate := range map[string]func(*models.SearchParams){
		"sem origem":      func(p *models.SearchParams) { p.Origin = "" },
		"data inválida":   func(p *models.SearchParams) { p.DepartDate = "2026-09-01" },
		"cabine inválida": func(p *models.SearchParams) { p.CabinClass = "Z" },
	} {
		stub := &stubDoer{}
		s := authenticated(t, newTestScraper(t, stub))

		p := calendarParams()
		mutate(&p)

		if _, _, err := s.Calendar(context.Background(), p); err == nil {
			t.Errorf("%s: aceito indevidamente", name)
		}
		if n := stub.calls.Load(); n != 0 {
			t.Errorf("%s: %d requisições disparadas, esperado 0", name, n)
		}
	}
}

// ---------------------------------------------------------------------------
// CalendarReturns
// ---------------------------------------------------------------------------

func returnsPayload(dates ...models.ReturnPrice) string {
	body, err := json.Marshal(models.CalendarReturnsResponse{
		Data: models.CalendarReturnsData{
			Origin: "LIS", Destination: "GIG", Currency: "EUR", Returns: dates,
		},
	})
	if err != nil {
		panic(err)
	}
	return string(body)
}

// TestCalendarReturnsInvertsTheRoute é a armadilha do endpoint: origin e
// destination descrevem a perna de VOLTA, então vão invertidos em relação ao
// sentido da viagem. Enviar na ordem natural devolve preços de outra rota — sem
// erro nenhum, o que torna a falha invisível.
func TestCalendarReturnsInvertsTheRoute(t *testing.T) {
	stub := &stubDoer{responses: []*http.Response{
		respond(http.StatusOK, returnsPayload(models.ReturnPrice{ReturnDate: "2026-09-19", Price: 445.92})),
	}}
	s := authenticated(t, newTestScraper(t, stub))

	if _, _, err := s.CalendarReturns(context.Background(), calendarParams()); err != nil {
		t.Fatalf("erro inesperado: %v", err)
	}

	body := sentBody(t, stub.requests[0])
	if got := fmt.Sprint(body["origin"]); got != "RIO" {
		t.Errorf("origin = %q, esperado RIO (o destino da viagem)", got)
	}
	if got := fmt.Sprint(body["destination"]); got != "LIS" {
		t.Errorf("destination = %q, esperado LIS (a origem da viagem)", got)
	}
}

// TestCalendarReturnsUsesISODate: esta rota recebe a data em ISO, e não no
// DDMMYYYY usado por search e pelo calendário.
func TestCalendarReturnsUsesISODate(t *testing.T) {
	stub := &stubDoer{responses: []*http.Response{
		respond(http.StatusOK, returnsPayload(models.ReturnPrice{ReturnDate: "2026-09-19", Price: 1})),
	}}
	s := authenticated(t, newTestScraper(t, stub))

	if _, _, err := s.CalendarReturns(context.Background(), calendarParams()); err != nil {
		t.Fatalf("erro inesperado: %v", err)
	}

	body := sentBody(t, stub.requests[0])
	if got := fmt.Sprint(body["departureDate"]); got != "2026-09-01" {
		t.Errorf("departureDate = %q, esperado 2026-09-01", got)
	}
	// A rota só responde para ida-e-volta: perguntar por datas de volta de uma
	// viagem só de ida não faz sentido, então o tipo é fixo.
	if got := fmt.Sprint(body["tripType"]); got != "R" {
		t.Errorf("tripType = %q, esperado R fixo", got)
	}
	if got := fmt.Sprint(body["paxType"]); got != "ADT" {
		t.Errorf("paxType = %q, esperado ADT", got)
	}
}

// TestCalendarReturnsForcesRoundTripEvenForOneWay: mesmo com TripType O nos
// parâmetros, o payload sai como R.
func TestCalendarReturnsForcesRoundTripEvenForOneWay(t *testing.T) {
	stub := &stubDoer{responses: []*http.Response{
		respond(http.StatusOK, returnsPayload(models.ReturnPrice{ReturnDate: "2026-09-19", Price: 1})),
	}}
	s := authenticated(t, newTestScraper(t, stub))

	p := calendarParams()
	p.TripType = "O"

	if _, _, err := s.CalendarReturns(context.Background(), p); err != nil {
		t.Fatalf("erro inesperado: %v", err)
	}
	if got := fmt.Sprint(sentBody(t, stub.requests[0])["tripType"]); got != "R" {
		t.Errorf("tripType = %q, esperado R", got)
	}
}

// TestCalendarReturnsResolvesDestination confirma que o destino concreto vem da
// resposta: uma busca por RIO devolve GIG, e é isso que deve ser persistido.
func TestCalendarReturnsResolvesDestination(t *testing.T) {
	stub := &stubDoer{responses: []*http.Response{
		respond(http.StatusOK, returnsPayload(
			models.ReturnPrice{ReturnDate: "2026-09-19", Price: 445.92},
			models.ReturnPrice{ReturnDate: "2026-09-20", NoFlights: true},
		)),
	}}
	s := authenticated(t, newTestScraper(t, stub))

	resp, _, err := s.CalendarReturns(context.Background(), calendarParams())
	if err != nil {
		t.Fatalf("erro inesperado: %v", err)
	}
	if resp.Data.Destination != "GIG" {
		t.Errorf("Destination = %q, esperado GIG", resp.Data.Destination)
	}
	if n := len(resp.Data.Bookable()); n != 1 {
		t.Errorf("%d datas comercializáveis, esperado 1", n)
	}
	if c := resp.Data.Cheapest(); c == nil || c.Price != 445.92 {
		t.Errorf("Cheapest() = %+v", c)
	}
}

// TestCalendarReturnsEmptyBodyIsError espelha a guarda do calendário.
func TestCalendarReturnsEmptyBodyIsError(t *testing.T) {
	stub := &stubDoer{responses: []*http.Response{respond(http.StatusOK, ``)}}
	s := authenticated(t, newTestScraper(t, stub))

	if _, _, err := s.CalendarReturns(context.Background(), calendarParams()); !errors.Is(err, ErrAPIStatus) {
		t.Errorf("err = %v, esperado ErrAPIStatus", err)
	}
}

// TestCalendarReturnsWithoutDatesStillReturnsPayload: mesmo contrato do
// calendário — a captura não se perde.
func TestCalendarReturnsWithoutDatesStillReturnsPayload(t *testing.T) {
	stub := &stubDoer{responses: []*http.Response{respond(http.StatusOK, returnsPayload())}}
	s := authenticated(t, newTestScraper(t, stub))

	resp, raw, err := s.CalendarReturns(context.Background(), calendarParams())
	if !errors.Is(err, ErrAPIStatus) {
		t.Fatalf("err = %v, esperado ErrAPIStatus", err)
	}
	if resp == nil || len(raw) == 0 {
		t.Error("resposta ou bruto ausentes; a captura seria perdida")
	}
}

// TestCalendarReturnsRejectsBadParamsBeforeAnyRequest cobre também a data, que
// aqui passa por um segundo parsing para virar ISO.
func TestCalendarReturnsRejectsBadParamsBeforeAnyRequest(t *testing.T) {
	for name, mutate := range map[string]func(*models.SearchParams){
		"sem destino":   func(p *models.SearchParams) { p.Destination = "" },
		"data inválida": func(p *models.SearchParams) { p.DepartDate = "31022026" },
	} {
		stub := &stubDoer{}
		s := authenticated(t, newTestScraper(t, stub))

		p := calendarParams()
		mutate(&p)

		if _, _, err := s.CalendarReturns(context.Background(), p); err == nil {
			t.Errorf("%s: aceito indevidamente", name)
		}
		if n := stub.calls.Load(); n != 0 {
			t.Errorf("%s: %d requisições disparadas, esperado 0", name, n)
		}
	}
}

// TestCalendarPropagatesBlock: um bloqueio na rota de calendário tem de chegar
// ao caso de uso como collect.ErrBlocked, senão o Runner não sabe distinguir de
// um erro qualquer.
func TestCalendarPropagatesBlock(t *testing.T) {
	stub := &stubDoer{responses: []*http.Response{
		respond(http.StatusForbidden, string(loadAccessDenied(t))),
	}}
	s := authenticated(t, newTestScraper(t, stub))

	_, _, err := s.Calendar(context.Background(), calendarParams())
	if !errors.Is(err, ErrAccessDenied) {
		t.Errorf("err = %v, esperado ErrAccessDenied", err)
	}
}
