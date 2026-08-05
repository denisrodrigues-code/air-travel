//go:build integration

// Testes contra PostgreSQL e Redis reais.
//
// Ficam atrás da tag `integration` para que `go test ./...` continue offline e
// sem Docker. Rode com:
//
//	./run.sh up && ./run.sh test-integration
//
// Um banco real é indispensável aqui: o bug de fuso horário (CLAUDE.md §3) era
// invisível para um dublê — só apareceu quando uma coluna TIMESTAMPTZ converteu
// a hora de parede que a TAP devolve.
package storage

import (
	"context"
	"errors"
	"testing"
	"time"

	"airtravel/internal/config"
	"airtravel/internal/models"
)

func newStores(t *testing.T) (*Postgres, *Redis) {
	t.Helper()

	cfg := config.Default()
	ctx := context.Background()

	pg, err := NewPostgres(ctx, cfg.PostgresDSN)
	if err != nil {
		t.Skipf("PostgreSQL indisponível (%v); rode ./run.sh up", err)
	}
	t.Cleanup(pg.Close)

	rdb, err := NewRedis(ctx, cfg.RedisAddr, cfg.RedisPass, cfg.RedisDB, cfg.RawTTL)
	if err != nil {
		t.Skipf("Redis indisponível (%v); rode ./run.sh up", err)
	}
	t.Cleanup(func() { _ = rdb.Close() })

	// Os testes escrevem no MESMO banco que a aplicação usa — não há base de
	// teste separada. Sem esta limpeza as linhas sentinela sobrevivem à suíte e
	// aparecem em `./run.sh queries` e em GET /api/v1/searches, onde parecem
	// coleta real. Registrado antes dos Close acima para rodar depois deles não
	// ser possível: t.Cleanup é LIFO, então isto executa primeiro, com as
	// conexões ainda abertas.
	t.Cleanup(func() { purgeSentinel(t, pg, rdb) })

	return pg, rdb
}

// sentinelRoute é a rota fictícia que uniqueKey usa. Serve de marca para a
// limpeza: nenhuma coleta real usa TST→XXX no mercado ZZ.
const sentinelRoute, sentinelMarket = "TST:XXX", "ZZ"

// purgeSentinel apaga o que os testes gravaram.
//
// Falhas aqui são registradas, não fatais: um teste que passou não deve ser
// reprovado por causa da limpeza, e o resíduo é visível pela marca sentinela.
func purgeSentinel(t *testing.T, pg *Postgres, rdb *Redis) {
	t.Helper()
	ctx := context.Background()

	// searches leva flights, segments e offers por CASCADE.
	for _, table := range []string{"searches", "calendar_prices", "calendar_return_prices"} {
		if _, err := pg.pool.Exec(ctx, "DELETE FROM "+table+" WHERE market = $1", sentinelMarket); err != nil {
			t.Logf("limpeza de %s falhou: %v", table, err)
		}
	}

	// Os dicionários não têm coluna market, então a limpeza por mercado não os
	// alcança — o aeroporto "TST · Teste · Cidade · País" sobrevivia à suíte e
	// aparecia na consulta 7 de queries.sql ao lado dos aeroportos reais.
	//
	// Só os códigos sentinela são apagados: TP e os demais vêm de coleta real e
	// apagá-los faria a suíte destruir dado do usuário.
	if _, err := pg.pool.Exec(ctx,
		`DELETE FROM airports WHERE code = ANY($1)`,
		[]string{"TST", "XXX", "YYY"}); err != nil {
		t.Logf("limpeza de airports falhou: %v", err)
	}

	// As chaves brutas expiram pelo TTL de 7 dias, mas apagá-las mantém
	// `./run.sh redis` legível no dia seguinte.
	keys, err := rdb.client.Keys(ctx, "tap:raw:*"+sentinelRoute+"*").Result()
	if err != nil {
		t.Logf("listagem de chaves do Redis falhou: %v", err)
		return
	}
	if len(keys) == 0 {
		return
	}
	if err := rdb.client.Del(ctx, keys...).Err(); err != nil {
		t.Logf("limpeza de %d chaves do Redis falhou: %v", len(keys), err)
	}
}

