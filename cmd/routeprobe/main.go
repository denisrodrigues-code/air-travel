// Command routeprobe mede cada perfil TLS registrado contra as rotas da TAP que
// NÃO discriminam por motor de navegador: session/create, calendar e
// calendarReturns.
//
// Existe porque o cmd/wafprobe só mede /booking/availability/search, e aquela
// rota tem duas defesas empilhadas (§4 do CLAUDE.md): recusa permanente de
// Chromium e um bloqueio temporário por VOLUME que atinge todos os motores.
// Dentro da janela de volume o wafprobe recusa-se a medir — corretamente, já que
// a tabela não valeria nada. Só que "não posso medir a search agora" não é o
// mesmo que "não posso medir nada": as rotas de calendário respondem 200 para
// Chromium e Gecko, e são as que a coleta de fato usa (modos calendar/returns).
//
// O que esta ferramenta responde, e o teste offline não pode:
//
//   - o handshake TLS do perfil funciona de verdade na rede;
//   - a identidade HTTP montada para ele é aceita pelo servidor;
//   - em particular, se os perfis herdados (Opera) realmente negociam, já que o
//     spec deles só sai pela tabela interna do utls e não por SpecFactory.
//
// Como o wafprobe, usa o MESMO adapter da aplicação (internal/tap) via
// platform.BootstrapAdapter, e não uma reimplementação parecida: uma segunda
// cópia faria a sondagem medir uma coisa e a aplicação enviar outra.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"airtravel/internal/client"
	"airtravel/internal/collect"
	"airtravel/internal/config"
	"airtravel/internal/models"
	"airtravel/internal/platform"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	var (
		proxyURL = flag.String("proxy", "", "proxy HTTP para inspeção")
		origin   = flag.String("origin", "LIS", "origem")
		dest     = flag.String("destination", "RIO", "destino")
		date     = flag.String("date", "01092026", "data de partida em DDMMYYYY")
		market   = flag.String("market", "PT", "mercado")
		language = flag.String("language", "pt", "idioma")
		cabin    = flag.String("cabin", "E", "cabine: E, W ou C")
		only     = flag.String("profiles", "", "subconjunto de perfis, separados por vírgula")
		routes   = flag.String("routes", "calendar", "rotas a medir: calendar, returns")
		delay    = flag.Duration("delay", 3*time.Second, "intervalo entre perfis")
		rps      = flag.Float64("rps", 2, "requisições por segundo dentro de uma tentativa")
		verbose  = flag.Bool("verbose", false, "mostrar o log do adapter")
	)
	flag.Parse()

	profiles := client.ProfileNames()
	if *only != "" {
		profiles = splitList(*only)
	}
	if len(profiles) == 0 {
		return errors.New("nenhum perfil a sondar")
	}

	wanted := splitList(*routes)
	if len(wanted) == 0 {
		return errors.New("nenhuma rota a medir")
	}
	for _, r := range wanted {
		if r != "calendar" && r != "returns" {
			return fmt.Errorf("rota desconhecida %q: use calendar ou returns", r)
		}
	}

	log := slog.New(slog.DiscardHandler)
	if *verbose {
		log = slog.New(slog.NewTextHandler(os.Stderr, nil))
	}

	params := models.SearchParams{
		Origin: strings.ToUpper(*origin), Destination: strings.ToUpper(*dest),
		DepartDate: *date, Adults: 1, CabinClass: strings.ToUpper(*cabin),
	}
	if err := params.Validate(); err != nil {
		return fmt.Errorf("parâmetros inválidos: %w", err)
	}

	ctx := context.Background()
	opts := attemptOptions{
		proxyURL: *proxyURL, market: *market, language: *language, rps: *rps,
	}

	fmt.Printf("%s->%s %s · mercado %s · %d perfil(is) × %d rota(s)\n\n",
		params.Origin, params.Destination, params.DepartDate, *market,
		len(profiles), len(wanted))

	fmt.Printf("%-18s %-10s %s\n", "PERFIL", "MOTOR", "RESULTADO POR ROTA")
	fmt.Printf("%-18s %-10s %s\n", "------", "-----", "------------------")

	var okCount, failCount int

	for i, profile := range profiles {
		if i > 0 {
			time.Sleep(*delay)
		}

		engine, results := attempt(ctx, log, profile, params, wanted, opts)
		fmt.Printf("%-18s %-10s %s\n", profile, engine, strings.Join(results.texts, " · "))

		if results.allOK {
			okCount++
		} else {
			failCount++
		}
	}

	fmt.Println()
	fmt.Printf("perfis medidos:   %d\n", len(profiles))
	fmt.Printf("todas as rotas OK: %d\n", okCount)
	fmt.Printf("com falha:         %d\n", failCount)

	if failCount > 0 {
		return errors.New("\nhouve perfil com falha — ver a tabela acima")
	}
	return nil
}

