// Package storage persiste os dados tratados no PostgreSQL e as respostas
// brutas no Redis.
package storage

import (
	"context"
	_ "embed"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"airtravel/internal/models"
)

//go:embed schema.sql
var schemaSQL string

// Postgres guarda os dados tratados.
type Postgres struct {
	pool *pgxpool.Pool
}

// NewPostgres abre o pool e aplica o schema de forma idempotente.
func NewPostgres(ctx context.Context, dsn string) (*Postgres, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("failed to create pgx pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("failed to ping postgres: %w", err)
	}
	if _, err := pool.Exec(ctx, schemaSQL); err != nil {
		pool.Close()
		return nil, fmt.Errorf("failed to apply schema: %w", err)
	}
	return &Postgres{pool: pool}, nil
}

// Close libera o pool.
func (p *Postgres) Close() { p.pool.Close() }

// Exists informa se a busca já foi persistida DEPOIS de notBefore — base da
// retomada incremental. Ver a nota em collect.TreatedStore sobre o corte de idade.
func (p *Postgres) Exists(ctx context.Context, searchKey string, notBefore time.Time) (bool, error) {
	var found bool
	err := p.pool.QueryRow(ctx,
		`SELECT EXISTS (
			SELECT 1 FROM searches
			 WHERE search_key = $1
			   AND ($2::timestamptz IS NULL OR scraped_at >= $2))`,
		searchKey, nullIfZero(notBefore)).Scan(&found)
	if err != nil {
		return false, fmt.Errorf("failed to check search %q: %w", searchKey, err)
	}
	return found, nil
}

// SaveSearch grava a busca inteira numa única transação: cabeçalho, voos,
// segmentos, ofertas e dicionários. Reexecutar a mesma busca substitui os
// dados anteriores (upsert por chave).
func (p *Postgres) SaveSearch(
	ctx context.Context,
	key models.SearchKey,
	resp *models.SearchResponse,
	rawKey string,
	scrapedAt time.Time,
) error {
	departDate, err := parseDDMMYYYY(key.DepartDate)
	if err != nil {
		return fmt.Errorf("failed to parse depart date: %w", err)
	}

	var returnDate *time.Time
	if key.ReturnDate != "" {
		rd, err := parseDDMMYYYY(key.ReturnDate)
		if err != nil {
			return fmt.Errorf("failed to parse return date: %w", err)
		}
		returnDate = &rd
	}

	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer func() {
		// Rollback após Commit é inócuo; garante limpeza em qualquer retorno.
		_ = tx.Rollback(ctx)
	}()

	searchKey := key.String()
	totalOffers := len(resp.Data.Offers.ListOffers)

	if err := upsertSearch(ctx, tx, searchKey, key, resp, rawKey, departDate, returnDate, scrapedAt, totalOffers); err != nil {
		return err
	}

	// Substitui o conteúdo anterior: o CASCADE remove segmentos e ofertas.
	if _, err := tx.Exec(ctx, `DELETE FROM flights WHERE search_key = $1`, searchKey); err != nil {
		return fmt.Errorf("failed to clear previous flights: %w", err)
	}

	if err := insertFlights(ctx, tx, searchKey, resp.Data.ListOutbound); err != nil {
		return err
	}
	if err := insertOffers(ctx, tx, searchKey, resp); err != nil {
		return err
	}
	if err := upsertDictionaries(ctx, tx, resp.Translate); err != nil {
		return err
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("failed to commit transaction: %w", err)
	}
	return nil
}

func upsertSearch(
	ctx context.Context,
	tx pgx.Tx,
	searchKey string,
	key models.SearchKey,
	resp *models.SearchResponse,
	rawKey string,
	departDate time.Time,
	returnDate *time.Time,
	scrapedAt time.Time,
	totalOffers int,
) error {
	const q = `
		INSERT INTO searches (search_key, origin, destination, depart_date, return_date,
		                      cabin_class, market, adults, currency, office_id,
		                      total_offers, raw_key, scraped_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
		ON CONFLICT (search_key) DO UPDATE SET
			currency     = EXCLUDED.currency,
			office_id    = EXCLUDED.office_id,
			total_offers = EXCLUDED.total_offers,
			raw_key      = EXCLUDED.raw_key,
			scraped_at   = EXCLUDED.scraped_at`

	_, err := tx.Exec(ctx, q,
		searchKey, key.Origin, key.Destination, departDate, returnDate,
		key.CabinClass, key.Market, key.Adults,
		resp.Data.Offers.Currency, resp.Data.OfficeID,
		totalOffers, rawKey, scrapedAt)
	if err != nil {
		return fmt.Errorf("failed to upsert search %q: %w", searchKey, err)
	}
	return nil
}