// uniqueKey evita colisão entre execuções e com dados de coleta real.
func uniqueKey(t *testing.T) models.SearchKey {
	t.Helper()
	return models.SearchKey{
		Origin: "TST", Destination: "XXX", DepartDate: "01092026",
		CabinClass: "E", Market: "ZZ", Adults: 1,
	}
}

// TestSchemaIsIdempotent: NewPostgres aplica o schema no boot, e a aplicação
// pode subir várias vezes.
func TestSchemaIsIdempotent(t *testing.T) {
	cfg := config.Default()
	ctx := context.Background()

	for i := range 3 {
		pg, err := NewPostgres(ctx, cfg.PostgresDSN)
		if err != nil {
			t.Skipf("PostgreSQL indisponível: %v", err)
		}
		if err := pg.Ping(ctx); err != nil {
			t.Errorf("aplicação %d: ping falhou: %v", i+1, err)
		}
		pg.Close()
	}
}

// TestFlightTimesArePreservedAsWallClock é o teste que só um banco real dá.
//
// A TAP devolve "2026-09-01T23:30:00.000Z" mas o valor é hora local do aeroporto.
// Com TIMESTAMPTZ, o PostgreSQL interpretaria como UTC e a leitura devolveria
// outro horário conforme o fuso da sessão. As colunas são TIMESTAMP justamente
// para isso.
func TestFlightTimesArePreservedAsWallClock(t *testing.T) {
	pg, rdb := newStores(t)
	ctx := context.Background()
	key := uniqueKey(t)

	resp := &models.SearchResponse{
		Status: "200",
		Data: models.SearchData{
			OfficeID: "TSTOFFICE",
			ListOutbound: []models.Flight{{
				IDFlight: 1, Duration: 620, NumberOfStops: 0,
				ListSegment: []models.Segment{{
					Carrier: "TP", FlightNumber: "87",
					DepartureAirport: "LIS", ArrivalAirport: "GRU",
					DepartureDate: "2026-09-01T23:30:00.000Z",
					ArrivalDate:   "2026-09-02T05:50:00.000Z",
					Duration:      620,
				}},
			}},
			Offers: models.Offers{Currency: "EUR", ListOffers: []models.Offer{{
				IDOffer:      1,
				GroupFlights: []models.GroupFlight{{IDOutBound: 1}},
				TotalPrice:   models.Price{Price: 615.21, BasePrice: 324, Tax: 291.21},
			}}},
		},
	}

	at := time.Now().UTC()
	rawKey, err := rdb.SaveRaw(ctx, key.String(), at, []byte(`{"status":"200"}`))
	if err != nil {
		t.Fatalf("SaveRaw: %v", err)
	}
	if err := pg.SaveSearch(ctx, key, resp, rawKey, at); err != nil {
		t.Fatalf("SaveSearch: %v", err)
	}

	offers, err := pg.ListFlightOffers(ctx, key.String(), 10)
	if err != nil {
		t.Fatalf("ListFlightOffers: %v", err)
	}
	if len(offers) != 1 {
		t.Fatalf("len(offers) = %d, esperado 1", len(offers))
	}

	o := offers[0]
	if o.DepartureTime == nil || o.ArrivalTime == nil {
		t.Fatal("horários nulos")
	}

	// A hora de parede tem de sobreviver ao ir e voltar do banco.
	if h, m := o.DepartureTime.Hour(), o.DepartureTime.Minute(); h != 23 || m != 30 {
		t.Errorf("partida = %02d:%02d, esperado 23:30 (hora local de Lisboa)", h, m)
	}
	if h, m := o.ArrivalTime.Hour(), o.ArrivalTime.Minute(); h != 5 || m != 50 {
		t.Errorf("chegada = %02d:%02d, esperado 05:50 (hora local de São Paulo)", h, m)
	}

	// E a duração vem da API, não da subtração — que daria 380 em vez de 620.
	if o.DurationMinutes != 620 {
		t.Errorf("durationMinutes = %d, esperado 620", o.DurationMinutes)
	}
	if wall := int(o.ArrivalTime.Sub(*o.DepartureTime).Minutes()); wall == o.DurationMinutes {
		t.Errorf("a diferença de parede (%d) igualou o duration; os fusos deixaram de diferir?", wall)
	}
}

