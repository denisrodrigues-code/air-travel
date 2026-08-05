package collect

import (
	"context"
	"errors"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"airtravel/internal/models"
)

// fakeCollector permite programar o desfecho por rota, que é o que os testes de
// orquestração precisam.
type fakeCollector struct {
	// byDestination decide o desfecho a partir do destino do job.
	byDestination map[string]outcome
	calls         atomic.Int32
	// inFlight mede a concorrência efetiva.
	inFlight, maxInFlight atomic.Int32
}

type outcome struct {
	err      error
	skipped  bool
	warnings []string
	bookable int
	delay    time.Duration
}

func (f *fakeCollector) enter() func() {
	f.calls.Add(1)
	n := f.inFlight.Add(1)
	for {
		peak := f.maxInFlight.Load()
		if n <= peak || f.maxInFlight.CompareAndSwap(peak, n) {
			break
		}
	}
	return func() { f.inFlight.Add(-1) }
}

func (f *fakeCollector) plan(p models.SearchParams) outcome {
	o := f.byDestination[p.Destination]
	if o.delay > 0 {
		time.Sleep(o.delay)
	}
	return o
}

func (f *fakeCollector) Search(_ context.Context, p models.SearchParams, _ bool) (SearchResult, error) {
	defer f.enter()()
	o := f.plan(p)
	if o.err != nil {
		return SearchResult{}, o.err
	}
	offers := make([]models.Offer, o.bookable)
	for i := range offers {
		offers[i] = models.Offer{IDOffer: i + 1}
	}
	return SearchResult{
		Key: p.Key("PT"), Skipped: o.skipped, Warnings: o.warnings,
		Response: &models.SearchResponse{Data: models.SearchData{
			Offers: models.Offers{ListOffers: offers},
		}},
	}, nil
}

func (f *fakeCollector) Calendar(_ context.Context, p models.SearchParams, _ bool) (CalendarResult, error) {
	defer f.enter()()
	o := f.plan(p)
	if o.err != nil {
		return CalendarResult{}, o.err
	}
	dates := make([]models.BestPriceForDate, o.bookable)
	for i := range dates {
		dates[i] = models.BestPriceForDate{
			BestTotalPrice: float64(100 + i), Currency: "EUR",
			DepartureDate: "2026-09-01T00:00:00",
		}
	}
	return CalendarResult{
		Key: p.CalendarKeyFor("PT"), Skipped: o.skipped, Warnings: o.warnings,
		Response: &models.CalendarResponse{Data: models.CalendarData{BestPriceForDates: dates}},
	}, nil
}

func (f *fakeCollector) Returns(_ context.Context, p models.SearchParams, _ bool) (ReturnsResult, error) {
	defer f.enter()()
	o := f.plan(p)
	if o.err != nil {
		return ReturnsResult{}, o.err
	}
	rets := make([]models.ReturnPrice, o.bookable)
	for i := range rets {
		rets[i] = models.ReturnPrice{Price: float64(100 + i), ReturnDate: "2026-09-20T00:00:00"}
	}
	return ReturnsResult{
		Key: p.ReturnsKeyFor("PT"), Skipped: o.skipped, Warnings: o.warnings,
		Response: &models.CalendarReturnsResponse{Data: models.CalendarReturnsData{Returns: rets}},
	}, nil
}

func newRunner(t *testing.T, f *fakeCollector, concurrency int) *Runner {
	t.Helper()
	return NewRunner(f, slog.New(slog.DiscardHandler), concurrency, false)
}

func jobsFor(destinations ...string) []models.SearchParams {
	out := make([]models.SearchParams, 0, len(destinations))
	for _, d := range destinations {
		out = append(out, models.SearchParams{
			Origin: "LIS", Destination: d, DepartDate: "01092026",
			Adults: 1, CabinClass: "E",
		})
	}
	return out
}

