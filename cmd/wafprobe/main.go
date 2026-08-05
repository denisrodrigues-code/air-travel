// Command wafprobe testa quais perfis TLS atravessam o WAF em
// /bfm/rest/booking/availability/search.
//
// Motivação: uma análise anterior concluiu que a rota era inalcançável porque o
// JA4 já era idêntico ao do Chrome real. Essa conclusão só considerou perfis
// Chromium. Com o tls-client, o User-Agent e os client hints são montados à mão,
// e uma combinação Chrome incoerente é mais fácil de detectar do que uma Gecko —
// que simplesmente não anuncia client hint nenhum. Ver CLAUDE.md §4.
//
// A sondagem usa o MESMO adapter que a aplicação (internal/tap), e não uma
// reimplementação parecida. É deliberado: uma ferramenta que decide qual perfil
// usar precisa medir exatamente o que a aplicação envia, senão deixaria de
// detectar justamente o tipo de divergência que existe para detectar.
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

	"airtravel/internal/collect"
	"airtravel/internal/config"
	"airtravel/internal/models"
	"airtravel/internal/platform"
)

// defaultProfiles cobre as três famílias de motor, começando pelas que passam.
var defaultProfiles = []string{
	"firefox_148", "firefox_147", "firefox_135",
	"safari_ios_18_5",
	"chrome_151", "chrome_146",
}

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
		delay    = flag.Duration("delay", 4*time.Second, "intervalo entre combinações")
		rps      = flag.Float64("rps", 2, "requisições por segundo dentro de uma tentativa")
		verbose  = flag.Bool("verbose", false, "mostrar o log do adapter")
		control  = flag.String("control", config.DefaultTLSProfile,
			"perfil de referência, medido antes e depois para atestar a janela; vazio desativa")
		force = flag.Bool("force", false,
			"medir mesmo com o controle bloqueado (a tabela resultante não vale nada)")
	)
	flag.Parse()

	profiles := defaultProfiles
	if *only != "" {
		profiles = splitList(*only)
	}
	if len(profiles) == 0 {
		return errors.New("nenhum perfil a sondar")
	}

	// Por padrão o log do adapter é descartado: a tabela é a saída útil.
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

	fmt.Printf("%s->%s %s · mercado %s · %d combinação(ões)\n\n",
		params.Origin, params.Destination, params.DepartDate, *market, len(profiles))

	// O controle vem ANTES de tudo, e é o que dá sentido à tabela.
	//
	// O WAF tem um bloqueio temporário por VOLUME que atinge todas as famílias de
	// motor. Medido dentro dessa janela, este comando reporta "todos bloqueados" — e
	// a leitura natural, "o perfil deixou de funcionar", está errada. Foi exatamente
	// o erro cometido em 2026-08-04. Um perfil de referência recusado significa que
	// nenhuma linha da tabela quer dizer nada. Ver CLAUDE.md §4.
	if *control != "" {
		fmt.Printf("controle (%s): ", *control)
		_, res := attempt(ctx, log, *control, params, opts)

		switch {
		case res.ok:
			fmt.Printf("OK — janela limpa, a tabela abaixo é interpretável\n\n")
		case *force:
			fmt.Printf("BLOQUEADO — seguindo por -force; A TABELA NÃO VALE NADA\n\n")
		default:
			fmt.Printf("BLOQUEADO\n")
			return fmt.Errorf(`
o perfil de referência %q foi recusado, então o WAF está recusando tudo agora —
provavelmente bloqueio por VOLUME, que é temporário e independe de fingerprint.

Medir nesta janela produziria "todos bloqueados" para qualquer perfil, o que se
confunde com "o perfil deixou de funcionar". Espere alguns minutos (na medição de
2026-08-04 a janela passou de 16 min) e remeça.

  -force        mede de qualquer forma
  -control ""   desativa esta checagem`, *control)
		}

		time.Sleep(*delay)
	}

	fmt.Printf("%-18s %-10s %s\n", "PERFIL", "MOTOR", "RESULTADO")
	fmt.Printf("%-18s %-10s %s\n", "------", "-----", "---------")

	var passed, blocked []string

	for i, profile := range profiles {
		if i > 0 {
			time.Sleep(*delay)
		}

		engine, res := attempt(ctx, log, profile, params, opts)
		fmt.Printf("%-18s %-10s %s\n", profile, engine, res.text)

		switch {
		case res.ok:
			passed = append(passed, profile)
		case res.blocked:
			blocked = append(blocked, profile)
		}
	}

	// O controle é remedido no fim: a janela pode ter fechado DURANTE a tabela, e aí
	// os últimos perfis foram julgados por um bloqueio que não é deles.
	var controlFellDuring bool
	if *control != "" && len(blocked) > 0 {
		time.Sleep(*delay)
		fmt.Printf("\ncontrole (%s), remedido: ", *control)
		_, res := attempt(ctx, log, *control, params, opts)
		if res.ok {
			fmt.Printf("OK — a janela continuou limpa do começo ao fim\n")
		} else {
			controlFellDuring = true
			fmt.Printf("BLOQUEADO — a janela FECHOU durante a medição\n")
		}
	}

	fmt.Println()
	fmt.Printf("combinações testadas:  %d\n", len(profiles))
	fmt.Printf("trouxeram voos:        %d  %s\n", len(passed), strings.Join(passed, ", "))
	fmt.Printf("bloqueadas pelo WAF:   %d  %s\n", len(blocked), strings.Join(blocked, ", "))

	if controlFellDuring {
		return errors.New(`
ATENÇÃO: o controle passou no início e falhou no fim, então o bloqueio por volume
começou no meio da medição. Os perfis reportados como bloqueados podem ter sido
julgados por isso, e não por fingerprint. Espere e remeça`)
	}

	if len(passed) == 0 {
		return errors.New("\nnenhuma combinação atravessou o WAF")
	}
	fmt.Printf("\nPara fixar a combinação aprovada:\n  PROFILE=%s ./run.sh search\n", passed[0])
	return nil
}