// TestSaveSearchIsUpsert: recoletar a mesma busca atualiza, não duplica.
func TestSaveSearchIsUpsert(t *testing.T) {
	pg, _ := newStores(t)
	ctx := context.Background()
	key := uniqueKey(t)

	resp := &models.SearchResponse{
		Status: "200",
		Data: models.SearchData{
			ListOutbound: []models.Flight{{IDFlight: 1, Duration: 100, ListSegment: []models.Segment{{
				Carrier: "TP", FlightNumber: "1", DepartureAirport: "LIS", ArrivalAirport: "GRU",
				DepartureDate: "2026-09-01T10:00:00.000Z", ArrivalDate: "2026-09-01T12:00:00.000Z",
			}}}},
			Offers: models.Offers{Currency: "EUR", ListOffers: []models.Offer{{
				IDOffer: 1, GroupFlights: []models.GroupFlight{{IDOutBound: 1}},
				TotalPrice: models.Price{Price: 100},
			}}},
		},
	}

	for i := range 2 {
		if err := pg.SaveSearch(ctx, key, resp, "raw", time.Now().UTC()); err != nil {
			t.Fatalf("SaveSearch %d: %v", i+1, err)
		}
	}

	offers, err := pg.ListFlightOffers(ctx, key.String(), 100)
	if err != nil {
		t.Fatalf("ListFlightOffers: %v", err)
	}
	if len(offers) != 1 {
		t.Errorf("len(offers) = %d após duas gravações, esperado 1 (upsert)", len(offers))
	}

	if exists, err := pg.Exists(ctx, key.String(), time.Time{}); err != nil || !exists {
		t.Errorf("Exists = %v, %v; esperado true", exists, err)
	}
}

// TestGetSearchNotFound cobre o erro sentinela que a API mapeia para 404.
func TestGetSearchNotFound(t *testing.T) {
	pg, _ := newStores(t)

	_, err := pg.GetSearch(context.Background(), "search:NAO:EXISTE:01012000:OW:E:ZZ:1")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, esperado ErrNotFound", err)
	}
}

// TestCalendarRoundTrip cobre gravação e leitura do calendário, incluindo o
// filtro que descarta datas sem voo.
func TestCalendarRoundTrip(t *testing.T) {
	pg, _ := newStores(t)
	ctx := context.Background()

	key := models.CalendarKey{
		Origin: "TST", Destination: "XXX", CabinClass: "E",
		TripType: "R", Market: "ZZ", Adults: 1,
	}

	resp := &models.CalendarResponse{Data: models.CalendarData{BestPriceForDates: []models.BestPriceForDate{
		{DepartureAirport: "TST", ArrivalAirport: "XXX", DepartureDate: "2026-09-01T00:00:00",
			InsertionDate: "2026-08-03T10:00:00", BestTotalPrice: 487.21, Currency: "EUR",
			CabinClass: "E", TripType: "R", Market: "ZZ", MonthlyMinimum: true},
		{DepartureAirport: "TST", ArrivalAirport: "XXX", DepartureDate: "2026-09-02T00:00:00",
			InsertionDate: "2026-08-03T10:00:00", BestTotalPrice: 0, Currency: "EUR",
			CabinClass: "E", TripType: "R", Market: "ZZ", NoFlights: true},
	}}}

	if err := pg.SaveCalendar(ctx, key, resp, "raw", time.Now().UTC()); err != nil {
		t.Fatalf("SaveCalendar: %v", err)
	}

	dates, err := pg.ListCalendar(ctx, CalendarFilter{
		Origin: "TST", Destination: "XXX", Market: "ZZ", TripType: "R", Limit: 100,
	})
	if err != nil {
		t.Fatalf("ListCalendar: %v", err)
	}
	if len(dates) != 1 {
		t.Fatalf("len(dates) = %d, esperado 1 (a data sem voo deve ser filtrada)", len(dates))
	}
	if dates[0].BestTotalPrice != 487.21 {
		t.Errorf("preço = %v", dates[0].BestTotalPrice)
	}

	if exists, err := pg.CalendarExists(ctx, key.String(), time.Time{}); err != nil || !exists {
		t.Errorf("CalendarExists = %v, %v", exists, err)
	}
}

