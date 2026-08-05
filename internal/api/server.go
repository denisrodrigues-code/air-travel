package api

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"
)

// Options parametriza o servidor.
type Options struct {
	Addr string
	// Timeout é o teto de uma requisição, incluindo a coleta na TAP — que leva
	// de 3 a 9 segundos.
	Timeout time.Duration
	// Metadados da captura, ecoados nas respostas para diagnóstico. O mercado
	// vem do Collector, não daqui: é ele que compõe as chaves.
	TLSProfile string
	Engine     string
	// Fingerprint, se informada, é consultada a cada resposta para dizer qual
	// combinação está em uso agora.
	//
	// Com rotação ligada, TLSProfile e Engine descrevem apenas o boot: depois de um
	// bloqueio o adapter troca de perfil, e um Capture que continuasse a citar o
	// perfil inicial estaria mentindo justamente no campo que existe para
	// responder "qual combinação capturou este preço".
	Fingerprint func() (profile, engine string)
}

// Server atende a API HTTP.
type Server struct {
	collector Collector
	reader    Reader
	rawReader RawReader
	log       *slog.Logger

	addr        string
	timeout     time.Duration
	tlsProfile  string
	engine      string
	fingerprint func() (profile, engine string)

	http *http.Server
}

// New monta o servidor com as portas informadas.
func New(collector Collector, reader Reader, rawReader RawReader, log *slog.Logger, opts Options) (*Server, error) {
	if collector == nil || reader == nil || rawReader == nil {
		return nil, errors.New("collector, reader e rawReader são obrigatórios")
	}
	if opts.Addr == "" {
		opts.Addr = ":8080"
	}
	if opts.Timeout <= 0 {
		opts.Timeout = 60 * time.Second
	}

	s := &Server{
		collector:   collector,
		reader:      reader,
		rawReader:   rawReader,
		log:         log,
		addr:        opts.Addr,
		timeout:     opts.Timeout,
		tlsProfile:  opts.TLSProfile,
		engine:      opts.Engine,
		fingerprint: opts.Fingerprint,
	}

	s.http = &http.Server{
		Addr:    s.addr,
		Handler: s.routes(),
		// ReadHeaderTimeout curto protege contra Slowloris; o teto por requisição
		// fica no middleware, para que a coleta lenta na TAP não seja cortada.
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       90 * time.Second,
	}
	return s, nil
}

// Addr devolve o endereço de escuta.
func (s *Server) Addr() string { return s.addr }

// route é uma rota registrada. A tabela existe para que a especificação
// OpenAPI não possa dessincronizar: TestSpecCoversAllRoutes percorre estas
// entradas e falha se alguma não constar de openapi.yaml.
type route struct {
	// Method e Path formam o padrão da stdlib.
	Method string
	Path   string
	// SpecPath é o caminho como aparece em openapi.yaml. Vazio significa que a
	// rota é intencionalmente não documentada (redirecionamentos, fallbacks).
	SpecPath string
	Handler  func(http.ResponseWriter, *http.Request)
}

// apiRoutes é a única fonte de verdade das rotas.
func (s *Server) apiRoutes() []route {
	return []route{
		// Coleta
		{"POST", "/api/v1/searches", "/api/v1/searches", s.postSearch},
		{"GET", "/api/v1/flights", "/api/v1/flights", s.getFlights},
		{"GET", "/api/v1/calendar", "/api/v1/calendar", s.getCalendar},
		{"GET", "/api/v1/returns", "/api/v1/returns", s.getReturns},

		// Histórico
		{"GET", "/api/v1/searches", "/api/v1/searches", s.listSearches},
		{"GET", "/api/v1/searches/{key}", "/api/v1/searches/{key}", s.getSearch},
		{"GET", "/api/v1/searches/{key}/raw", "/api/v1/searches/{key}/raw", s.getSearchRaw},

		// Saúde
		{"GET", "/health", "/health", s.health},
		{"GET", "/health/ready", "/health/ready", s.ready},

		// Documentação: servem a especificação, não são descritas por ela.
		{"GET", "/openapi.yaml", "", s.openAPISpec},
		{"GET", "/docs", "", s.swaggerUI},
	}
}

// routes monta o roteador a partir da tabela.
//
// Usa o padrão de rotas da stdlib (Go 1.22+): método e variáveis de caminho no
// próprio padrão, sem framework.
func (s *Server) routes() http.Handler {
	mux := http.NewServeMux()

	for _, r := range s.apiRoutes() {
		mux.HandleFunc(r.Method+" "+r.Path, r.Handler)
	}

	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/docs", http.StatusFound)
	})

	// Métodos não suportados em rotas conhecidas devolvem 405 com Allow.
	mux.HandleFunc("/api/v1/searches", func(w http.ResponseWriter, _ *http.Request) {
		methodNotAllowed(w, http.MethodGet, http.MethodPost)
	})

	return s.withMiddleware(mux)
}

// withMiddleware encadeia recuperação de panic, log e timeout.
func (s *Server) withMiddleware(next http.Handler) http.Handler {
	return s.recoverPanic(s.logRequests(s.withTimeout(next)))
}

// withTimeout impõe o teto por requisição.
func (s *Server) withTimeout(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), s.timeout)
		defer cancel()
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// statusRecorder captura o status para o log de acesso.
type statusRecorder struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (r *statusRecorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

func (r *statusRecorder) Write(b []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	n, err := r.ResponseWriter.Write(b)
	r.bytes += n
	return n, err
}

// logRequests registra um evento por requisição atendida.
func (s *Server) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w}

		next.ServeHTTP(rec, r)

		if rec.status == 0 {
			rec.status = http.StatusOK
		}
		s.log.InfoContext(r.Context(), "requisição atendida",
			"metodo", r.Method,
			"rota", r.URL.Path,
			"status", rec.status,
			"bytes", rec.bytes,
			"duracao_ms", time.Since(start).Milliseconds())
	})
}

// recoverPanic evita que um panic num handler derrube o servidor.
func (s *Server) recoverPanic(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				s.log.ErrorContext(r.Context(), "panic no handler",
					"valor", v, "rota", r.URL.Path)
				writeJSON(w, s.log, http.StatusInternalServerError, Problem{
					Status: http.StatusInternalServerError,
					Code:   "internal_error",
					Detail: "erro interno ao atender a requisição",
				})
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// Handler expõe o roteador, para testes sem abrir porta.
func (s *Server) Handler() http.Handler { return s.routes() }

// Run atende requisições até o contexto ser cancelado, então encerra de forma
// ordenada.
func (s *Server) Run(ctx context.Context) error {
	errc := make(chan error, 1)

	go func() {
		s.log.InfoContext(ctx, "API no ar",
			"addr", s.addr,
			"docs", "http://localhost"+s.addr+"/docs",
			"perfil_tls", s.tlsProfile,
			"motor", s.engine,
			"mercado", s.collector.Market())

		if err := s.http.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errc <- fmt.Errorf("failed to serve: %w", err)
			return
		}
		errc <- nil
	}()

	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
		s.log.Info("encerrando a API")

		shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()

		if err := s.http.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("failed to shut down: %w", err)
		}
		return nil
	}
}
