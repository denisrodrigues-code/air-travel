package collect

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/sourcegraph/conc/pool"

	"airtravel/internal/models"
)

// Collector é o que o Runner precisa de um serviço de coleta.
//
// A interface existe para que a orquestração seja testável com dublês: sem ela o
// Runner só funcionaria com um Service concreto, que por sua vez exige provedor e
// dois armazenamentos. O que se quer verificar aqui — que uma falha isolada não
// interrompe as demais — não depende de nada disso.
type Collector interface {
	Search(ctx context.Context, p models.SearchParams, resume bool) (SearchResult, error)
	Calendar(ctx context.Context, p models.SearchParams, resume bool) (CalendarResult, error)
	Returns(ctx context.Context, p models.SearchParams, resume bool) (ReturnsResult, error)
}

// Runner executa várias coletas concorrentemente.
//
// Depois da extração do Service, o runner é só orquestração: paralelismo,
// agregação de resultados e consolidação do resumo. A política de persistência
// não está aqui.
type Runner struct {
	svc Collector
	log *slog.Logger

	concurrency int
	resume      bool
}

// NewRunner monta o orquestrador.
func NewRunner(svc Collector, log *slog.Logger, concurrency int, resume bool) *Runner {
	return &Runner{
		svc:         svc,
		log:         log,
		concurrency: max(1, concurrency),
		resume:      resume,
	}
}

// Summary agrega o resultado de uma execução. Cada modo preenche sua fatia.
type Summary struct {
	Total    int
	Done     int
	Skipped  int
	Failed   int
	Offers   int
	Warnings int

	Searches []JobResult[SearchResult]
	Calendar []JobResult[CalendarResult]
	Returns  []JobResult[ReturnsResult]
}

// JobResult é o desfecho de um item, com o erro que lhe pertence.
//
// Genérico para que a agregação seja escrita uma vez, em vez de três.
type JobResult[T any] struct {
	Params models.SearchParams
	Result T
	Err    error
}

// runJobs executa os itens concorrentemente e devolve os desfechos na ordem de
// entrada. Uma falha isolada não interrompe as demais.
func runJobs[T any](
	ctx context.Context,
	concurrency int,
	jobs []models.SearchParams,
	run func(context.Context, models.SearchParams) (T, error),
) []JobResult[T] {
	p := pool.NewWithResults[JobResult[T]]().WithMaxGoroutines(concurrency)

	for _, job := range jobs {
		p.Go(func() JobResult[T] {
			result, err := run(ctx, job)
			return JobResult[T]{Params: job, Result: result, Err: err}
		})
	}
	return p.Wait()
}

// tally consolida o resumo a partir dos desfechos.
func tally[T any](total int, results []JobResult[T], skipped func(T) bool, offers func(T) int, warns func(T) int) Summary {
	s := Summary{Total: total}

	for _, r := range results {
		switch {
		case r.Err != nil:
			s.Failed++
		case skipped(r.Result):
			s.Skipped++
		default:
			s.Done++
			s.Offers += offers(r.Result)
			s.Warnings += warns(r.Result)
		}
	}
	return s
}

// ---------------------------------------------------------------------------
// Modos
// ---------------------------------------------------------------------------

// RunSearches coleta voos e tarifas para cada combinação do plano.
func (r *Runner) RunSearches(ctx context.Context, jobs []models.SearchParams) (Summary, error) {
	if len(jobs) == 0 {
		return Summary{}, errors.New("nenhuma busca a executar")
	}

	results := runJobs(ctx, r.concurrency, jobs,
		func(ctx context.Context, p models.SearchParams) (SearchResult, error) {
			res, err := r.svc.Search(ctx, p, r.resume)
			r.logOutcome(ctx, p, "busca", res.Skipped, err,
				"ofertas", res.OfferCount(), "voos", res.Flights(), "chave_bruta", res.RawKey)
			return res, err
		})

	summary := tally(len(jobs), results,
		func(r SearchResult) bool { return r.Skipped },
		func(r SearchResult) int { return r.OfferCount() },
		func(r SearchResult) int { return len(r.Warnings) })
	summary.Searches = results

	return summary, ctxErr(ctx)
}

// RunCalendar consulta o calendário de cada rota.
//
// Uma requisição cobre um ano, então as datas do plano são irrelevantes aqui —
// use DedupeRoutes antes de chamar.
func (r *Runner) RunCalendar(ctx context.Context, jobs []models.SearchParams) (Summary, error) {
	if len(jobs) == 0 {
		return Summary{}, errors.New("nenhuma rota a consultar")
	}

	results := runJobs(ctx, r.concurrency, jobs,
		func(ctx context.Context, p models.SearchParams) (CalendarResult, error) {
			res, err := r.svc.Calendar(ctx, p, r.resume)

			attrs := []any{"datas", res.Dates(), "com_voo", len(res.Bookable()), "chave_bruta", res.RawKey}
			if c := res.Cheapest(); c != nil {
				attrs = append(attrs, "menor_preco", c.BestTotalPrice,
					"moeda", c.Currency, "data_mais_barata", c.DepartureDate)
			}
			r.logOutcome(ctx, p, "calendário", res.Skipped, err, attrs...)
			return res, err
		})

	summary := tally(len(jobs), results,
		func(r CalendarResult) bool { return r.Skipped },
		func(r CalendarResult) int { return len(r.Bookable()) },
		func(r CalendarResult) int { return len(r.Warnings) })
	summary.Calendar = results

	return summary, ctxErr(ctx)
}

// RunReturns consulta a matriz ida × volta para cada data de ida do plano.
func (r *Runner) RunReturns(ctx context.Context, jobs []models.SearchParams) (Summary, error) {
	if len(jobs) == 0 {
		return Summary{}, errors.New("nenhuma data de ida a consultar")
	}

	results := runJobs(ctx, r.concurrency, jobs,
		func(ctx context.Context, p models.SearchParams) (ReturnsResult, error) {
			res, err := r.svc.Returns(ctx, p, r.resume)

			attrs := []any{"datas_retorno", res.Dates(), "com_voo", len(res.Bookable()),
				"destino_resolvido", res.ResolvedDestination(), "chave_bruta", res.RawKey}
			if c := res.Cheapest(); c != nil {
				attrs = append(attrs, "menor_total", c.Price, "melhor_volta", c.ReturnDate)
			}
			r.logOutcome(ctx, p, "matriz ida x volta", res.Skipped, err, attrs...)
			return res, err
		})

	summary := tally(len(jobs), results,
		func(r ReturnsResult) bool { return r.Skipped },
		func(r ReturnsResult) int { return len(r.Bookable()) },
		func(r ReturnsResult) int { return len(r.Warnings) })
	summary.Returns = results

	return summary, ctxErr(ctx)
}

// logOutcome registra um evento por item, com o contexto da rota.
func (r *Runner) logOutcome(ctx context.Context, p models.SearchParams, what string, skipped bool, err error, attrs ...any) {
	log := r.log.With(
		"origem", p.Origin,
		"destino", p.Destination,
		"data", p.DepartDate,
		"cabine", p.CabinClass,
		"tipo", p.EffectiveTripType(),
	)

	switch {
	case err != nil:
		log.ErrorContext(ctx, what+" falhou", "err", err)
	case skipped:
		log.DebugContext(ctx, what+" já persistido, ignorando")
	default:
		log.InfoContext(ctx, what+" persistido", attrs...)
	}
}

func ctxErr(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("execução interrompida: %w", err)
	}
	return nil
}