// TestCalendarSeparatesByAdults: o preço de 1 e o de 2 adultos são séries
// distintas da mesma rota, e a listagem tem de devolver só a pedida.
//
// Antes de adults ser coluna, as duas coexistiam sem forma de distinguí-las: a
// resposta trazia duas linhas para cada data, com valores diferentes, e a mais
// barata ganhava — que é sempre a de menos passageiros.
func TestCalendarSeparatesByAdults(t *testing.T) {
	pg, _ := newStores(t)
	ctx := context.Background()

	entry := func(price float64) *models.CalendarResponse {
		return &models.CalendarResponse{Data: models.CalendarData{
			BestPriceForDates: []models.BestPriceForDate{{
				DepartureAirport: "TST", ArrivalAirport: "XXX",
				DepartureDate: "2026-09-01T00:00:00", InsertionDate: "2026-08-03T10:00:00",
				BestTotalPrice: price, Currency: "EUR",
				CabinClass: "E", TripType: "R", Market: "ZZ",
			}},
		}}
	}

	for _, tc := range []struct {
		adults int
		price  float64
	}{{1, 487.21}, {2, 974.42}} {
		key := models.CalendarKey{
			Origin: "TST", Destination: "XXX", CabinClass: "E",
			TripType: "R", Market: "ZZ", Adults: tc.adults,
		}
		if err := pg.SaveCalendar(ctx, key, entry(tc.price), "raw", time.Now().UTC()); err != nil {
			t.Fatalf("SaveCalendar adults=%d: %v", tc.adults, err)
		}
	}

	for _, tc := range []struct {
		adults int
		price  float64
	}{{1, 487.21}, {2, 974.42}} {
		dates, err := pg.ListCalendar(ctx, CalendarFilter{
			Origin: "TST", Destination: "XXX", Market: "ZZ", TripType: "R",
			Adults: tc.adults, Limit: 100,
		})
		if err != nil {
			t.Fatalf("ListCalendar adults=%d: %v", tc.adults, err)
		}
		if len(dates) != 1 {
			t.Fatalf("adults=%d: len(dates) = %d, esperado 1", tc.adults, len(dates))
		}
		if dates[0].BestTotalPrice != tc.price {
			t.Errorf("adults=%d: preço = %v, esperado %v", tc.adults, dates[0].BestTotalPrice, tc.price)
		}
		if dates[0].Adults != tc.adults {
			t.Errorf("adults=%d: coluna = %d", tc.adults, dates[0].Adults)
		}
	}

	// Adults=0 é o modo diagnóstico: devolve as duas séries.
	all, err := pg.ListCalendar(ctx, CalendarFilter{
		Origin: "TST", Destination: "XXX", Market: "ZZ", TripType: "R", Limit: 100,
	})
	if err != nil {
		t.Fatalf("ListCalendar sem filtro: %v", err)
	}
	if len(all) != 2 {
		t.Errorf("sem filtro de adults: len = %d, esperado 2 (as duas séries)", len(all))
	}
}