// TestOneFailureDoesNotStopTheOthers é a razão de o Runner existir e de esta
// interface ter sido extraída.
//
// Uma rota bloqueada não pode abortar a coleta das demais: o dado das outras já
// custou requisição.
func TestOneFailureDoesNotStopTheOthers(t *testing.T) {
	boom := errors.New("WAF bloqueou")
	f := &fakeCollector{byDestination: map[string]outcome{
		"RIO": {bookable: 3},
		"GRU": {err: boom},
		"FOR": {bookable: 2},
		"REC": {skipped: true},
	}}

	summary, err := newRunner(t, f, 4).RunSearches(context.Background(), jobsFor("RIO", "GRU", "FOR", "REC"))
	if err != nil {
		t.Fatalf("err = %v, esperado nil: uma falha isolada não é erro da execução", err)
	}

	if got := int(f.calls.Load()); got != 4 {
		t.Errorf("%d chamadas, esperado 4: todas as rotas devem ser tentadas", got)
	}
	if summary.Total != 4 || summary.Done != 2 || summary.Failed != 1 || summary.Skipped != 1 {
		t.Errorf("resumo = total %d, done %d, failed %d, skipped %d; esperado 4/2/1/1",
			summary.Total, summary.Done, summary.Failed, summary.Skipped)
	}
	if summary.Offers != 5 {
		t.Errorf("Offers = %d, esperado 5 (3+2, sem contar a falha nem a ignorada)", summary.Offers)
	}

	// O erro pertence ao item, e o item preserva os seus parâmetros.
	var found bool
	for _, job := range summary.Searches {
		if job.Params.Destination == "GRU" {
			found = true
			if !errors.Is(job.Err, boom) {
				t.Errorf("erro do item GRU = %v, esperado %v", job.Err, boom)
			}
		} else if job.Err != nil {
			t.Errorf("item %s recebeu erro alheio: %v", job.Params.Destination, job.Err)
		}
	}
	if !found {
		t.Error("o item que falhou não aparece no resumo")
	}
}

// TestWarningsAreCounted: falha de persistência não é erro, mas precisa aparecer
// no balanço — senão passa despercebida.
func TestWarningsAreCounted(t *testing.T) {
	f := &fakeCollector{byDestination: map[string]outcome{
		"RIO": {bookable: 1, warnings: []string{"redis fora"}},
		"GRU": {bookable: 1, warnings: []string{"redis fora", "postgres fora"}},
		"FOR": {bookable: 1},
	}}

	summary, err := newRunner(t, f, 3).RunSearches(context.Background(), jobsFor("RIO", "GRU", "FOR"))
	if err != nil {
		t.Fatalf("erro inesperado: %v", err)
	}
	if summary.Done != 3 {
		t.Errorf("Done = %d, esperado 3: avisos não impedem conclusão", summary.Done)
	}
	if summary.Warnings != 3 {
		t.Errorf("Warnings = %d, esperado 3", summary.Warnings)
	}
}

// TestSkippedIsNotDoneNorFailed: a retomada é um terceiro estado, e confundi-lo
// com sucesso faria o resumo mentir.
func TestSkippedIsNotDoneNorFailed(t *testing.T) {
	f := &fakeCollector{byDestination: map[string]outcome{
		"RIO": {skipped: true},
		"GRU": {skipped: true},
	}}

	summary, err := newRunner(t, f, 2).RunSearches(context.Background(), jobsFor("RIO", "GRU"))
	if err != nil {
		t.Fatalf("erro inesperado: %v", err)
	}
	if summary.Done != 0 || summary.Failed != 0 || summary.Skipped != 2 {
		t.Errorf("resumo = done %d, failed %d, skipped %d; esperado 0/0/2",
			summary.Done, summary.Failed, summary.Skipped)
	}
	if summary.Offers != 0 {
		t.Errorf("Offers = %d; itens ignorados não trazem ofertas", summary.Offers)
	}
}

// TestConcurrencyIsBounded confirma que o limite é respeitado — a TAP cobra por
// consulta ao GDS e não se deve inundá-la.
func TestConcurrencyIsBounded(t *testing.T) {
	dests := []string{"RIO", "GRU", "FOR", "REC", "SSA", "CNF"}
	plan := map[string]outcome{}
	for _, d := range dests {
		plan[d] = outcome{bookable: 1, delay: 25 * time.Millisecond}
	}
	f := &fakeCollector{byDestination: plan}

	const limit = 2
	if _, err := newRunner(t, f, limit).RunCalendar(context.Background(), jobsFor(dests...)); err != nil {
		t.Fatalf("erro inesperado: %v", err)
	}

	if peak := int(f.maxInFlight.Load()); peak > limit {
		t.Errorf("concorrência máxima observada = %d, limite = %d", peak, limit)
	}
	if got := int(f.calls.Load()); got != len(dests) {
		t.Errorf("%d chamadas, esperado %d", got, len(dests))
	}
}