type attemptOptions struct {
	proxyURL string
	market   string
	language string
	rps      float64
}

type outcome struct {
	// ok indica que a busca trouxe voos — o critério de sucesso é este, não
	// "respondeu 200".
	ok bool
	// blocked distingue recusa do provedor de erro de montagem.
	blocked bool
	text    string
}

// attempt monta a aplicação real com o perfil informado e executa uma busca.
//
// Devolve também o nome do motor, para deixar visível na tabela que perfil e
// identidade HTTP andam juntos.
func attempt(ctx context.Context, log *slog.Logger, profile string, params models.SearchParams, opts attemptOptions) (string, outcome) {
	cfg := config.Default()
	cfg.TLSProfile = profile
	cfg.ProxyURL = opts.proxyURL
	cfg.Market = strings.ToUpper(opts.market)
	cfg.Language = opts.language
	cfg.RequestsPerSecond = opts.rps
	// A rotação fica DESLIGADA aqui, e isto é essencial: com ela, um 403 em
	// chrome_151 faria o adapter trocar para firefox_148 e a tentativa seria
	// reportada como aprovada — a ferramenta mediria o pool, não o perfil.
	cfg.Rotate = false

	// Uma função só monta o caminho até a TAP, compartilhada com a aplicação. Uma
	// segunda cópia aqui deixaria a sondagem medir uma coisa e a aplicação enviar
	// outra — o erro que esta ferramenta existe para detectar.
	dep, err := platform.BootstrapAdapter(cfg, log)
	if err != nil {
		return "?", outcome{text: "erro ao montar: " + firstLine(err.Error())}
	}
	engine := dep.Engine

	start := time.Now()
	resp, _, err := dep.Scraper.Search(ctx, params)
	elapsed := time.Since(start).Milliseconds()

	if err != nil {
		// A distinção importa: bloqueio pede trocar de perfil, os demais não.
		if errors.Is(err, collect.ErrBlocked) {
			return engine.Name, outcome{blocked: true, text: fmt.Sprintf("BLOQUEADO  WAF · %d ms", elapsed)}
		}
		return engine.Name, outcome{text: "FALHOU  " + firstLine(err.Error())}
	}

	flights := len(resp.Data.ListOutbound)
	offers := resp.Data.Offers.ListOffers

	cheapest := 0.0
	for i := range offers {
		if p := offers[i].TotalPrice.Price; p > 0 && (cheapest == 0 || p < cheapest) {
			cheapest = p
		}
	}

	return engine.Name, outcome{
		ok: flights > 0,
		text: fmt.Sprintf("OK  %d voos · %d ofertas · %.2f %s · %d ms",
			flights, len(offers), cheapest, resp.Data.Offers.Currency, elapsed),
	}
}

// splitList normaliza uma lista separada por vírgulas.
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

// firstLine encurta mensagens de erro longas para caber na tabela.
func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	const limit = 78
	if len(s) > limit {
		return s[:limit] + "..."
	}
	return s
}
