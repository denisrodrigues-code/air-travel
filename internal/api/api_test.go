package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"airtravel/internal/collect"
	"airtravel/internal/models"
	"airtravel/internal/storage"
)

// ---------------------------------------------------------------------------
// Dublês: nenhum teste toca a rede, o PostgreSQL ou o Redis.
// ---------------------------------------------------------------------------

// fakeCollector implementa api.Collector, a porta do collect.Service.
//
// Os dublês assumem o papel do serviço, não do provedor: a política de
// persistência é responsabilidade dele e é testada em internal/collect.
type fakeCollector struct {
	searchErr     error
	calendarErr   error
	returnsErr    error
	searchCalls   int
	calendarCalls int
	returnsCalls  int
	warnings      []string
	// lastParams é o que a API pediu ao serviço — parte do contrato dos endpoints
	// de coleta.
	lastParams models.SearchParams
}

func (f *fakeCollector) Market() string { return "PT" }

func (f *fakeCollector) Search(_ context.Context, p models.SearchParams, _ bool) (collect.SearchResult, error) {
	f.searchCalls++
	f.lastParams = p
	if f.searchErr != nil {
		return collect.SearchResult{}, f.searchErr
	}
	return collect.SearchResult{
		Key:       p.Key("PT"),
		RawKey:    "tap:raw:" + p.Key("PT").String() + ":1",
		ScrapedAt: time.Unix(1785766450, 0).UTC(),
		Warnings:  f.warnings,
		Response: &models.SearchResponse{
			Status: "200",
			Data: models.SearchData{
				OfficeID:     "LISTP08AB",
				ListOutbound: []models.Flight{{IDFlight: 1, Duration: 595}},
				Offers: models.Offers{
					Currency: "EUR",
					ListOffers: []models.Offer{{
						IDOffer:      1,
						GroupFlights: []models.GroupFlight{{IDOutBound: 1}},
						TotalPrice:   models.Price{Price: 615.21, BasePrice: 324, Tax: 291.21},
					}},
				},
			},
		},
	}, nil
}

func (f *fakeCollector) Calendar(_ context.Context, p models.SearchParams, _ bool) (collect.CalendarResult, error) {
	f.calendarCalls++
	f.lastParams = p
	if f.calendarErr != nil {
		return collect.CalendarResult{}, f.calendarErr
	}
	return collect.CalendarResult{
		Key:      p.CalendarKeyFor("PT"),
		RawKey:   "tap:raw:calendar:1",
		Warnings: f.warnings,
		Response: &models.CalendarResponse{
			Data: models.CalendarData{BestPriceForDates: []models.BestPriceForDate{{
				DepartureAirport: "LIS", ArrivalAirport: "GIG", Currency: "EUR",
				DepartureDate: "2026-09-01T00:00:00", InsertionDate: "2026-08-03T10:00:00",
				BestTotalPrice: 487.21, CabinClass: "E", TripType: "R", Market: "PT",
			}}},
		},
	}, nil
}

func (f *fakeCollector) Returns(_ context.Context, p models.SearchParams, _ bool) (collect.ReturnsResult, error) {
	f.returnsCalls++
	f.lastParams = p
	if f.returnsErr != nil {
		return collect.ReturnsResult{}, f.returnsErr
	}
	return collect.ReturnsResult{
		Key:      p.ReturnsKeyFor("PT"),
		RawKey:   "tap:raw:returns:1",
		Warnings: f.warnings,
		Response: &models.CalendarReturnsResponse{
			Data: models.CalendarReturnsData{
				Origin: "LIS", Destination: "GIG", Currency: "EUR", TripType: "R",
				Returns: []models.ReturnPrice{{ReturnDate: "2026-09-20T00:00:00", Price: 445.92}},
			},
		},
	}, nil
}

// fakeReader implementa api.Reader.
//
// Guarda o último filtro recebido: parte do contrato dos endpoints de leitura é
// *quais* critérios chegam ao banco, e não só o corpo devolvido.
type fakeReader struct {
	searches []storage.SearchSummary
	offers   []storage.FlightOffer
	calendar []storage.CalendarEntry
	returns  []storage.ReturnEntry
	getErr   error
	pingErr  error

	lastCalendarFilter storage.CalendarFilter
	lastReturnsFilter  storage.ReturnsFilter
}