// TestConcurrencyNeverBelowOne: um limite zero ou negativo não pode travar a
// execução.
func TestConcurrencyNeverBelowOne(t *testing.T) {
	for _, c := range []int{0, -5} {
		f := &fakeCollector{byDestination: map[string]outcome{"RIO": {bookable: 1}}}

		summary, err := newRunner(t, f, c).RunSearches(context.Background(), jobsFor("RIO"))
		if err != nil {
			t.Fatalf("concorrência %d: erro %v", c, err)
		}
		if summary.Done != 1 {
			t.Errorf("concorrência %d: Done = %d, esperado 1", c, summary.Done)
		}
	}
}

// TestEmptyPlanIsError: um plano vazio é erro de uso, não execução silenciosa.
func TestEmptyPlanIsError(t *testing.T) {
	r := newRunner(t, &fakeCollector{}, 1)
	ctx := context.Background()

	if _, err := r.RunSearches(ctx, nil); err == nil {
		t.Error("RunSearches aceitou plano vazio")
	}
	if _, err := r.RunCalendar(ctx, nil); err == nil {
		t.Error("RunCalendar aceitou plano vazio")
	}
	if _, err := r.RunReturns(ctx, nil); err == nil {
		t.Error("RunReturns aceitou plano vazio")
	}
}

// TestCanceledContextIsReported: o resumo do que já foi coletado é devolvido, e o
// cancelamento é informado — descartar o parcial seria perder dado pago.
func TestCanceledContextIsReported(t *testing.T) {
	f := &fakeCollector{byDestination: map[string]outcome{"RIO": {bookable: 1}}}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	summary, err := newRunner(t, f, 1).RunSearches(ctx, jobsFor("RIO"))
	if err == nil {
		t.Error("cancelamento não foi reportado")
	}
	if summary.Total != 1 {
		t.Errorf("Total = %d; o resumo parcial deve ser devolvido junto com o erro", summary.Total)
	}
}

// TestEachModeFillsItsOwnSlice evita que um modo preencha a fatia de outro — o
// que faria a apresentação imprimir vazio.
func TestEachModeFillsItsOwnSlice(t *testing.T) {
	plan := map[string]outcome{"RIO": {bookable: 2}}
	ctx := context.Background()
	jobs := jobsFor("RIO")

	s, err := newRunner(t, &fakeCollector{byDestination: plan}, 1).RunSearches(ctx, jobs)
	if err != nil || len(s.Searches) != 1 || s.Calendar != nil || s.Returns != nil {
		t.Errorf("RunSearches: searches %d, calendar %v, returns %v", len(s.Searches), s.Calendar, s.Returns)
	}

	c, err := newRunner(t, &fakeCollector{byDestination: plan}, 1).RunCalendar(ctx, jobs)
	if err != nil || len(c.Calendar) != 1 || c.Searches != nil || c.Returns != nil {
		t.Errorf("RunCalendar: calendar %d, searches %v, returns %v", len(c.Calendar), c.Searches, c.Returns)
	}

	rr, err := newRunner(t, &fakeCollector{byDestination: plan}, 1).RunReturns(ctx, jobs)
	if err != nil || len(rr.Returns) != 1 || rr.Searches != nil || rr.Calendar != nil {
		t.Errorf("RunReturns: returns %d, searches %v, calendar %v", len(rr.Returns), rr.Searches, rr.Calendar)
	}
}

// TestResultHelpersTolerateNilResponse: os acessores são usados na apresentação,
// inclusive sobre itens que falharam e não têm resposta.
func TestResultHelpersTolerateNilResponse(t *testing.T) {
	var s SearchResult
	if s.Flights() != 0 || s.OfferCount() != 0 || s.Offers() != nil {
		t.Error("SearchResult com resposta nil não devolveu zero")
	}

	var c CalendarResult
	if c.Dates() != 0 || c.Bookable() != nil || c.Cheapest() != nil {
		t.Error("CalendarResult com resposta nil não devolveu zero")
	}

	var rr ReturnsResult
	if rr.Dates() != 0 || rr.Bookable() != nil || rr.Cheapest() != nil || rr.ResolvedDestination() != "" {
		t.Error("ReturnsResult com resposta nil não devolveu zero")
	}
}
