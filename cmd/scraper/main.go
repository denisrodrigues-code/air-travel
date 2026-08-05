// Command scraper coleta disponibilidade e tarifas da TAP (booking.flytap.com),
// persistindo os dados tratados no PostgreSQL e as respostas brutas no Redis.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"airtravel/internal/collect"
	"airtravel/internal/config"
	"airtravel/internal/models"
	"airtravel/internal/platform"
	"airtravel/internal/report"
)

func main() {
	if err := run(); err != nil {
		slog.Error("execução falhou", "err", err)
		os.Exit(1)
	}
}

// options reúne as flags da linha de comando.
type options struct {
	origins      string
	destinations string
	startDate    string
	days         int
	cabins       string
	adults       int
	tripDuration int
	cookiesFile  string
	proxyURL     string
	tlsProfile   string
	rotate       bool
	market       string
	language     string
	concurrency  int
	rps          float64
	resume       bool
	resumeMaxAge time.Duration
	topN         int
	debug        bool
	mode         string
	from         string
	to           string
	tripType     string
}

func parseFlags() *options {
	o := &options{}
	flag.StringVar(&o.origins, "origins", "LIS", "origens separadas por vírgula (ex.: LIS,OPO)")
	flag.StringVar(&o.destinations, "destinations", "RIO", "destinos separados por vírgula (ex.: RIO,GRU)")
	flag.StringVar(&o.startDate, "start", "", "primeira data de partida em DD-MM-AAAA (padrão: hoje + 30 dias)")
	flag.IntVar(&o.days, "days", 1, "quantidade de datas consecutivas a consultar")
	flag.StringVar(&o.cabins, "cabins", "E", "cabines separadas por vírgula: E economy, W premium, C executiva")
	flag.IntVar(&o.adults, "adults", 1, "número de adultos")
	flag.IntVar(&o.tripDuration, "trip-duration", 0, "noites de permanência; 0 gera somente ida")
	flag.StringVar(&o.cookiesFile, "cookies", "cookies.txt", "arquivo com cookies do navegador (nome=valor por linha)")
	// Vazio por padrão. O padrão anterior era o proxy do powhttp
	// (http://localhost:8888), o que fazia qualquer invocação direta falhar em
	// máquina sem ele de pé — inclusive os exemplos da documentação. Para
	// inspecionar o tráfego, passe -proxy ou exporte PROXY.
	flag.StringVar(&o.proxyURL, "proxy", envOr("PROXY", ""),
		"proxy HTTP, ex.: http://localhost:8888 para o powhttp; vazio desativa")
	flag.StringVar(&o.tlsProfile, "tls-profile", config.DefaultTLSProfile, "perfil TLS preferido")
	flag.BoolVar(&o.rotate, "rotate", true, "trocar de fingerprint quando o WAF recusar")
	flag.StringVar(&o.market, "market", "PT", "mercado (define a moeda)")
	flag.StringVar(&o.language, "language", "pt", "idioma dos textos da resposta")
	flag.IntVar(&o.concurrency, "concurrency", 3, "buscas simultâneas")
	flag.Float64Var(&o.rps, "rps", 0.5, "requisições por segundo")
	flag.BoolVar(&o.resume, "resume", true, "ignorar buscas já persistidas e ainda recentes")
	flag.DurationVar(&o.resumeMaxAge, "resume-max-age", config.Default().ResumeMaxAge,
		"idade máxima do que a retomada aproveita; 0 aproveita qualquer coleta, por antiga que seja")
	flag.IntVar(&o.topN, "top", 20, "quantas ofertas exibir na tabela final; 0 exibe todas")
	flag.BoolVar(&o.debug, "debug", false, "log em nível debug")
	flag.StringVar(&o.tripType, "trip-type", "",
		"O só ida, R ida e volta; vazio deriva de -trip-duration. "+
			"No modo calendar, R devolve a tarifa de ida e volta (mais barata)")
	flag.StringVar(&o.from, "from", "", "modo calendar: recorta a saída a partir desta data (DD-MM-AAAA)")
	flag.StringVar(&o.to, "to", "", "modo calendar: recorta a saída até esta data (DD-MM-AAAA)")
	flag.StringVar(&o.mode, "mode", "calendar",
		"o que coletar: 'calendar' (melhor preço por data de partida), "+
			"'returns' (matriz ida x volta: preço total por data de retorno) ou "+
			"'search' (voos e tarifas; bloqueada pelo WAF, ver CLAUDE.md)")
	flag.Parse()
	return o
}