func (f *fakeReader) ListSearches(context.Context, storage.SearchFilter) ([]storage.SearchSummary, error) {
	return f.searches, nil
}

func (f *fakeReader) GetSearch(_ context.Context, key string) (storage.SearchSummary, error) {
	if f.getErr != nil {
		return storage.SearchSummary{}, f.getErr
	}
	for _, s := range f.searches {
		if s.SearchKey == key {
			return s, nil
		}
	}
	return storage.SearchSummary{}, storage.ErrNotFound
}

func (f *fakeReader) ListFlightOffers(context.Context, string, int) ([]storage.FlightOffer, error) {
	return f.offers, nil
}

func (f *fakeReader) ListCalendar(_ context.Context, filter storage.CalendarFilter) ([]storage.CalendarEntry, error) {
	f.lastCalendarFilter = filter
	return f.calendar, nil
}

func (f *fakeReader) ListReturns(_ context.Context, filter storage.ReturnsFilter) ([]storage.ReturnEntry, error) {
	f.lastReturnsFilter = filter
	return f.returns, nil
}

func (f *fakeReader) Ping(context.Context) error { return f.pingErr }

// fakeRaw implementa api.RawReader.
type fakeRaw struct {
	payload []byte
	rawErr  error
	pingErr error
}

func (f *fakeRaw) LatestRaw(context.Context, string) ([]byte, error) {
	return f.payload, f.rawErr
}
func (f *fakeRaw) Ping(context.Context) error { return f.pingErr }

// newTestServer monta o servidor com dublês compartilhando o registro de ordem.
func newTestServer(t *testing.T, c *fakeCollector, rd *fakeReader, raw *fakeRaw) http.Handler {
	t.Helper()

	s, err := New(c, rd, raw, slog.New(slog.DiscardHandler), Options{
		Addr: ":0", Timeout: 5 * time.Second,
		TLSProfile: "firefox_148", Engine: "gecko",
	})
	if err != nil {
		t.Fatalf("failed to build server: %v", err)
	}
	return s.Handler()
}

func do(t *testing.T, h http.Handler, method, target, body string) *httptest.ResponseRecorder {
	t.Helper()

	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, target, reader)
	if body != "" {
		req.Header.Set("content-type", "application/json")
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// ---------------------------------------------------------------------------
// Testes
// ---------------------------------------------------------------------------

// TestPostSearchReturnsCapture confirma o que a API é responsável por: chamar o
// serviço e traduzir o resultado.
//
// A ordem de gravação e a política de falha de persistência NÃO são testadas
// aqui — pertencem a internal/collect, que é quem as implementa.
func TestPostSearchReturnsCapture(t *testing.T) {
	c := &fakeCollector{}
	h := newTestServer(t, c, &fakeReader{}, &fakeRaw{})

	rec := do(t, h, http.MethodPost, "/api/v1/searches",
		`{"origin":"LIS","destination":"RIO","departureDate":"2026-09-01","passengers":{"adults":1}}`)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, esperado 200 (corpo: %s)", rec.Code, rec.Body)
	}
	if c.searchCalls != 1 {
		t.Errorf("searchCalls = %d, esperado 1", c.searchCalls)
	}

	var out searchResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("resposta ilegível: %v", err)
	}
	if out.Capture.Engine != "gecko" || out.Capture.TLSProfile != "firefox_148" {
		t.Errorf("capture = %+v, esperado firefox_148/gecko", out.Capture)
	}
	if out.Capture.Market != "PT" {
		t.Errorf("market = %q, esperado PT (deve vir do Collector)", out.Capture.Market)
	}
	if out.Capture.OfficeID != "LISTP08AB" {
		t.Errorf("officeId = %q", out.Capture.OfficeID)
	}
	if !strings.HasPrefix(out.Capture.RawKey, "tap:raw:") {
		t.Errorf("rawKey = %q, esperado prefixo tap:raw:", out.Capture.RawKey)
	}
	if out.SearchKey == "" {
		t.Error("searchKey vazio")
	}
}

