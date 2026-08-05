package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"airtravel/internal/collect"
	"airtravel/internal/models"
	"airtravel/internal/storage"
)

// Collector é a porta de coleta. Implementada por collect.Service, que concentra
// a política de persistência — a API não a repete.
type Collector interface {
	Search(ctx context.Context, p models.SearchParams, resume bool) (collect.SearchResult, error)
	Calendar(ctx context.Context, p models.SearchParams, resume bool) (collect.CalendarResult, error)
	Returns(ctx context.Context, p models.SearchParams, resume bool) (collect.ReturnsResult, error)
	Market() string
}

// Reader é a porta de leitura do histórico.
type Reader interface {
	ListSearches(ctx context.Context, f storage.SearchFilter) ([]storage.SearchSummary, error)
	GetSearch(ctx context.Context, searchKey string) (storage.SearchSummary, error)
	ListFlightOffers(ctx context.Context, searchKey string, limit int) ([]storage.FlightOffer, error)
	ListCalendar(ctx context.Context, f storage.CalendarFilter) ([]storage.CalendarEntry, error)
	ListReturns(ctx context.Context, f storage.ReturnsFilter) ([]storage.ReturnEntry, error)
	Ping(ctx context.Context) error
}

// RawReader é a porta de leitura das respostas brutas.
type RawReader interface {
	LatestRaw(ctx context.Context, searchKey string) ([]byte, error)
	Ping(ctx context.Context) error
}

// ---------------------------------------------------------------------------
// Coleta
// ---------------------------------------------------------------------------

// searchResponse é o corpo de POST /api/v1/searches.
type searchResponse struct {
	SearchKey string                `json:"searchKey"`
	Search    storage.SearchSummary `json:"search"`
	Offers    []storage.FlightOffer `json:"offers"`
	Capture   Capture               `json:"capture"`
	Warnings  []string              `json:"warnings,omitempty"`
}

// postSearch coleta voos e tarifas na TAP, persiste nos dois destinos e devolve
// as ofertas já resolvidas.
func (s *Server) postSearch(w http.ResponseWriter, r *http.Request) {
	var req SearchRequest
	if err := decodeBody(r, &req); err != nil {
		writeError(w, s.log, err)
		return
	}
	s.collectSearch(w, r, req)
}

// getFlights é o equivalente de postSearch com critérios na query string.
func (s *Server) getFlights(w http.ResponseWriter, r *http.Request) {
	q := newQuery(r)
	q.require("origin", "destination", "departureDate")

	// A conversão passa pelo binder para que `adults=abc` seja 400, como já era
	// em /calendar. Antes o erro de strconv era descartado e o valor virava o
	// padrão em silêncio.
	req := fromQuery(q)

	if err := q.Err(); err != nil {
		writeError(w, s.log, err)
		return
	}
	s.collectSearch(w, r, req)
}

func (s *Server) collectSearch(w http.ResponseWriter, r *http.Request, req SearchRequest) {
	params, err := req.toParams()
	if err != nil {
		writeError(w, s.log, err)
		return
	}

	ctx := r.Context()
	start := time.Now()

	// resume=false: um POST explícito sempre coleta.
	res, err := s.collector.Search(ctx, params, false)
	if err != nil {
		writeError(w, s.log, err)
		return
	}
	latency := time.Since(start).Milliseconds()
	profile, engine := s.currentFingerprint()

	searchKey := res.Key.String()

	offers, err := s.reader.ListFlightOffers(ctx, searchKey, req.Limit)
	if err != nil {
		// Sem o banco, ainda se pode responder a partir do que foi capturado.
		s.log.WarnContext(ctx, "leitura das ofertas falhou, usando a captura", "err", err)
		offers = offersFromCapture(res, req.Limit)
	}

	summary, err := s.reader.GetSearch(ctx, searchKey)
	if err != nil {
		summary = summaryFromCapture(res)
	}

	writeJSON(w, s.log, http.StatusOK, searchResponse{
		SearchKey: searchKey,
		Search:    summary,
		Offers:    offers,
		Capture: Capture{
			TLSProfile: profile,
			Engine:     engine,
			Market:     s.collector.Market(),
			LatencyMs:  latency,
			OfficeID:   officeID(res),
			Flights:    res.Flights(),
			Offers:     res.OfferCount(),
			RawKey:     res.RawKey,
		},
		Warnings: res.Warnings,
	})
}