// TestResumeRespectsAge: uma coleta antiga não conta como retomável.
//
// É o que evita que a segunda execução de `./run.sh calendar` seja um no-op
// permanente: a calendar_key não inclui data, então "existe" não implica "está
// atual", e preço de passagem muda todo dia.
func TestResumeRespectsAge(t *testing.T) {
	pg, _ := newStores(t)
	ctx := context.Background()

	key := models.CalendarKey{
		Origin: "TST", Destination: "XXX", CabinClass: "E",
		TripType: "R", Market: "ZZ", Adults: 1,
	}
	resp := &models.CalendarResponse{Data: models.CalendarData{
		BestPriceForDates: []models.BestPriceForDate{{
			DepartureAirport: "TST", ArrivalAirport: "XXX",
			DepartureDate: "2026-09-01T00:00:00", InsertionDate: "2026-08-03T10:00:00",
			BestTotalPrice: 487.21, Currency: "EUR",
			CabinClass: "E", TripType: "R", Market: "ZZ",
		}},
	}}

	// Coleta de 48 h atrás.
	old := time.Now().UTC().Add(-48 * time.Hour)
	if err := pg.SaveCalendar(ctx, key, resp, "raw", old); err != nil {
		t.Fatalf("SaveCalendar: %v", err)
	}

	for _, tc := range []struct {
		name      string
		notBefore time.Time
		want      bool
	}{
		{"sem corte aproveita qualquer idade", time.Time{}, true},
		{"corte de 24 h descarta a de 48 h", time.Now().UTC().Add(-24 * time.Hour), false},
		{"corte de 72 h aproveita a de 48 h", time.Now().UTC().Add(-72 * time.Hour), true},
	} {
		got, err := pg.CalendarExists(ctx, key.String(), tc.notBefore)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if got != tc.want {
			t.Errorf("%s: CalendarExists = %v, esperado %v", tc.name, got, tc.want)
		}
	}
}

// TestRawAbsentIsNotFound: o bruto ausente é ErrNotFound, que a API mapeia para
// 404. Com TTL de 7 dias e a linha tratada sem expiração, é o caso normal depois
// de uma semana — não uma falha do Redis, que seria 500.
func TestRawAbsentIsNotFound(t *testing.T) {
	_, rdb := newStores(t)
	ctx := context.Background()

	if _, err := rdb.LatestRaw(ctx, "calendar:TST:XXX:E:R:ZZ:1:inexistente"); !errors.Is(err, ErrNotFound) {
		t.Errorf("LatestRaw: err = %v, esperado ErrNotFound", err)
	}
	if _, err := rdb.LoadRaw(ctx, "tap:raw:TST:XXX:inexistente"); !errors.Is(err, ErrNotFound) {
		t.Errorf("LoadRaw: err = %v, esperado ErrNotFound", err)
	}
}

// TestReturnsDerivesNights confirma o cálculo de noites feito na gravação.
func TestReturnsDerivesNights(t *testing.T) {
	pg, _ := newStores(t)
	ctx := context.Background()

	key := models.ReturnsKey{
		Origin: "TST", Destination: "XXX", DepartDate: "01092026",
		CabinClass: "E", Market: "ZZ", Adults: 1,
	}

	resp := &models.CalendarReturnsResponse{Data: models.CalendarReturnsData{
		Origin: "TST", Destination: "YYY", Currency: "EUR", TripType: "R", DirectFlight: true,
		Returns: []models.ReturnPrice{
			{ReturnDate: "2026-09-19T00:00:00", Price: 605.92},
			{ReturnDate: "2026-09-21T00:00:00", Price: 520.92},
		},
	}}

	if err := pg.SaveReturns(ctx, key, resp, "raw", time.Now().UTC()); err != nil {
		t.Fatalf("SaveReturns: %v", err)
	}

	combos, err := pg.ListReturns(ctx, ReturnsFilter{
		Origin: "TST", Destination: "XXX", Market: "ZZ", Limit: 100,
	})
	if err != nil {
		t.Fatalf("ListReturns: %v", err)
	}
	if len(combos) != 2 {
		t.Fatalf("len(combos) = %d, esperado 2", len(combos))
	}

	// Ordenado por preço: 21/09 (20 noites, 520.92) primeiro.
	if combos[0].Nights != 20 {
		t.Errorf("nights = %d, esperado 20 (01/09 -> 21/09)", combos[0].Nights)
	}
	if combos[1].Nights != 18 {
		t.Errorf("nights = %d, esperado 18 (01/09 -> 19/09)", combos[1].Nights)
	}
	if combos[0].ResolvedDest == nil || *combos[0].ResolvedDest != "YYY" {
		t.Errorf("resolvedDest = %v, esperado YYY", combos[0].ResolvedDest)
	}

	// O filtro por duração é o eixo de análise da matriz. As noites existentes
	// são 18 (01/09 -> 19/09) e 20 (01/09 -> 21/09).
	filtered, err := pg.ListReturns(ctx, ReturnsFilter{
		Origin: "TST", Destination: "XXX", Market: "ZZ",
		MinNights: ptr(18), MaxNights: ptr(18), Limit: 100,
	})
	if err != nil {
		t.Fatalf("ListReturns filtrado: %v", err)
	}
	if len(filtered) != 1 || filtered[0].Nights != 18 {
		t.Errorf("filtro por 18 noites devolveu %d itens: %+v", len(filtered), filtered)
	}

	// E um intervalo sem combinação devolve vazio, não erro.
	empty, err := pg.ListReturns(ctx, ReturnsFilter{
		Origin: "TST", Destination: "XXX", Market: "ZZ",
		MinNights: ptr(19), MaxNights: ptr(19), Limit: 100,
	})
	if err != nil {
		t.Fatalf("ListReturns intervalo vazio: %v", err)
	}
	if len(empty) != 0 {
		t.Errorf("filtro por 19 noites devolveu %d itens, esperado 0", len(empty))
	}
}