func run() error {
	opts := parseFlags()

	level := slog.LevelInfo
	if opts.debug {
		level = slog.LevelDebug
	}
	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: level}))

	// Ctrl+C e SIGTERM encerram a coleta de forma ordenada.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cfg, err := buildConfig(opts)
	if err != nil {
		return err
	}

	plan, err := buildPlan(opts)
	if err != nil {
		return err
	}
	jobs := plan.Expand()
	if len(jobs) == 0 {
		return errors.New("o plano não gerou nenhuma busca; verifique origens e destinos")
	}
	logger.InfoContext(ctx, "plano montado", "buscas", len(jobs))

	deps, closeAll, err := platform.Bootstrap(ctx, cfg, logger, platform.Options{
		CookiesFile: opts.cookiesFile,
		// Só a busca completa é protegida pelo WAF; os demais modos dispensam
		// cookies. Ainda assim nenhum deles os exige hoje — ver CLAUDE.md §4.
		RequireClearance: false,
	})
	if err != nil {
		return err
	}
	defer closeAll()

	if err := deps.Adapter.Authenticate(ctx); err != nil {
		return fmt.Errorf("failed initial authentication: %w", err)
	}

	runner := collect.NewRunner(deps.Collect, logger, cfg.Concurrency, opts.resume)

	switch opts.mode {
	case "calendar":
		// Uma consulta por rota devolve um ano de preços, então as datas do
		// plano são irrelevantes: basta uma por rota/cabine.
		summary, err := runner.RunCalendar(ctx, collect.DedupeRoutes(jobs))
		if err != nil {
			logger.WarnContext(ctx, "execução encerrada antes do fim", "err", err)
		}
		window, err := parseWindow(opts)
		if err != nil {
			return err
		}
		return printCalendar(summary, opts.topN, window)

	case "returns":
		window, err := parseWindow(opts)
		if err != nil {
			return err
		}
		summary, err := runner.RunReturns(ctx, jobs)
		if err != nil {
			logger.WarnContext(ctx, "execução encerrada antes do fim", "err", err)
		}
		return printReturns(summary, opts.topN, window)

	case "search":
		summary, err := runner.RunSearches(ctx, jobs)
		if err != nil {
			logger.WarnContext(ctx, "execução encerrada antes do fim", "err", err)
		}
		return printResults(summary, opts.topN)

	default:
		return fmt.Errorf("modo %q inválido: use 'calendar', 'returns' ou 'search'", opts.mode)
	}
}

// buildConfig monta a configuração a partir das flags.
//
// A leitura de cookies e a validação ficam no platform.Bootstrap, que é quem
// conhece a ordem correta de montagem.
func buildConfig(opts *options) (*config.Config, error) {
	cfg := config.Default()
	cfg.ProxyURL = opts.proxyURL
	cfg.TLSProfile = opts.tlsProfile
	cfg.Rotate = opts.rotate
	cfg.Market = strings.ToUpper(opts.market)
	cfg.Language = opts.language
	cfg.Concurrency = opts.concurrency
	cfg.RequestsPerSecond = opts.rps
	if opts.resumeMaxAge < 0 {
		return nil, fmt.Errorf("-resume-max-age não pode ser negativo, obtido %s", opts.resumeMaxAge)
	}
	cfg.ResumeMaxAge = opts.resumeMaxAge
	return cfg, nil
}

// buildPlan traduz as flags no plano de buscas.
func buildPlan(opts *options) (collect.Plan, error) {
	start := time.Now().AddDate(0, 0, 30)
	if opts.startDate != "" {
		parsed, err := time.Parse("02-01-2006", opts.startDate)
		if err != nil {
			return collect.Plan{}, fmt.Errorf("data inicial %q fora do formato DD-MM-AAAA: %w",
				opts.startDate, err)
		}
		start = parsed
	}

	return collect.Plan{
		Origins:      splitCodes(opts.origins),
		Destinations: splitCodes(opts.destinations),
		StartDate:    start,
		Days:         opts.days,
		Cabins:       splitCodes(opts.cabins),
		Adults:       opts.adults,
		TripDuration: opts.tripDuration,
		TripType:     strings.ToUpper(strings.TrimSpace(opts.tripType)),
	}, nil
}

// dateWindow recorta a saída. Zero significa sem limite.
type dateWindow struct {
	from time.Time
	to   time.Time
}