// TestWarningsArePropagated garante que os avisos do serviço chegam ao cliente.
//
// É o que preserva o contrato "falha de persistência não invalida a captura":
// 200 com warnings, não erro.
func TestWarningsArePropagated(t *testing.T) {
	c := &fakeCollector{warnings: []string{
		"resposta bruta não foi gravada no Redis",
		"dados tratados não foram gravados no PostgreSQL",
	}}
	h := newTestServer(t, c, &fakeReader{}, &fakeRaw{})

	rec := do(t, h, http.MethodPost, "/api/v1/searches",
		`{"origin":"LIS","destination":"RIO","departureDate":"2026-09-01","passengers":{"adults":1}}`)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, esperado 200 apesar dos avisos", rec.Code)
	}

	var out searchResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("resposta ilegível: %v", err)
	}
	if len(out.Warnings) != 2 {
		t.Errorf("warnings = %v, esperados 2", out.Warnings)
	}
}

// TestStatusMapping confirma a tradução de erro do provedor em status HTTP.
func TestStatusMapping(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want int
		code string
	}{
		{"bloqueado", collect.ErrBlocked, http.StatusBadGateway, "upstream_blocked"},
		{"rate limit", collect.ErrRateLimited, http.StatusTooManyRequests, "upstream_rate_limited"},
		{"credencial", collect.ErrUnauthorized, http.StatusBadGateway, "upstream_unauthorized"},
		{"resposta inválida", collect.ErrInvalidResponse, http.StatusBadGateway, "upstream_invalid_response"},
		{"params do domínio", collect.ErrInvalidParams, http.StatusBadRequest, "bad_request"},
		{"timeout", context.DeadlineExceeded, http.StatusGatewayTimeout, "upstream_timeout"},
		{"não encontrado", storage.ErrNotFound, http.StatusNotFound, "not_found"},
		{"interno", errors.New("qualquer"), http.StatusInternalServerError, "internal_error"},
	}

	for _, tc := range tests {
		h := newTestServer(t, &fakeCollector{searchErr: tc.err}, &fakeReader{}, &fakeRaw{})

		rec := do(t, h, http.MethodPost, "/api/v1/searches",
			`{"origin":"LIS","destination":"RIO","departureDate":"2026-09-01","passengers":{"adults":1}}`)

		if rec.Code != tc.want {
			t.Errorf("%s: status = %d, esperado %d", tc.name, rec.Code, tc.want)
		}

		var p Problem
		if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
			t.Errorf("%s: corpo ilegível: %v", tc.name, err)
			continue
		}
		if p.Code != tc.code {
			t.Errorf("%s: code = %q, esperado %q", tc.name, p.Code, tc.code)
		}
	}
}

// TestInputValidation cobre a validação de entrada, que não deve chegar à TAP.
func TestInputValidation(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{"sem origem", `{"destination":"RIO","departureDate":"2026-09-01"}`},
		{"sem destino", `{"origin":"LIS","departureDate":"2026-09-01"}`},
		{"sem data", `{"origin":"LIS","destination":"RIO"}`},
		{"origem igual ao destino", `{"origin":"LIS","destination":"LIS","departureDate":"2026-09-01"}`},
		{"data inválida", `{"origin":"LIS","destination":"RIO","departureDate":"amanhã"}`},
		{"volta antes da ida", `{"origin":"LIS","destination":"RIO","departureDate":"2026-09-10","returnDate":"2026-09-01"}`},
		{"cabine inválida", `{"origin":"LIS","destination":"RIO","departureDate":"2026-09-01","cabinClass":"X"}`},
		{"campo desconhecido", `{"origin":"LIS","destination":"RIO","departureDate":"2026-09-01","xpto":1}`},
		{"corpo vazio", ``},
	}

	for _, tc := range tests {
		c := &fakeCollector{}
		h := newTestServer(t, c, &fakeReader{}, &fakeRaw{})

		rec := do(t, h, http.MethodPost, "/api/v1/searches", tc.body)

		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, esperado 400", tc.name, rec.Code)
		}
		if c.searchCalls != 0 {
			t.Errorf("%s: a TAP foi chamada apesar da entrada inválida", tc.name)
		}
	}
}