// TestRawRoundTrip cobre a gravação, o índice e a leitura da resposta bruta.
func TestRawRoundTrip(t *testing.T) {
	_, rdb := newStores(t)
	ctx := context.Background()

	const key = "test:raw:roundtrip"
	payload := []byte(`{"status":"200","data":{"x":1}}`)

	rawKey, err := rdb.SaveRaw(ctx, key, time.Now().UTC(), payload)
	if err != nil {
		t.Fatalf("SaveRaw: %v", err)
	}

	got, err := rdb.LoadRaw(ctx, rawKey)
	if err != nil {
		t.Fatalf("LoadRaw: %v", err)
	}
	if string(got) != string(payload) {
		t.Errorf("LoadRaw = %q", got)
	}

	// LatestRaw usa o índice ordenado por tempo.
	latest, err := rdb.LatestRaw(ctx, key)
	if err != nil {
		t.Fatalf("LatestRaw: %v", err)
	}
	if string(latest) != string(payload) {
		t.Errorf("LatestRaw = %q", latest)
	}

	if _, err := rdb.LatestRaw(ctx, "test:raw:inexistente"); err == nil {
		t.Error("LatestRaw de chave inexistente não devolveu erro")
	}
}

// TestDictionariesUpsert: os dicionários vêm em toda resposta e não podem
// duplicar.
func TestDictionariesUpsert(t *testing.T) {
	pg, _ := newStores(t)
	ctx := context.Background()

	key := models.CalendarKey{
		Origin: "TST", Destination: "XXX", CabinClass: "E",
		TripType: "O", Market: "ZZ", Adults: 1,
	}
	resp := &models.CalendarResponse{
		Data: models.CalendarData{BestPriceForDates: []models.BestPriceForDate{{
			DepartureAirport: "TST", ArrivalAirport: "XXX",
			DepartureDate: "2026-09-01T00:00:00", InsertionDate: "2026-08-03T10:00:00",
			BestTotalPrice: 1, Currency: "EUR", CabinClass: "E", TripType: "O", Market: "ZZ",
		}}},
		Translate: models.Translate{
			Locations: map[string]models.Location{
				"TST": {Code: "TST", Name: "Teste", City: "Cidade", Country: "País", CountryCode: "ZZ"},
			},
			Airlines: map[string]models.Airline{"TP": {Code: "TP", Name: "TAP Air Portugal"}},
		},
	}

	for range 2 {
		if err := pg.SaveCalendar(ctx, key, resp, "raw", time.Now().UTC()); err != nil {
			t.Fatalf("SaveCalendar: %v", err)
		}
	}
	// Uma chave primária duplicada faria a transação falhar; chegar aqui já prova
	// o upsert.
}

func ptr[T any](v T) *T { return &v }