// calendarResponse é o corpo de GET /api/v1/calendar.
type calendarResponse struct {
	Origin      string                  `json:"origin"`
	Destination string                  `json:"destination"`
	TripType    string                  `json:"tripType"`
	Adults      int                     `json:"adults"`
	Currency    string                  `json:"currency,omitempty"`
	Cheapest    *storage.CalendarEntry  `json:"cheapest,omitempty"`
	Dates       []storage.CalendarEntry `json:"dates"`
	Capture     *Capture                `json:"capture,omitempty"`
}

// getCalendar devolve o melhor preço por data de partida.
//
// Por padrão lê do banco; com refresh=true coleta na TAP antes. A coleta é a
// operação cara (consulta o GDS), então não é o comportamento default de um GET.
func (s *Server) getCalendar(w http.ResponseWriter, r *http.Request) {
	q := newQuery(r)
	q.require("origin", "destination")

	origin, destination := q.upper("origin"), q.upper("destination")
	// O padrão é R porque com ida e volta a TAP devolve a tarifa de round-trip,
	// que é a mais barata — é o preço que se quer ver num calendário.
	tripType := q.enum("tripType", "R", "O", "R")
	cabin := q.enum("cabinClass", "E", "E", "W", "C")
	// adults entra no filtro porque compõe a chave da coleta: sem ele, uma rota
	// coletada para 1 e para 2 adultos devolveria duas linhas por data, com
	// preços diferentes e nada que as distinguisse.
	adults := q.positive("adults", 1)
	from, to := q.date("from"), q.date("to")
	limit := q.int("limit")
	refresh := q.flag("refresh")

	if err := q.Err(); err != nil {
		writeError(w, s.log, err)
		return
	}

	ctx := r.Context()
	out := calendarResponse{
		Origin: origin, Destination: destination, TripType: tripType, Adults: adults,
	}

	if refresh {
		capture, err := s.refreshCalendar(ctx, origin, destination, cabin, tripType, adults)
		if err != nil {
			writeError(w, s.log, err)
			return
		}
		out.Capture = capture
	}

	dates, err := s.reader.ListCalendar(ctx, storage.CalendarFilter{
		Origin: origin, Destination: destination, Market: s.collector.Market(),
		CabinClass: cabin, TripType: tripType, Adults: adults,
		From: from, To: to, Limit: limit,
	})
	if err != nil {
		writeError(w, s.log, err)
		return
	}

	out.Dates = dates
	if len(dates) > 0 {
		out.Cheapest = &dates[0]
		out.Currency = dates[0].Currency
	}
	writeJSON(w, s.log, http.StatusOK, out)
}

// refreshCalendar delega a coleta ao serviço e devolve os metadados da captura.
func (s *Server) refreshCalendar(ctx context.Context, origin, destination, cabin, tripType string, adults int) (*Capture, error) {
	params := models.SearchParams{
		Origin: origin, Destination: destination,
		// O calendário ignora a data pedida e devolve um ano; qualquer data
		// válida serve de âncora.
		DepartDate: time.Now().AddDate(0, 0, 30).Format(models.DateLayout),
		Adults:     adults, CabinClass: cabin, TripType: tripType,
	}

	start := time.Now()
	res, err := s.collector.Calendar(ctx, params, false)
	if err != nil {
		return nil, err
	}

	profile, engine := s.currentFingerprint()
	return &Capture{
		TLSProfile: profile, Engine: engine, Market: s.collector.Market(),
		LatencyMs: time.Since(start).Milliseconds(),
		Offers:    res.Dates(), RawKey: res.RawKey, Warnings: res.Warnings,
	}, nil
}

// returnsResponse é o corpo de GET /api/v1/returns.
type returnsResponse struct {
	Origin        string                `json:"origin"`
	Destination   string                `json:"destination"`
	DepartureDate string                `json:"departureDate,omitempty"`
	Adults        int                   `json:"adults"`
	Currency      string                `json:"currency,omitempty"`
	Cheapest      *storage.ReturnEntry  `json:"cheapest,omitempty"`
	Combinations  []storage.ReturnEntry `json:"combinations"`
	Capture       *Capture              `json:"capture,omitempty"`
}