// TestDateFormats confirma os formatos de data aceitos na entrada.
func TestDateFormats(t *testing.T) {
	for _, date := range []string{"2026-09-01", "01/09/2026", "01-09-2026", "01092026"} {
		c := &fakeCollector{}
		h := newTestServer(t, c, &fakeReader{}, &fakeRaw{})

		rec := do(t, h, http.MethodPost, "/api/v1/searches",
			`{"origin":"LIS","destination":"RIO","departureDate":"`+date+`"}`)

		if rec.Code != http.StatusOK {
			t.Errorf("data %q recusada: status %d (%s)", date, rec.Code, rec.Body)
		}
		if c.searchCalls != 1 {
			t.Errorf("data %q: a TAP não foi chamada", date)
		}
	}
}

// TestCalendarDoesNotCollectByDefault fixa a política: GET lê do banco, e só
// coleta com refresh=true, porque a coleta consulta o GDS.
func TestCalendarDoesNotCollectByDefault(t *testing.T) {
	rd := &fakeReader{calendar: []storage.CalendarEntry{{
		DepartureAirport: "LIS", ArrivalAirport: "GIG", Currency: "EUR",
		BestTotalPrice: 487.21, TripType: "R",
	}}}
	c := &fakeCollector{}
	h := newTestServer(t, c, rd, &fakeRaw{})

	rec := do(t, h, http.MethodGet, "/api/v1/calendar?origin=LIS&destination=RIO", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d (%s)", rec.Code, rec.Body)
	}
	if c.calendarCalls != 0 {
		t.Errorf("GET sem refresh chamou o coletor %d vez(es)", c.calendarCalls)
	}

	var out calendarResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("resposta ilegível: %v", err)
	}
	if out.Cheapest == nil || out.Cheapest.BestTotalPrice != 487.21 {
		t.Errorf("cheapest = %+v, esperado 487.21", out.Cheapest)
	}
	if out.Currency != "EUR" {
		t.Errorf("currency = %q", out.Currency)
	}
	if out.Capture != nil {
		t.Error("capture presente sem refresh")
	}

	// Com refresh=true, coleta.
	c2 := &fakeCollector{}
	h2 := newTestServer(t, c2, &fakeReader{}, &fakeRaw{})
	rec2 := do(t, h2, http.MethodGet, "/api/v1/calendar?origin=LIS&destination=RIO&refresh=true", "")
	if rec2.Code != http.StatusOK {
		t.Fatalf("refresh: status = %d (%s)", rec2.Code, rec2.Body)
	}
	if c2.calendarCalls != 1 {
		t.Errorf("refresh: calendarCalls = %d, esperado 1", c2.calendarCalls)
	}
}

// TestReturnsRefreshRequiresDate: sem data de ida não há o que consultar.
func TestReturnsRefreshRequiresDate(t *testing.T) {
	h := newTestServer(t, &fakeCollector{}, &fakeReader{}, &fakeRaw{})

	rec := do(t, h, http.MethodGet, "/api/v1/returns?origin=LIS&destination=RIO&refresh=true", "")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, esperado 400", rec.Code)
	}

	rec = do(t, h, http.MethodGet,
		"/api/v1/returns?origin=LIS&destination=RIO&departureDate=2026-09-01&refresh=true", "")
	if rec.Code != http.StatusOK {
		t.Errorf("com data: status = %d (%s)", rec.Code, rec.Body)
	}
}

// TestGetSearchNotFound cobre o 404 do histórico.
func TestGetSearchNotFound(t *testing.T) {
	h := newTestServer(t, &fakeCollector{}, &fakeReader{}, &fakeRaw{})

	rec := do(t, h, http.MethodGet, "/api/v1/searches/nao:existe", "")
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, esperado 404", rec.Code)
	}
}