func insertFlights(ctx context.Context, tx pgx.Tx, searchKey string, flights []models.Flight) error {
	const flightQ = `
		INSERT INTO flights (search_key, flight_id, duration_minutes, number_of_stops,
		                     route, departure_time, arrival_time)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`

	const segmentQ = `
		INSERT INTO segments (search_key, flight_id, seq, carrier, operating_carrier,
		                      flight_number, departure_airport, arrival_airport,
		                      departure_time, arrival_time, duration_minutes,
		                      stop_time_minutes, equipment, departure_terminal,
		                      arrival_terminal, codeshare)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16)`

	for i := range flights {
		flight := &flights[i]

		route := routeOf(flight)
		departure, arrival := boundsOf(flight)

		if _, err := tx.Exec(ctx, flightQ,
			searchKey, flight.IDFlight, flight.Duration, flight.NumberOfStops,
			route, departure, arrival); err != nil {
			return fmt.Errorf("failed to insert flight %d: %w", flight.IDFlight, err)
		}

		for seq := range flight.ListSegment {
			seg := &flight.ListSegment[seq]

			departureTime, err := seg.DepartureTime()
			if err != nil {
				return fmt.Errorf("flight %d segment %d: %w", flight.IDFlight, seq, err)
			}
			arrivalTime, err := seg.ArrivalTime()
			if err != nil {
				return fmt.Errorf("flight %d segment %d: %w", flight.IDFlight, seq, err)
			}

			if _, err := tx.Exec(ctx, segmentQ,
				searchKey, flight.IDFlight, seq,
				seg.Carrier, nullIfEmpty(seg.OperationCarrier), seg.FlightNumber,
				seg.DepartureAirport, seg.ArrivalAirport,
				departureTime, arrivalTime,
				seg.Duration, seg.StopTime,
				nullIfEmpty(seg.Equipment), seg.DepartureTerminal, seg.ArrivalTerminal,
				seg.CodeshareFlight); err != nil {
				return fmt.Errorf("failed to insert segment %d of flight %d: %w",
					seq, flight.IDFlight, err)
			}
		}
	}
	return nil
}

func insertOffers(ctx context.Context, tx pgx.Tx, searchKey string, resp *models.SearchResponse) error {
	const q = `
		INSERT INTO offers (search_key, offer_id, flight_id, currency, cabin,
		                    fare_family, commercial_fare_family, fare_family_rank,
		                    total_price, base_price, tax, super_saver,
		                    discounted_with_promocode, fare_basis, rbd)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15)
		ON CONFLICT (search_key, offer_id, flight_id) DO NOTHING`

	// Só se persistem ofertas cujo voo exista na resposta: a chave estrangeira
	// exige o par (search_key, flight_id).
	known := make(map[int]struct{}, len(resp.Data.ListOutbound))
	for i := range resp.Data.ListOutbound {
		known[resp.Data.ListOutbound[i].IDFlight] = struct{}{}
	}

	currency := resp.Data.Offers.Currency

	for i := range resp.Data.Offers.ListOffers {
		offer := &resp.Data.Offers.ListOffers[i]

		var fareBasis, rbd []string
		if offer.Outbound != nil {
			fareBasis, rbd = offer.Outbound.FareBasis, offer.Outbound.Rbd
		}

		for _, group := range offer.GroupFlights {
			if _, ok := known[group.IDOutBound]; !ok {
				continue
			}
			if _, err := tx.Exec(ctx, q,
				searchKey, offer.IDOffer, group.IDOutBound, currency,
				nullIfEmpty(offer.OutCabin), nullIfEmpty(offer.OutFareFamily),
				nullIfEmpty(offer.OutCommercialFareFamily), offer.OutFareFamilyHierarchy,
				offer.TotalPrice.Price, offer.TotalPrice.BasePrice, offer.TotalPrice.Tax,
				offer.TotalPrice.SuperSaver, offer.DiscountedWithPromocode,
				fareBasis, rbd); err != nil {
				return fmt.Errorf("failed to insert offer %d/flight %d: %w",
					offer.IDOffer, group.IDOutBound, err)
			}
		}
	}
	return nil
}