// getReturns devolve a matriz ida × volta.
func (s *Server) getReturns(w http.ResponseWriter, r *http.Request) {
	q := newQuery(r)
	q.require("origin", "destination")

	origin, destination := q.upper("origin"), q.upper("destination")
	cabin := q.enum("cabinClass", "E", "E", "W", "C")
	adults := q.positive("adults", 1)
	departure := q.date("departureDate")
	// minNights/maxNights são ponteiros porque zero é um valor legítimo: uma ida e
	// volta no mesmo dia. Ausente significa "sem filtro".
	minNights, maxNights := q.intPtr("minNights"), q.intPtr("maxNights")
	limit := q.int("limit")
	refresh := q.flag("refresh")

	if err := q.Err(); err != nil {
		writeError(w, s.log, err)
		return
	}

	ctx := r.Context()
	out := returnsResponse{Origin: origin, Destination: destination, Adults: adults}

	if refresh {
		if departure == nil {
			writeError(w, s.log, badRequest("refresh=true exige departureDate"))
			return
		}
		capture, err := s.refreshReturns(ctx, origin, destination, cabin, *departure, adults)
		if err != nil {
			writeError(w, s.log, err)
			return
		}
		out.Capture = capture
	}

	if departure != nil {
		out.DepartureDate = departure.Format(time.DateOnly)
	}

	combos, err := s.reader.ListReturns(ctx, storage.ReturnsFilter{
		Origin: origin, Destination: destination, Market: s.collector.Market(),
		CabinClass: cabin, Adults: adults, DepartDate: departure,
		MinNights: minNights, MaxNights: maxNights, Limit: limit,
	})
	if err != nil {
		writeError(w, s.log, err)
		return
	}

	out.Combinations = combos
	if len(combos) > 0 {
		out.Cheapest = &combos[0]
		out.Currency = combos[0].Currency
	}
	writeJSON(w, s.log, http.StatusOK, out)
}

// refreshReturns delega a coleta ao serviço.
func (s *Server) refreshReturns(ctx context.Context, origin, destination, cabin string, departure time.Time, adults int) (*Capture, error) {
	params := models.SearchParams{
		Origin: origin, Destination: destination,
		DepartDate: departure.Format(models.DateLayout),
		Adults:     adults, CabinClass: cabin, TripType: "R",
	}

	start := time.Now()
	res, err := s.collector.Returns(ctx, params, false)
	if err != nil {
		return nil, err
	}

	profile, engine := s.currentFingerprint()
	return &Capture{
		TLSProfile: profile, Engine: engine, Market: s.collector.Market(),
		LatencyMs: time.Since(start).Milliseconds(),
		Offers:    res.Dates(), RawKey: res.RawKey, Warnings: res.Warnings,
	}, nil
}

// ---------------------------------------------------------------------------
// Histórico
// ---------------------------------------------------------------------------

// listSearches devolve o histórico de buscas.
func (s *Server) listSearches(w http.ResponseWriter, r *http.Request) {
	q := newQuery(r)
	filter := storage.SearchFilter{
		Origin:      q.upper("origin"),
		Destination: q.upper("destination"),
		Market:      q.upper("market"),
		Limit:       q.int("limit"),
		Offset:      q.int("offset"),
	}
	if err := q.Err(); err != nil {
		writeError(w, s.log, err)
		return
	}

	items, err := s.reader.ListSearches(r.Context(), filter)
	if err != nil {
		writeError(w, s.log, err)
		return
	}

	writeJSON(w, s.log, http.StatusOK, map[string]any{
		"items": items,
		"count": len(items),
	})
}

// getSearch devolve uma busca do histórico com as ofertas resolvidas.
func (s *Server) getSearch(w http.ResponseWriter, r *http.Request) {
	searchKey := r.PathValue("key")
	ctx := r.Context()

	// A query é validada ANTES de consultar o banco: um limit inválido é erro do
	// cliente, e reportá-lo como 404 porque a chave também não existe esconderia
	// a causa real.
	q := newQuery(r)
	limit := q.int("limit")
	if err := q.Err(); err != nil {
		writeError(w, s.log, err)
		return
	}

	summary, err := s.reader.GetSearch(ctx, searchKey)
	if err != nil {
		writeError(w, s.log, err)
		return
	}

	offers, err := s.reader.ListFlightOffers(ctx, searchKey, limit)
	if err != nil {
		writeError(w, s.log, err)
		return
	}

	writeJSON(w, s.log, http.StatusOK, map[string]any{
		"search": summary,
		"offers": offers,
	})
}