// TestRawBodyToggle confirma que o payload bruto só sai com body=true.
func TestRawBodyToggle(t *testing.T) {
	const key = "search:LIS:RIO:01092026:OW:E:PT:1"
	rawKey := "tap:raw:" + key + ":1"
	st := &fakeReader{searches: []storage.SearchSummary{{SearchKey: key, RawKey: &rawKey}}}
	payload := []byte(`{"status":"200","data":{"x":1}}`)
	h := newTestServer(t, &fakeCollector{}, st, &fakeRaw{payload: payload})

	rec := do(t, h, http.MethodGet, "/api/v1/searches/"+key+"/raw", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d (%s)", rec.Code, rec.Body)
	}
	var meta map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &meta); err != nil {
		t.Fatalf("metadados ilegíveis: %v", err)
	}
	if meta["sizeBytes"] != float64(len(payload)) {
		t.Errorf("sizeBytes = %v, esperado %d", meta["sizeBytes"], len(payload))
	}
	if strings.Contains(rec.Body.String(), `"data"`) {
		t.Error("payload bruto vazou sem body=true")
	}

	rec = do(t, h, http.MethodGet, "/api/v1/searches/"+key+"/raw?body=true", "")
	if got := rec.Body.String(); got != string(payload) {
		t.Errorf("body=true devolveu %q, esperado o payload original", got)
	}
}

// TestReadFiltersByAdults: adults compõe a chave da coleta, então precisa chegar
// ao filtro de leitura.
//
// Sem isto, uma rota coletada para 1 e para 2 adultos devolvia DUAS linhas por
// data, com preços diferentes e nada que as distinguisse — e `cheapest` saía
// sempre da série de 1 adulto, independentemente do que o cliente pediu. É o
// mesmo erro que misturar EUR e BRL por ignorar `market`.
func TestReadFiltersByAdults(t *testing.T) {
	for _, tc := range []struct {
		name   string
		target string
		want   int
	}{
		{"calendário sem adults usa 1", "/api/v1/calendar?origin=LIS&destination=RIO", 1},
		{"calendário com adults=2", "/api/v1/calendar?origin=LIS&destination=RIO&adults=2", 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rd := &fakeReader{}
			rec := do(t, newTestServer(t, &fakeCollector{}, rd, &fakeRaw{}),
				http.MethodGet, tc.target, "")
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d (%s)", rec.Code, rec.Body)
			}
			if got := rd.lastCalendarFilter.Adults; got != tc.want {
				t.Errorf("filtro.Adults = %d, esperado %d", got, tc.want)
			}
			var out calendarResponse
			if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
				t.Fatalf("corpo ilegível: %v", err)
			}
			if out.Adults != tc.want {
				t.Errorf("resposta.adults = %d, esperado %d", out.Adults, tc.want)
			}
		})
	}

	t.Run("matriz repassa adults", func(t *testing.T) {
		rd := &fakeReader{}
		rec := do(t, newTestServer(t, &fakeCollector{}, rd, &fakeRaw{}),
			http.MethodGet, "/api/v1/returns?origin=LIS&destination=RIO&adults=3", "")
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d (%s)", rec.Code, rec.Body)
		}
		if got := rd.lastReturnsFilter.Adults; got != 3 {
			t.Errorf("filtro.Adults = %d, esperado 3", got)
		}
	})

	t.Run("adults inválido é 400", func(t *testing.T) {
		rec := do(t, newTestServer(t, &fakeCollector{}, &fakeReader{}, &fakeRaw{}),
			http.MethodGet, "/api/v1/calendar?origin=LIS&destination=RIO&adults=0", "")
		if rec.Code != http.StatusBadRequest {
			t.Errorf("status = %d, esperado 400 (%s)", rec.Code, rec.Body)
		}
	})
}

// TestCollectUsesRequestedAdults: com refresh=true, a coleta precisa usar o
// adults pedido — antes era fixo em 1, então /calendar?adults=2&refresh=true
// coletava a série de 1 adulto e depois a filtrava por 2, devolvendo vazio.
func TestCollectUsesRequestedAdults(t *testing.T) {
	c := &fakeCollector{}
	rec := do(t, newTestServer(t, c, &fakeReader{}, &fakeRaw{}), http.MethodGet,
		"/api/v1/calendar?origin=LIS&destination=RIO&adults=2&refresh=true", "")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d (%s)", rec.Code, rec.Body)
	}
	if c.lastParams.Adults != 2 {
		t.Errorf("params.Adults = %d, esperado 2", c.lastParams.Adults)
	}
}