func upsertDictionaries(ctx context.Context, tx pgx.Tx, tr models.Translate) error {
	const airportQ = `
		INSERT INTO airports (code, name, city, country, country_code)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (code) DO UPDATE SET
			name = EXCLUDED.name, city = EXCLUDED.city,
			country = EXCLUDED.country, country_code = EXCLUDED.country_code`

	for code, loc := range tr.Locations {
		if _, err := tx.Exec(ctx, airportQ,
			code, loc.Name, loc.City, loc.Country, loc.CountryCode); err != nil {
			return fmt.Errorf("failed to upsert airport %q: %w", code, err)
		}
	}

	const airlineQ = `
		INSERT INTO airlines (code, name) VALUES ($1, $2)
		ON CONFLICT (code) DO UPDATE SET name = EXCLUDED.name`

	for code, air := range tr.Airlines {
		if _, err := tx.Exec(ctx, airlineQ, code, air.Name); err != nil {
			return fmt.Errorf("failed to upsert airline %q: %w", code, err)
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// Calendário de preços
// ---------------------------------------------------------------------------

// CalendarExists informa se o calendário desta rota foi persistido depois de
// notBefore.
//
// O corte de idade importa mais aqui do que em Exists: a calendar_key não inclui
// data, então sem ele uma rota coletada uma vez ficaria marcada como pronta para
// sempre — e a segunda execução de `./run.sh calendar` não atualizaria nada.
func (p *Postgres) CalendarExists(ctx context.Context, calendarKey string, notBefore time.Time) (bool, error) {
	var found bool
	err := p.pool.QueryRow(ctx,
		`SELECT EXISTS (
			SELECT 1 FROM calendar_prices
			 WHERE calendar_key = $1
			   AND ($2::timestamptz IS NULL OR scraped_at >= $2))`,
		calendarKey, nullIfZero(notBefore)).Scan(&found)
	if err != nil {
		return false, fmt.Errorf("failed to check calendar %q: %w", calendarKey, err)
	}
	return found, nil
}

// SaveCalendar grava o ano de preços numa única transação, com upsert por
// (calendar_key, departure_date): recoletar a mesma rota atualiza os preços em
// vez de duplicá-los.
func (p *Postgres) SaveCalendar(
	ctx context.Context,
	key models.CalendarKey,
	resp *models.CalendarResponse,
	rawKey string,
	scrapedAt time.Time,
) error {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// O DO UPDATE cobre tudo que a TAP pode mudar entre coletas, e não só o
	// preço: arrival_airport muda de verdade quando o destino é código de cidade
	// (RIO resolve para GIG ou SDU conforme a data). Atualizar só o preço deixava
	// o aeroporto congelado no da primeira coleta.
	const q = `
		INSERT INTO calendar_prices (calendar_key, origin, destination,
		                             departure_airport, arrival_airport, departure_date,
		                             cabin_class, trip_type, market, adults, currency,
		                             best_total_price, best_total_miles,
		                             monthly_minimum, monthly_maximum, star_alliance,
		                             sold_out, no_flights, insertion_date, raw_key, scraped_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21)
		ON CONFLICT (calendar_key, departure_date) DO UPDATE SET
			departure_airport = EXCLUDED.departure_airport,
			arrival_airport   = EXCLUDED.arrival_airport,
			currency          = EXCLUDED.currency,
			best_total_price  = EXCLUDED.best_total_price,
			best_total_miles  = EXCLUDED.best_total_miles,
			monthly_minimum   = EXCLUDED.monthly_minimum,
			monthly_maximum   = EXCLUDED.monthly_maximum,
			star_alliance     = EXCLUDED.star_alliance,
			sold_out          = EXCLUDED.sold_out,
			no_flights        = EXCLUDED.no_flights,
			insertion_date    = EXCLUDED.insertion_date,
			raw_key           = EXCLUDED.raw_key,
			scraped_at        = EXCLUDED.scraped_at`

	calendarKey := key.String()

	for i := range resp.Data.BestPriceForDates {
		item := &resp.Data.BestPriceForDates[i]

		departure, err := item.Departure()
		if err != nil {
			return fmt.Errorf("item %d: %w", i, err)
		}

		// insertionDate é informativo; sua ausência não invalida o registro.
		var insertion *time.Time
		if t, err := item.Inserted(); err == nil {
			insertion = &t
		}

		if _, err := tx.Exec(ctx, q,
			calendarKey, key.Origin, key.Destination,
			item.DepartureAirport, item.ArrivalAirport, departure,
			item.CabinClass, item.TripType, item.Market, key.Adults, item.Currency,
			item.BestTotalPrice, item.BestTotalMiles,
			item.MonthlyMinimum, item.MonthlyMaximum, item.StarAlliance,
			item.SoldOut, item.NoFlights, insertion, rawKey, scrapedAt); err != nil {
			return fmt.Errorf("failed to upsert calendar date %s: %w",
				departure.Format(time.DateOnly), err)
		}
	}

	if err := upsertDictionaries(ctx, tx, resp.Translate); err != nil {
		return err
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("failed to commit transaction: %w", err)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Matriz ida x volta
// ---------------------------------------------------------------------------

// ReturnsExists informa se esta combinação rota/data de ida foi persistida depois
// de notBefore.
func (p *Postgres) ReturnsExists(ctx context.Context, returnsKey string, notBefore time.Time) (bool, error) {
	var found bool
	err := p.pool.QueryRow(ctx,
		`SELECT EXISTS (
			SELECT 1 FROM calendar_return_prices
			 WHERE returns_key = $1
			   AND ($2::timestamptz IS NULL OR scraped_at >= $2))`,
		returnsKey, nullIfZero(notBefore)).Scan(&found)
	if err != nil {
		return false, fmt.Errorf("failed to check returns %q: %w", returnsKey, err)
	}
	return found, nil
}

// SaveReturns grava as datas de retorno numa transação.
//
// A coluna nights é derivada aqui em vez de calculada nas consultas: é o eixo
// natural de análise ("qual a viagem de 7 noites mais barata?").
func (p *Postgres) SaveReturns(
	ctx context.Context,
	key models.ReturnsKey,
	resp *models.CalendarReturnsResponse,
	rawKey string,
	scrapedAt time.Time,
) error {
	departure, err := parseDDMMYYYY(key.DepartDate)
	if err != nil {
		return fmt.Errorf("failed to parse depart date: %w", err)
	}

	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// Como em SaveCalendar, o DO UPDATE cobre o que a TAP pode mudar entre
	// coletas: resolved_dest e direct_flight descrevem a viagem, não a chave.
	const q = `
		INSERT INTO calendar_return_prices (returns_key, origin, destination, resolved_dest,
		                                    departure_date, return_date, nights,
		                                    cabin_class, market, adults, currency,
		                                    total_price, miles,
		                                    monthly_minimum, monthly_maximum,
		                                    sold_out, no_flights, direct_flight,
		                                    raw_key, scraped_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20)
		ON CONFLICT (returns_key, return_date) DO UPDATE SET
			resolved_dest   = EXCLUDED.resolved_dest,
			currency        = EXCLUDED.currency,
			total_price     = EXCLUDED.total_price,
			miles           = EXCLUDED.miles,
			monthly_minimum = EXCLUDED.monthly_minimum,
			monthly_maximum = EXCLUDED.monthly_maximum,
			sold_out        = EXCLUDED.sold_out,
			no_flights      = EXCLUDED.no_flights,
			direct_flight   = EXCLUDED.direct_flight,
			raw_key         = EXCLUDED.raw_key,
			scraped_at      = EXCLUDED.scraped_at`

	returnsKey := key.String()

	for i := range resp.Data.Returns {
		item := &resp.Data.Returns[i]

		returnDate, err := item.Return()
		if err != nil {
			return fmt.Errorf("item %d: %w", i, err)
		}
		nights := int(returnDate.Sub(departure).Hours() / 24)

		if _, err := tx.Exec(ctx, q,
			returnsKey, key.Origin, key.Destination, nullIfEmpty(resp.Data.Destination),
			departure, returnDate, nights,
			key.CabinClass, key.Market, key.Adults, resp.Data.Currency,
			item.Price, item.Miles,
			item.MonthlyMinimum, item.MonthlyMaximum,
			item.SoldOut, item.NoFlights, resp.Data.DirectFlight,
			rawKey, scrapedAt); err != nil {
			return fmt.Errorf("failed to upsert return date %s: %w",
				returnDate.Format(time.DateOnly), err)
		}
	}

	if err := upsertDictionaries(ctx, tx, resp.Translate); err != nil {
		return err
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("failed to commit transaction: %w", err)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Auxiliares
// ---------------------------------------------------------------------------

// parseDDMMYYYY interpreta o formato de data usado pela API (ex.: 01092026).
func parseDDMMYYYY(value string) (time.Time, error) {
	t, err := time.Parse("02012006", value)
	if err != nil {
		return time.Time{}, fmt.Errorf("data %q fora do formato DDMMYYYY: %w", value, err)
	}
	return t, nil
}

// routeOf monta a rota legível do itinerário, ex.: "LIS-GRU-SDU".
func routeOf(flight *models.Flight) string {
	if len(flight.ListSegment) == 0 {
		return ""
	}
	airports := make([]string, 0, len(flight.ListSegment)+1)
	airports = append(airports, flight.ListSegment[0].DepartureAirport)
	for i := range flight.ListSegment {
		airports = append(airports, flight.ListSegment[i].ArrivalAirport)
	}
	return strings.Join(airports, "-")
}

// boundsOf devolve partida do primeiro segmento e chegada do último.
func boundsOf(flight *models.Flight) (*time.Time, *time.Time) {
	if len(flight.ListSegment) == 0 {
		return nil, nil
	}
	var departure, arrival *time.Time
	if t, err := flight.ListSegment[0].DepartureTime(); err == nil {
		departure = &t
	}
	if t, err := flight.ListSegment[len(flight.ListSegment)-1].ArrivalTime(); err == nil {
		arrival = &t
	}
	return departure, arrival
}

func nullIfEmpty(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

// nullIfZero traduz o instante zero em NULL, que nas consultas de retomada
// significa "qualquer idade serve".
func nullIfZero(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}