// getSearchRaw devolve a resposta bruta guardada no Redis.
//
// Sem ?body=true responde apenas os metadados, para não despejar centenas de
// kilobytes por acidente.
func (s *Server) getSearchRaw(w http.ResponseWriter, r *http.Request) {
	searchKey := r.PathValue("key")
	ctx := r.Context()

	q := newQuery(r)
	wantBody := q.flag("body")
	if err := q.Err(); err != nil {
		writeError(w, s.log, err)
		return
	}

	summary, err := s.reader.GetSearch(ctx, searchKey)
	if err != nil {
		writeError(w, s.log, err)
		return
	}

	payload, err := s.rawReader.LatestRaw(ctx, searchKey)
	if err != nil {
		writeError(w, s.log, err)
		return
	}

	if wantBody {
		w.Header().Set("content-type", "application/json; charset=utf-8")
		if _, err := w.Write(payload); err != nil {
			s.log.ErrorContext(ctx, "falha ao escrever corpo bruto", "err", err)
		}
		return
	}

	rawKey := ""
	if summary.RawKey != nil {
		rawKey = *summary.RawKey
	}
	writeJSON(w, s.log, http.StatusOK, map[string]any{
		"searchKey": searchKey,
		"rawKey":    rawKey,
		"sizeBytes": len(payload),
		"hint":      "acrescente ?body=true para receber o JSON original da TAP",
	})
}

// ---------------------------------------------------------------------------
// Saúde
// ---------------------------------------------------------------------------

// health responde liveness: o processo está de pé.
func (s *Server) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, s.log, http.StatusOK, map[string]string{"status": "ok"})
}

// ready responde readiness: as dependências respondem.
func (s *Server) ready(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()

	checks := map[string]string{"postgres": "ok", "redis": "ok"}
	status := http.StatusOK

	if err := s.reader.Ping(ctx); err != nil {
		checks["postgres"] = err.Error()
		status = http.StatusServiceUnavailable
	}
	if err := s.rawReader.Ping(ctx); err != nil {
		checks["redis"] = err.Error()
		status = http.StatusServiceUnavailable
	}

	writeJSON(w, s.log, status, map[string]any{
		"status": map[bool]string{true: "ok", false: "degraded"}[status == http.StatusOK],
		"checks": checks,
	})
}

// ---------------------------------------------------------------------------
// Auxiliares
// ---------------------------------------------------------------------------

// maxBodyBytes limita o corpo aceito: os payloads legítimos têm centenas de
// bytes.
const maxBodyBytes = 64 << 10

// currentFingerprint devolve a combinação em uso agora, caindo nos valores do
// boot quando não há rotação configurada.
func (s *Server) currentFingerprint() (profile, engine string) {
	if s.fingerprint == nil {
		return s.tlsProfile, s.engine
	}
	return s.fingerprint()
}

// decodeBody lê e valida o corpo JSON da requisição.
func decodeBody(r *http.Request, v any) error {
	dec := json.NewDecoder(io.LimitReader(r.Body, maxBodyBytes))
	dec.DisallowUnknownFields()

	if err := dec.Decode(v); err != nil {
		if errors.Is(err, io.EOF) {
			return badRequest("corpo JSON ausente")
		}
		return badRequest("corpo JSON inválido: %v", err)
	}
	return nil
}

// officeID extrai o officeId da captura, se houver resposta.
func officeID(res collect.SearchResult) string {
	if res.Response == nil {
		return ""
	}
	return res.Response.Data.OfficeID
}

// summaryFromCapture monta um cabeçalho a partir da captura, para quando o banco
// não responde.
func summaryFromCapture(res collect.SearchResult) storage.SearchSummary {
	return storage.SearchSummary{
		SearchKey: res.Key.String(), Origin: res.Key.Origin,
		Destination: res.Key.Destination, CabinClass: res.Key.CabinClass,
		Market: res.Key.Market, Adults: res.Key.Adults,
		TotalOffers: res.OfferCount(), ScrapedAt: res.ScrapedAt,
	}
}

// offersFromCapture converte a captura em ofertas, para quando o banco não
// responde.
func offersFromCapture(res collect.SearchResult, limit int) []storage.FlightOffer {
	records := res.Offers()
	if limit > 0 && len(records) > limit {
		records = records[:limit]
	}

	out := make([]storage.FlightOffer, 0, len(records))
	for _, rec := range records {
		out = append(out, storage.FlightOffer{
			OfferID:         rec.OfferID,
			FlightID:        rec.FlightID,
			Route:           rec.RouteString(),
			FlightNumbers:   rec.FlightNumbers,
			DurationMinutes: rec.DurationMin,
			NumberOfStops:   rec.NumberOfStops,
			TotalPrice:      rec.TotalPrice,
			BasePrice:       rec.BasePrice,
			Tax:             rec.Tax,
			Currency:        rec.Currency,
			SuperSaver:      rec.SuperSaver,
		})
	}
	return out
}