// TestRawExpiredIsNotFound: o bruto expirado no Redis é 404, não 500.
//
// O TTL é de 7 dias e a linha tratada não expira, então a combinação "busca
// existe, bruto já não" é o caso NORMAL depois de uma semana — não uma falha do
// Redis. Antes desta correção o handler devolvia 500 internal_error, e o
// openapi.yaml prometia 404 na mesma rota.
func TestRawExpiredIsNotFound(t *testing.T) {
	const key = "search:LIS:RIO:01092026:OW:E:PT:1"
	st := &fakeReader{searches: []storage.SearchSummary{{SearchKey: key}}}
	raw := &fakeRaw{rawErr: fmt.Errorf("%w: nenhuma coleta bruta para %q",
		storage.ErrNotFound, key)}

	rec := do(t, newTestServer(t, &fakeCollector{}, st, raw),
		http.MethodGet, "/api/v1/searches/"+key+"/raw", "")

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, esperado 404 (%s)", rec.Code, rec.Body)
	}
	var problem Problem
	if err := json.Unmarshal(rec.Body.Bytes(), &problem); err != nil {
		t.Fatalf("corpo ilegível: %v", err)
	}
	if problem.Code != "not_found" {
		t.Errorf("code = %q, esperado not_found", problem.Code)
	}
}

// TestReadinessReflectsDependencies: readiness responde 503 quando uma
// dependência não responde.
func TestReadinessReflectsDependencies(t *testing.T) {
	h := newTestServer(t, &fakeCollector{}, &fakeReader{}, &fakeRaw{})
	if rec := do(t, h, http.MethodGet, "/health/ready", ""); rec.Code != http.StatusOK {
		t.Errorf("tudo saudável: status = %d, esperado 200", rec.Code)
	}

	h = newTestServer(t, &fakeCollector{},
		&fakeReader{pingErr: errors.New("sem conexão")}, &fakeRaw{})
	rec := do(t, h, http.MethodGet, "/health/ready", "")
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("postgres fora: status = %d, esperado 503", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "degraded") {
		t.Errorf("corpo não indica degradação: %s", rec.Body)
	}

	if rec := do(t, h, http.MethodGet, "/health", ""); rec.Code != http.StatusOK {
		t.Errorf("liveness não deve depender do banco: status = %d", rec.Code)
	}
}

// TestDocsAndSpec confirma a documentação embutida.
func TestDocsAndSpec(t *testing.T) {
	h := newTestServer(t, &fakeCollector{}, &fakeReader{}, &fakeRaw{})

	rec := do(t, h, http.MethodGet, "/openapi.yaml", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("spec: status = %d", rec.Code)
	}
	if ct := rec.Header().Get("content-type"); !strings.Contains(ct, "yaml") {
		t.Errorf("content-type = %q", ct)
	}
	for _, want := range []string{"openapi: 3.1.0", "/api/v1/searches", "/api/v1/calendar", "/api/v1/returns"} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Errorf("spec não menciona %q", want)
		}
	}

	if rec := do(t, h, http.MethodGet, "/docs", ""); rec.Code != http.StatusOK {
		t.Errorf("docs: status = %d", rec.Code)
	}

	rec = do(t, h, http.MethodGet, "/", "")
	if rec.Code != http.StatusFound || rec.Header().Get("location") != "/docs" {
		t.Errorf("raiz: status = %d, location = %q", rec.Code, rec.Header().Get("location"))
	}
}

