package storage

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// ErrNotFound indica ausência do registro pedido.
var ErrNotFound = errors.New("registro não encontrado")

// ---------------------------------------------------------------------------
// Histórico de buscas
// ---------------------------------------------------------------------------

// SearchSummary é o cabeçalho de uma busca persistida.
type SearchSummary struct {
	SearchKey   string     `json:"searchKey"`
	Origin      string     `json:"origin"`
	Destination string     `json:"destination"`
	DepartDate  time.Time  `json:"departDate"`
	ReturnDate  *time.Time `json:"returnDate,omitempty"`
	CabinClass  string     `json:"cabinClass"`
	Market      string     `json:"market"`
	Adults      int        `json:"adults"`
	Currency    *string    `json:"currency,omitempty"`
	OfficeID    *string    `json:"officeId,omitempty"`
	TotalOffers int        `json:"totalOffers"`
	RawKey      *string    `json:"rawKey,omitempty"`
	ScrapedAt   time.Time  `json:"scrapedAt"`
}

// SearchFilter restringe a listagem do histórico.
type SearchFilter struct {
	Origin      string
	Destination string
	Market      string
	Limit       int
	Offset      int
}

// ListSearches devolve o histórico, do mais recente para o mais antigo.
func (p *Postgres) ListSearches(ctx context.Context, f SearchFilter) ([]SearchSummary, error) {
	const q = `
		SELECT search_key, origin, destination, depart_date, return_date,
		       cabin_class, market, adults, currency, office_id,
		       total_offers, raw_key, scraped_at
		FROM searches
		WHERE ($1 = '' OR origin = $1)
		  AND ($2 = '' OR destination = $2)
		  AND ($3 = '' OR market = $3)
		ORDER BY scraped_at DESC
		LIMIT $4 OFFSET $5`

	rows, err := p.pool.Query(ctx, q,
		f.Origin, f.Destination, f.Market, limitOrDefault(f.Limit), max(f.Offset, 0))
	if err != nil {
		return nil, fmt.Errorf("failed to list searches: %w", err)
	}
	defer rows.Close()

	out := make([]SearchSummary, 0, 16)
	for rows.Next() {
		var s SearchSummary
		if err := rows.Scan(&s.SearchKey, &s.Origin, &s.Destination, &s.DepartDate,
			&s.ReturnDate, &s.CabinClass, &s.Market, &s.Adults, &s.Currency,
			&s.OfficeID, &s.TotalOffers, &s.RawKey, &s.ScrapedAt); err != nil {
			return nil, fmt.Errorf("failed to scan search: %w", err)
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("failed to read searches: %w", err)
	}
	return out, nil
}

// GetSearch devolve o cabeçalho de uma busca.
func (p *Postgres) GetSearch(ctx context.Context, searchKey string) (SearchSummary, error) {
	const q = `
		SELECT search_key, origin, destination, depart_date, return_date,
		       cabin_class, market, adults, currency, office_id,
		       total_offers, raw_key, scraped_at
		FROM searches WHERE search_key = $1`

	var s SearchSummary
	err := p.pool.QueryRow(ctx, q, searchKey).Scan(&s.SearchKey, &s.Origin,
		&s.Destination, &s.DepartDate, &s.ReturnDate, &s.CabinClass, &s.Market,
		&s.Adults, &s.Currency, &s.OfficeID, &s.TotalOffers, &s.RawKey, &s.ScrapedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return s, fmt.Errorf("%w: busca %q", ErrNotFound, searchKey)
	}
	if err != nil {
		return s, fmt.Errorf("failed to get search %q: %w", searchKey, err)
	}
	return s, nil
}

// ---------------------------------------------------------------------------
// Voos e ofertas de uma busca
// ---------------------------------------------------------------------------

// FlightOffer é uma oferta com o itinerário resolvido, pronta para exibição.
//
// Os horários são hora local do aeroporto: a duração vem de DurationMinutes,
// nunca da diferença entre eles. Ver models.ParseWallClock.
type FlightOffer struct {
	OfferID         int        `json:"offerId"`
	FlightID        int        `json:"flightId"`
	Route           string     `json:"route"`
	FlightNumbers   []string   `json:"flightNumbers"`
	DepartureTime   *time.Time `json:"departureTime,omitempty"`
	ArrivalTime     *time.Time `json:"arrivalTime,omitempty"`
	DurationMinutes int        `json:"durationMinutes"`
	NumberOfStops   int        `json:"numberOfStops"`
	// TechnicalStops conta as paradas técnicas do itinerário, que NÃO estão em
	// NumberOfStops: a TAP conta ali apenas conexões. Somados, dão o número de
	// escalas que o site anuncia ao usuário.
	TechnicalStops int     `json:"technicalStops"`
	Cabin          *string `json:"cabin,omitempty"`
	FareFamily     *string `json:"fareFamily,omitempty"`
	TotalPrice     float64 `json:"totalPrice"`
	// OutboundPrice é o preço só da perna de ida — o valor que a TAP exibe.
	// TotalPrice é a viagem inteira. Nulo quando a resposta não trouxe a perna.
	OutboundPrice *float64 `json:"outboundPrice,omitempty"`
	BasePrice     float64  `json:"basePrice"`
	Tax           float64  `json:"tax"`
	Currency      string   `json:"currency"`
	SuperSaver    bool     `json:"superSaver"`
}

// ListFlightOffers devolve as ofertas de uma busca, da mais barata para a mais
// cara, com os números de voo agregados a partir dos segmentos.
func (p *Postgres) ListFlightOffers(ctx context.Context, searchKey string, limit int) ([]FlightOffer, error) {
	const q = `
		SELECT o.offer_id, o.flight_id, f.route,
		       (SELECT array_agg(s.carrier || s.flight_number ORDER BY s.seq)
		          FROM segments s
		         WHERE s.search_key = o.search_key AND s.flight_id = o.flight_id),
		       f.departure_time, f.arrival_time, f.duration_minutes, f.number_of_stops,
		       (SELECT count(*)
		          FROM technical_stops t
		         WHERE t.search_key = o.search_key AND t.flight_id = o.flight_id),
		       o.cabin, o.fare_family, o.total_price, o.outbound_price, o.base_price, o.tax,
		       o.currency, o.super_saver
		FROM offers o
		JOIN flights f ON f.search_key = o.search_key AND f.flight_id = o.flight_id
		WHERE o.search_key = $1
		ORDER BY o.total_price, f.duration_minutes
		LIMIT $2`

	rows, err := p.pool.Query(ctx, q, searchKey, limitOrDefault(limit))
	if err != nil {
		return nil, fmt.Errorf("failed to list flight offers: %w", err)
	}
	defer rows.Close()

	out := make([]FlightOffer, 0, 32)
	for rows.Next() {
		var o FlightOffer
		if err := rows.Scan(&o.OfferID, &o.FlightID, &o.Route, &o.FlightNumbers,
			&o.DepartureTime, &o.ArrivalTime, &o.DurationMinutes, &o.NumberOfStops,
			&o.TechnicalStops,
			&o.Cabin, &o.FareFamily, &o.TotalPrice, &o.OutboundPrice, &o.BasePrice, &o.Tax,
			&o.Currency, &o.SuperSaver); err != nil {
			return nil, fmt.Errorf("failed to scan flight offer: %w", err)
		}
		out = append(out, o)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("failed to read flight offers: %w", err)
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// Calendário
// ---------------------------------------------------------------------------

// CalendarEntry é o melhor preço de uma data de partida.
type CalendarEntry struct {
	DepartureAirport string    `json:"departureAirport"`
	ArrivalAirport   string    `json:"arrivalAirport"`
	DepartureDate    time.Time `json:"departureDate"`
	CabinClass       string    `json:"cabinClass"`
	TripType         string    `json:"tripType"`
	Adults           int       `json:"adults"`
	Currency         string    `json:"currency"`
	BestTotalPrice   float64   `json:"bestTotalPrice"`
	MonthlyMinimum   bool      `json:"monthlyMinimum"`
	ScrapedAt        time.Time `json:"scrapedAt"`
}

// CalendarFilter restringe a consulta ao calendário.
type CalendarFilter struct {
	Origin      string
	Destination string
	Market      string
	CabinClass  string
	TripType    string
	// Adults é obrigatório na prática: o preço de 1 e o de 2 adultos são séries
	// distintas da mesma rota, e somá-las numa listagem produz duas linhas por
	// data com valores diferentes. Zero significa "sem filtro" e existe só para
	// diagnóstico.
	Adults int
	From   *time.Time
	To     *time.Time
	Limit  int
}

// ListCalendar devolve os preços por data, do mais barato para o mais caro.
// Datas sem voo são descartadas.
func (p *Postgres) ListCalendar(ctx context.Context, f CalendarFilter) ([]CalendarEntry, error) {
	const q = `
		SELECT departure_airport, arrival_airport, departure_date, cabin_class,
		       trip_type, adults, currency, best_total_price, monthly_minimum, scraped_at
		FROM calendar_prices
		WHERE NOT no_flights AND NOT sold_out
		  AND origin = $1 AND destination = $2
		  AND ($3 = '' OR market = $3)
		  AND ($4 = '' OR cabin_class = $4)
		  AND ($5 = '' OR trip_type = $5)
		  AND ($6 = 0 OR adults = $6)
		  AND ($7::date IS NULL OR departure_date >= $7)
		  AND ($8::date IS NULL OR departure_date <= $8)
		ORDER BY best_total_price, departure_date
		LIMIT $9`

	rows, err := p.pool.Query(ctx, q, f.Origin, f.Destination, f.Market,
		f.CabinClass, f.TripType, f.Adults, f.From, f.To, limitOrDefault(f.Limit))
	if err != nil {
		return nil, fmt.Errorf("failed to list calendar: %w", err)
	}
	defer rows.Close()

	out := make([]CalendarEntry, 0, 32)
	for rows.Next() {
		var e CalendarEntry
		if err := rows.Scan(&e.DepartureAirport, &e.ArrivalAirport, &e.DepartureDate,
			&e.CabinClass, &e.TripType, &e.Adults, &e.Currency, &e.BestTotalPrice,
			&e.MonthlyMinimum, &e.ScrapedAt); err != nil {
			return nil, fmt.Errorf("failed to scan calendar entry: %w", err)
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("failed to read calendar: %w", err)
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// Matriz ida x volta
// ---------------------------------------------------------------------------

// ReturnEntry é o preço total de uma combinação ida × volta.
type ReturnEntry struct {
	Origin        string    `json:"origin"`
	Destination   string    `json:"destination"`
	ResolvedDest  *string   `json:"resolvedDestination,omitempty"`
	DepartureDate time.Time `json:"departureDate"`
	ReturnDate    time.Time `json:"returnDate"`
	Nights        int       `json:"nights"`
	CabinClass    string    `json:"cabinClass"`
	Adults        int       `json:"adults"`
	Currency      string    `json:"currency"`
	TotalPrice    float64   `json:"totalPrice"`
	DirectFlight  bool      `json:"directFlight"`
	ScrapedAt     time.Time `json:"scrapedAt"`
}

// ReturnsFilter restringe a consulta à matriz.
type ReturnsFilter struct {
	Origin      string
	Destination string
	Market      string
	CabinClass  string
	// Adults: ver a nota de CalendarFilter.Adults.
	Adults     int
	DepartDate *time.Time
	MinNights  *int
	MaxNights  *int
	Limit      int
}

// ListReturns devolve as combinações ida × volta, da mais barata para a mais
// cara.
func (p *Postgres) ListReturns(ctx context.Context, f ReturnsFilter) ([]ReturnEntry, error) {
	const q = `
		SELECT origin, destination, resolved_dest, departure_date, return_date,
		       nights, cabin_class, adults, currency, total_price, direct_flight, scraped_at
		FROM calendar_return_prices
		WHERE NOT no_flights AND NOT sold_out
		  AND origin = $1 AND destination = $2
		  AND ($3 = '' OR market = $3)
		  AND ($4 = '' OR cabin_class = $4)
		  AND ($5 = 0 OR adults = $5)
		  AND ($6::date IS NULL OR departure_date = $6)
		  AND ($7::int IS NULL OR nights >= $7)
		  AND ($8::int IS NULL OR nights <= $8)
		ORDER BY total_price, nights
		LIMIT $9`

	rows, err := p.pool.Query(ctx, q, f.Origin, f.Destination, f.Market,
		f.CabinClass, f.Adults, f.DepartDate, f.MinNights, f.MaxNights,
		limitOrDefault(f.Limit))
	if err != nil {
		return nil, fmt.Errorf("failed to list returns: %w", err)
	}
	defer rows.Close()

	out := make([]ReturnEntry, 0, 32)
	for rows.Next() {
		var e ReturnEntry
		if err := rows.Scan(&e.Origin, &e.Destination, &e.ResolvedDest,
			&e.DepartureDate, &e.ReturnDate, &e.Nights, &e.CabinClass, &e.Adults,
			&e.Currency, &e.TotalPrice, &e.DirectFlight, &e.ScrapedAt); err != nil {
			return nil, fmt.Errorf("failed to scan return entry: %w", err)
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("failed to read returns: %w", err)
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// Saúde
// ---------------------------------------------------------------------------

// Ping verifica a conexão com o banco.
func (p *Postgres) Ping(ctx context.Context) error {
	if err := p.pool.Ping(ctx); err != nil {
		return fmt.Errorf("failed to ping postgres: %w", err)
	}
	return nil
}

// limitOrDefault aplica um teto para que uma consulta sem limite não devolva a
// tabela inteira.
func limitOrDefault(limit int) int {
	const (
		fallback = 50
		maximum  = 1000
	)
	if limit <= 0 {
		return fallback
	}
	return min(limit, maximum)
}