// parseWindow lê as flags -from/-to.
func parseWindow(opts *options) (dateWindow, error) {
	var w dateWindow

	for _, f := range []struct {
		raw   string
		name  string
		field *time.Time
	}{
		{opts.from, "-from", &w.from},
		{opts.to, "-to", &w.to},
	} {
		if f.raw == "" {
			continue
		}
		parsed, err := time.Parse("02-01-2006", f.raw)
		if err != nil {
			return w, fmt.Errorf("%s %q fora do formato DD-MM-AAAA: %w", f.name, f.raw, err)
		}
		*f.field = parsed
	}

	if !w.from.IsZero() && !w.to.IsZero() && w.to.Before(w.from) {
		return w, fmt.Errorf("-to (%s) é anterior a -from (%s)",
			w.to.Format("02-01-2006"), w.from.Format("02-01-2006"))
	}
	return w, nil
}

// bounds expande a janela para um intervalo utilizável.
func (w dateWindow) bounds() (time.Time, time.Time) {
	to := w.to
	if to.IsZero() {
		to = time.Date(9999, 12, 31, 0, 0, 0, 0, time.UTC)
	}
	return w.from, to
}

// active informa se há recorte a aplicar.
func (w dateWindow) active() bool { return !w.from.IsZero() || !w.to.IsZero() }

// summarize imprime o balanço e, quando nada foi coletado por já estar
// persistido, explica isso em vez de imprimir uma tabela vazia.
//
// Sem esta distinção, "nenhuma data disponível" sugeria ausência de voos quando o
// caso era o oposto.
func summarize(summary collect.Summary) (handled bool, err error) {
	if summary.Done == 0 && summary.Skipped > 0 {
		fmt.Printf("\nNada a coletar: %d de %d já estavam persistidos e recentes.\n"+
			"Para recoletar agora, use -resume=false; para mudar o que conta como recente,\n"+
			"-resume-max-age. Para consultar o que existe, ./run.sh queries\n",
			summary.Skipped, summary.Total)
		return true, printSummary(summary)
	}
	return false, nil
}

// printSummary escreve o balanço, incluindo os avisos de persistência.
func printSummary(summary collect.Summary) error {
	if err := report.PrintSummary(os.Stdout, summary.Total, summary.Done,
		summary.Skipped, summary.Failed, summary.Offers); err != nil {
		return err
	}
	if summary.Warnings > 0 {
		fmt.Printf("Atenção: %d aviso(s) de persistência — o dado foi capturado, "+
			"mas a gravação falhou em parte. Veja o log.\n", summary.Warnings)
	}
	return nil
}

// printResults exibe a tabela de voos e o balanço.
func printResults(summary collect.Summary, topN int) error {
	if handled, err := summarize(summary); handled {
		return err
	}

	var records []models.OfferRecord
	for _, job := range summary.Searches {
		if job.Err != nil || job.Result.Skipped {
			continue
		}
		records = append(records, job.Result.Offers()...)
	}

	if err := report.PrintOffers(os.Stdout, records, topN); err != nil {
		return err
	}
	return printSummary(summary)
}

// printCalendar exibe as datas mais baratas e o balanço.
func printCalendar(summary collect.Summary, topN int, window dateWindow) error {
	if handled, err := summarize(summary); handled {
		return err
	}

	var prices []models.BestPriceForDate
	for _, job := range summary.Calendar {
		if job.Err != nil || job.Result.Skipped {
			continue
		}
		prices = append(prices, job.Result.Bookable()...)
	}

	if window.active() {
		from, to := window.bounds()
		prices = models.InWindow(prices, from, to)
	}

	if err := report.PrintCalendar(os.Stdout, prices, topN); err != nil {
		return err
	}
	return printSummary(summary)
}

// printReturns exibe uma matriz por data de ida e o balanço.
func printReturns(summary collect.Summary, topN int, window dateWindow) error {
	if handled, err := summarize(summary); handled {
		return err
	}

	for _, job := range summary.Returns {
		if job.Err != nil || job.Result.Skipped || job.Result.Dates() == 0 {
			continue
		}

		departure, err := time.Parse(models.DateLayout, job.Params.DepartDate)
		if err != nil {
			return fmt.Errorf("data de ida %q inválida: %w", job.Params.DepartDate, err)
		}

		prices := job.Result.Bookable()
		if window.active() {
			from, to := window.bounds()
			prices = models.ReturnsInWindow(prices, from, to)
		}

		route := job.Params.Origin + "-" + job.Params.Destination
		if err := report.PrintReturns(os.Stdout, route, departure, prices, topN); err != nil {
			return err
		}
	}

	return printSummary(summary)
}

// envOr devolve a variável de ambiente ou o padrão informado. As flags que o
// run.sh parametriza leem do ambiente para que os dois caminhos concordem.
func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// splitCodes normaliza uma lista separada por vírgulas para maiúsculas.
func splitCodes(value string) []string {
	parts := strings.Split(value, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if trimmed := strings.ToUpper(strings.TrimSpace(part)); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}