// TestSpecCoversAllRoutes garante que a especificação acompanha as rotas.
//
// Falha ao acrescentar uma rota à tabela sem descrevê-la em openapi.yaml — é o
// que impede a documentação de envelhecer em silêncio.
func TestSpecCoversAllRoutes(t *testing.T) {
	srv, err := New(&fakeCollector{}, &fakeReader{}, &fakeRaw{},
		slog.New(slog.DiscardHandler), Options{Addr: ":0"})
	if err != nil {
		t.Fatalf("failed to build server: %v", err)
	}

	spec := string(openAPI)
	var documented int

	for _, r := range srv.apiRoutes() {
		if r.SpecPath == "" {
			continue // não documentada de propósito
		}
		documented++

		block, ok := specBlockFor(spec, r.SpecPath)
		if !ok {
			t.Errorf("rota %s %s não consta de openapi.yaml", r.Method, r.SpecPath)
			continue
		}
		// O método também é conferido: sem isto, acrescentar
		// `DELETE /api/v1/calendar` à tabela passava no teste só porque o *path* já
		// estava documentado — e a especificação seguia sem descrever o verbo novo.
		if !strings.Contains(block, "\n    "+strings.ToLower(r.Method)+":") {
			t.Errorf("openapi.yaml descreve %s mas não o método %s",
				r.SpecPath, strings.ToLower(r.Method))
		}
	}

	if documented == 0 {
		t.Fatal("nenhuma rota documentada; a tabela de rotas está vazia?")
	}
}

// TestSpecBlockIsScopedToOnePath prova a propriedade de que a checagem de método
// depende: o recorte não vaza para o path seguinte.
//
// Se vazasse, qualquer verbo documentado em qualquer rota satisfaria a checagem de
// todas — e o teste voltaria a ser o que era, uma busca de substring.
func TestSpecBlockIsScopedToOnePath(t *testing.T) {
	spec := string(openAPI)

	block, ok := specBlockFor(spec, "/api/v1/calendar")
	if !ok {
		t.Fatal("/api/v1/calendar não encontrado na especificação")
	}
	if !strings.Contains(block, "\n    get:") {
		t.Error("o bloco de /api/v1/calendar não contém o próprio get:")
	}
	// /api/v1/searches tem post:, /api/v1/calendar não deveria enxergá-lo.
	if strings.Contains(block, "\n    post:") {
		t.Error("o bloco de /api/v1/calendar vazou o post: de outra rota")
	}
	if strings.Contains(block, "/api/v1/returns") {
		t.Error("o bloco de /api/v1/calendar alcançou o path seguinte")
	}

	if _, ok := specBlockFor(spec, "/api/v1/naodocumentada"); ok {
		t.Error("um path inexistente foi encontrado")
	}
}

// specBlockFor recorta o trecho de openapi.yaml que descreve um path.
//
// Vai da linha do path até o próximo item de mesma indentação, que é onde as
// operações daquele path terminam. Recortar é o que permite afirmar que o método
// pertence ÀQUELE path, e não a qualquer outro da especificação.
func specBlockFor(spec, path string) (string, bool) {
	header := "\n  " + path + ":\n"
	start := strings.Index(spec, header)
	if start < 0 {
		return "", false
	}
	// O "\n" à frente é o que permite casar a PRIMEIRA operação do bloco, que
	// senão começaria sem quebra de linha antes.
	rest := "\n" + spec[start+len(header):]

	for offset := 0; ; {
		i := strings.Index(rest[offset:], "\n  ")
		if i < 0 {
			return rest, true
		}
		at := offset + i
		// Uma linha mais indentada ainda pertence a este path.
		if strings.HasPrefix(rest[at:], "\n    ") || strings.HasPrefix(rest[at:], "\n  #") {
			offset = at + 1
			continue
		}
		return rest[:at], true
	}
}

// TestMethodNotAllowed confirma o 405 com o cabeçalho Allow.
func TestMethodNotAllowed(t *testing.T) {
	h := newTestServer(t, &fakeCollector{}, &fakeReader{}, &fakeRaw{})

	rec := do(t, h, http.MethodDelete, "/api/v1/searches", "")
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, esperado 405", rec.Code)
	}
	if allow := rec.Header().Get("allow"); !strings.Contains(allow, "POST") {
		t.Errorf("Allow = %q, esperado conter POST", allow)
	}
}