type attemptOptions struct {
	proxyURL string
	market   string
	language string
	rps      float64
}

type results struct {
	allOK bool
	texts []string
}

// attempt monta a aplicação real com o perfil informado e exercita as rotas
// pedidas, na ordem.
//
// A primeira rota carrega também o custo do session/create, já que o adapter
// obtém o JWT por demanda: um perfil cujo handshake não funcione falha ali, o
// que é informação suficiente — não há por que insistir nas rotas seguintes.
func attempt(ctx context.Context, log *slog.Logger, profile string, params models.SearchParams, routes []string, opts attemptOptions) (string, results) {
	cfg := config.Default()
	cfg.TLSProfile = profile
	cfg.ProxyURL = opts.proxyURL
	cfg.Market = strings.ToUpper(opts.market)
	cfg.Language = opts.language
	cfg.RequestsPerSecond = opts.rps
	// Desligada pela mesma razão do wafprobe: com rotação, um 403 faria o adapter
	// trocar de perfil e a tentativa sairia como aprovada — mediríamos o pool.
	cfg.Rotate = false

	dep, err := platform.BootstrapAdapter(cfg, log)
	if err != nil {
		return "?", results{texts: []string{"erro ao montar: " + firstLine(err.Error())}}
	}

	out := results{allOK: true}
	for _, route := range routes {
		text, ok := probeRoute(ctx, dep, route, params)
		out.texts = append(out.texts, text)
		if !ok {
			out.allOK = false
			// Sem sentido medir a rota seguinte com um perfil que já falhou: o
			// erro seria o mesmo e a requisição, desperdiçada.
			break
		}
	}
	return dep.Engine.Name, out
}

func probeRoute(ctx context.Context, dep *platform.Adapter, route string, params models.SearchParams) (string, bool) {
	start := time.Now()

	switch route {
	case "calendar":
		resp, raw, err := dep.Scraper.Calendar(ctx, params)
		elapsed := time.Since(start).Milliseconds()
		if err != nil {
			return "calendar " + classify(err, elapsed), false
		}

		dates := resp.Data.BestPriceForDates
		bookable, cheapest, currency := summarize(dates)
		return fmt.Sprintf("calendar OK %d datas · %d com voo · menor %.2f %s · %d KB · %d ms",
			len(dates), bookable, cheapest, currency, len(raw)/1024, elapsed), len(dates) > 0

	case "returns":
		resp, raw, err := dep.Scraper.CalendarReturns(ctx, params)
		elapsed := time.Since(start).Milliseconds()
		if err != nil {
			return "returns " + classify(err, elapsed), false
		}

		rets := resp.Data.Returns
		var bookable int
		cheapest := 0.0
		for i := range rets {
			if !rets[i].Bookable() {
				continue
			}
			bookable++
			if p := rets[i].Price; cheapest == 0 || p < cheapest {
				cheapest = p
			}
		}
		return fmt.Sprintf("returns OK %d voltas · %d com voo · menor %.2f %s · %d KB · %d ms",
			len(rets), bookable, cheapest, resp.Data.Currency, len(raw)/1024, elapsed), len(rets) > 0
	}

	return "rota desconhecida", false
}

// summarize extrai o que resume um calendário: quantas datas são
// comercializáveis e qual a mais barata.
//
// Filtra por Bookable porque as datas sem voo vêm com preço 0 — incluí-las faria
// o "menor preço" ser sempre zero. É a mesma armadilha documentada no §5.
func summarize(dates []models.BestPriceForDate) (bookable int, cheapest float64, currency string) {
	for i := range dates {
		if !dates[i].Bookable() {
			continue
		}
		bookable++
		if currency == "" {
			currency = dates[i].Currency
		}
		if p := dates[i].BestTotalPrice; cheapest == 0 || p < cheapest {
			cheapest = p
		}
	}
	return bookable, cheapest, currency
}

// classify separa recusa do provedor de falha de outra natureza, porque as ações
// são opostas: bloqueio pede trocar de identidade ou esperar, o resto pede olhar
// o erro.
func classify(err error, elapsed int64) string {
	if errors.Is(err, collect.ErrBlocked) {
		return fmt.Sprintf("BLOQUEADO WAF · %d ms", elapsed)
	}
	return "FALHOU " + firstLine(err.Error())
}

func splitList(value string) []string {
	parts := strings.Split(value, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	const limit = 70
	if len(s) > limit {
		return s[:limit] + "..."
	}
	return s
}
