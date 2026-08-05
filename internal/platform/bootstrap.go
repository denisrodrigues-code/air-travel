// Package platform monta a aplicação: resolve as dependências concretas e as
// entrega prontas.
//
// Existe porque os dois comandos repetiam os mesmos sete passos de montagem —
// cliente TLS, cookies, PostgreSQL, Redis, adapter, serviço — cada um com o seu
// próprio `defer`. Um passo esquecido num deles só apareceria em produção.
package platform

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"airtravel/internal/client"
	"airtravel/internal/collect"
	"airtravel/internal/config"
	"airtravel/internal/storage"
	"airtravel/internal/tap"
)

// Deps são as dependências prontas para uso.
type Deps struct {
	Cfg    *config.Config
	Engine client.Engine

	// Adapter é o provedor concreto. Exposto porque o CLI precisa autenticar
	// explicitamente no boot; o caso de uso é acessado por Collect.
	Adapter *tap.Scraper
	Collect *collect.Service

	Postgres *storage.Postgres
	Redis    *storage.Redis
}

// Adapter são as dependências suficientes para conversar com a TAP, sem banco
// nenhum.
type Adapter struct {
	// Engine é a identidade HTTP inicial. Com rotação ligada ela muda em tempo de
	// execução — use Scraper.Engine() para saber a atual.
	Engine  client.Engine
	Scraper *tap.Scraper
	// Pool é nil quando a rotação está desligada.
	Pool *client.Pool
}

// BootstrapAdapter monta apenas o caminho até a TAP: motor, cliente TLS, cookies
// e adapter.
//
// Existe para o `cmd/wafprobe`, que precisa enviar exatamente o que a aplicação
// envia mas não persiste nada. Sem isto ele repetia os quatro passos de montagem,
// e uma divergência entre as duas cópias faria a ferramenta medir uma combinação
// e a aplicação usar outra — justamente o tipo de erro que ela existe para
// detectar.
//
// Não devolve função de encerramento porque nada aqui a exige: o cliente TLS não
// tem recurso a fechar.
func BootstrapAdapter(cfg *config.Config, log *slog.Logger) (*Adapter, error) {
	if cfg == nil || log == nil {
		return nil, errors.New("cfg e log são obrigatórios")
	}
	// Validar aqui torna a função segura sozinha. O Bootstrap valida antes por
	// conta própria, para poder dar um erro melhor sobre cookies; validar duas
	// vezes é barato e puro.
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("configuração inválida: %w", err)
	}

	engine, err := client.EngineFor(cfg.TLSProfile)
	if err != nil {
		return nil, fmt.Errorf("perfil TLS inválido: %w", err)
	}

	if cfg.Rotate {
		pool, err := client.NewPool(client.PoolOptions{
			Profiles:       rotationOrder(cfg.TLSProfile),
			BaseURL:        cfg.BaseURL,
			Cookies:        cfg.Cookies,
			ProxyURL:       cfg.ProxyURL,
			TimeoutSeconds: int(cfg.Timeout.Seconds()),
			Cooldown:       cfg.RotationCooldown,
			Log:            log,
		})
		if err != nil {
			return nil, fmt.Errorf("failed to build fingerprint pool: %w", err)
		}

		scraper, err := tap.NewWithRotation(cfg, pool, log)
		if err != nil {
			return nil, fmt.Errorf("failed to build tap adapter: %w", err)
		}
		log.Debug("rotação de fingerprint ativa", "combinacoes", pool.Size(),
			"preferido", cfg.TLSProfile)

		return &Adapter{Engine: engine, Scraper: scraper, Pool: pool}, nil
	}

	httpClient, jar, err := client.New(client.Options{
		ProxyURL:       cfg.ProxyURL,
		ProfileName:    cfg.TLSProfile,
		TimeoutSeconds: int(cfg.Timeout.Seconds()),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to build http client: %w", err)
	}
	if err := client.SeedCookies(jar, cfg.BaseURL, cfg.Cookies); err != nil {
		return nil, fmt.Errorf("failed to seed cookies: %w", err)
	}

	scraper, err := tap.New(cfg, httpClient, jar, log)
	if err != nil {
		return nil, fmt.Errorf("failed to build tap adapter: %w", err)
	}

	return &Adapter{Engine: engine, Scraper: scraper}, nil
}

