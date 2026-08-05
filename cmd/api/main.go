// Command api expõe a coleta e o histórico por HTTP, com OpenAPI em /docs.
//
// As mesmas rotas da TAP usadas pelo cmd/scraper, atendidas sob demanda:
// availability/search, availability/calendar e availability/calendarReturns.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"airtravel/internal/api"
	"airtravel/internal/config"
	"airtravel/internal/platform"
)

func main() {
	if err := run(); err != nil {
		slog.Error("execução falhou", "err", err)
		os.Exit(1)
	}
}

func run() error {
	var (
		addr        = flag.String("addr", envOr("API_ADDR", ":8080"), "endereço de escuta")
		market      = flag.String("market", envOr("MARKET", "PT"), "mercado: define moeda e tarifas")
		language    = flag.String("language", envOr("LANGUAGE", "pt"), "idioma dos textos")
		tlsProfile  = flag.String("tls-profile", envOr("TLS_PROFILE", config.DefaultTLSProfile), "perfil TLS preferido")
		rotate      = flag.Bool("rotate", true, "trocar de fingerprint quando o WAF recusar")
		proxyURL    = flag.String("proxy", envOr("PROXY", ""), "proxy HTTP; vazio desativa")
		cookiesFile = flag.String("cookies", envOr("COOKIES_FILE", "cookies.txt"), "arquivo de cookies (opcional)")
		rps         = flag.Float64("rps", 0.5, "requisições por segundo à TAP")
		timeout     = flag.Duration("timeout", 90*time.Second, "teto por requisição HTTP")
		debug       = flag.Bool("debug", false, "log em nível debug")
	)
	flag.Parse()

	level := slog.LevelInfo
	if *debug {
		level = slog.LevelDebug
	}
	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: level}))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cfg := config.Default()
	cfg.Market = strings.ToUpper(*market)
	cfg.Language = *language
	cfg.TLSProfile = *tlsProfile
	cfg.Rotate = *rotate
	cfg.ProxyURL = *proxyURL
	cfg.RequestsPerSecond = *rps

	deps, closeAll, err := platform.Bootstrap(ctx, cfg, logger, platform.Options{
		CookiesFile: *cookiesFile,
	})
	if err != nil {
		return err
	}
	defer closeAll()

	// A sessão é criada no boot para que a primeira requisição do cliente não
	// pague o custo do session/create. Falhar aqui não impede subir: os
	// endpoints renovam o token sozinhos.
	if err := deps.Adapter.Authenticate(ctx); err != nil {
		logger.WarnContext(ctx, "autenticação inicial falhou; será tentada por requisição", "err", err)
	}

	server, err := api.New(deps.Collect, deps.Postgres, deps.Redis, logger, api.Options{
		Addr:       *addr,
		Timeout:    *timeout,
		TLSProfile: cfg.TLSProfile,
		Engine:     deps.Engine.Name,
		// Com rotação ligada o perfil muda em tempo de execução; o Capture de cada
		// resposta precisa dizer qual foi usado de verdade.
		Fingerprint: func() (string, string) {
			return deps.Adapter.Profile(), deps.Adapter.Engine().Name
		},
	})
	if err != nil {
		return fmt.Errorf("failed to build server: %w", err)
	}

	if err := server.Run(ctx); err != nil {
		return fmt.Errorf("failed to run server: %w", err)
	}
	return nil
}

// envOr devolve a variável de ambiente ou o padrão informado.
func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