// rotationOrder põe o perfil preferido na frente e as alternativas medidas atrás.
//
// A duplicata é descartada pelo próprio pool, então passar o preferido que já
// consta de PassingProfiles não o repete.
func rotationOrder(preferred string) []string {
	return append([]string{preferred}, client.PassingProfiles...)
}

// Options parametriza a montagem.
type Options struct {
	// CookiesFile é opcional: as rotas usadas respondem sem cookies. A ausência
	// do arquivo não é erro.
	CookiesFile string
	// RequireClearance exige cf_clearance. Só faz sentido para o modo search.
	RequireClearance bool
}

// Bootstrap resolve as dependências na ordem correta.
//
// Devolve também a função de encerramento, que fecha na ordem inversa da
// abertura. Chame-a com defer: é o que garante que uma falha no meio da montagem
// não deixe conexão aberta.
func Bootstrap(ctx context.Context, cfg *config.Config, log *slog.Logger, opts Options) (*Deps, func(), error) {
	if cfg == nil || log == nil {
		return nil, nil, errors.New("cfg e log são obrigatórios")
	}

	// closers acumula o que já foi aberto, para que um erro tardio não vaze o
	// que foi aberto antes.
	var closers []func()
	closeAll := func() {
		for i := len(closers) - 1; i >= 0; i-- {
			closers[i]()
		}
	}
	fail := func(err error) (*Deps, func(), error) {
		closeAll()
		return nil, nil, err
	}

	if opts.CookiesFile != "" {
		if err := cfg.LoadCookiesFile(opts.CookiesFile); err != nil {
			if !errors.Is(err, config.ErrCookiesFileMissing) {
				return fail(fmt.Errorf("failed to load cookies: %w", err))
			}
			if opts.RequireClearance {
				return fail(fmt.Errorf("o modo search exige %s — copie cf_clearance de uma "+
					"sessão real do navegador (ver cookies.txt.example)", opts.CookiesFile))
			}
			log.Debug("seguindo sem cookies", "motivo", err)
		}
	}

	if err := cfg.Validate(); err != nil {
		return fail(fmt.Errorf("configuração inválida: %w", err))
	}
	if opts.RequireClearance {
		if err := cfg.RequireClearance(); err != nil {
			return fail(fmt.Errorf("%w — copie cf_clearance de uma sessão real do "+
				"navegador para %q", err, opts.CookiesFile))
		}
	}

	// O caminho até a TAP é o mesmo que o wafprobe monta — vive numa função só.
	adapter, err := BootstrapAdapter(cfg, log)
	if err != nil {
		return fail(err)
	}

	pg, err := storage.NewPostgres(ctx, cfg.PostgresDSN)
	if err != nil {
		return fail(fmt.Errorf("failed to open postgres: %w", err))
	}
	closers = append(closers, pg.Close)

	rdb, err := storage.NewRedis(ctx, cfg.RedisAddr, cfg.RedisPass, cfg.RedisDB, cfg.RawTTL)
	if err != nil {
		return fail(fmt.Errorf("failed to open redis: %w", err))
	}
	closers = append(closers, func() {
		if err := rdb.Close(); err != nil {
			log.Warn("falha ao fechar o Redis", "err", err)
		}
	})

	svc, err := collect.New(adapter.Scraper, pg, rdb, log, collect.Options{
		Market:       cfg.Market,
		ResumeMaxAge: cfg.ResumeMaxAge,
	})
	if err != nil {
		return fail(fmt.Errorf("failed to build collect service: %w", err))
	}

	return &Deps{
		Cfg:      cfg,
		Engine:   adapter.Engine,
		Adapter:  adapter.Scraper,
		Collect:  svc,
		Postgres: pg,
		Redis:    rdb,
	}, closeAll, nil
}
