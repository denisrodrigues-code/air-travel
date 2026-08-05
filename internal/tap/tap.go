// Package tap é o adapter da API BFM do booking.flytap.com.
//
// Só conhece a TAP: autenticação, formato dos payloads, cabeçalhos por endpoint
// e transporte. A orquestração e a persistência ficam em internal/collect.
package tap

import (
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/bogdanfinn/fhttp/cookiejar"
	tls_client "github.com/bogdanfinn/tls-client"
	"golang.org/x/time/rate"

	"airtravel/internal/client"
	"airtravel/internal/config"
)

// doer é o mínimo que o adapter precisa de um cliente HTTP.
//
// Existe para tornar o transporte testável: com ela, retry, classificação de erro
// e ordem de cabeçalhos são verificáveis sem rede. O tls_client.HttpClient
// satisfaz a interface, então nada muda na montagem.
type doer = client.Doer

// rotator fornece a combinação a usar e recebe o aviso de que ela foi recusada.
//
// A interface existe para que o adapter não precise saber se há uma combinação ou
// seis: `fixed` atende o caso de uma, `client.Pool` o de várias.
type rotator interface {
	// Current devolve a combinação corrente. É chamada UMA vez por requisição, e
	// o resultado carrega motor e cliente juntos — ver client.Fingerprint.
	Current() *client.Fingerprint
	// Blocked reporta uma recusa e devolve se houve troca. false significa que não
	// há alternativa e o erro é terminal.
	Blocked(profile string, reason error) bool
}

// globalBlocker é o rotator que sabe distinguir bloqueio por volume de recusa de
// identidade. Implementado por client.Pool.
//
// Interface separada e consultada por type assertion de propósito: `fixed` não tem
// como responder isso — com uma combinação só não há como observar que o WAF está
// recusando várias — e obrigá-lo a um método sem sentido seria pior que a
// assertion.
type globalBlocker interface {
	GlobalBlockSuspected() bool
}

// fixed é o rotator de uma combinação só: nunca troca.
//
// É o comportamento anterior ao pool, preservado para quem não quer rotação e
// usado pelos testes.
type fixed struct{ fp *client.Fingerprint }

func (f fixed) Current() *client.Fingerprint { return f.fp }
func (f fixed) Blocked(string, error) bool   { return false }

// Scraper encapsula cliente, credenciais e estado de sessão.
type Scraper struct {
	cfg     *config.Config
	log     *slog.Logger
	limiter *rate.Limiter

	// fps fornece a combinação perfil TLS + identidade HTTP de cada requisição.
	//
	// Substituiu os campos `http` e `engine`: mantê-los separados permitiria que
	// uma rotação entre a montagem dos cabeçalhos e o envio combinasse o
	// User-Agent de um perfil com o ClientHello de outro.
	fps rotator

	// dt reproduz os identificadores de RUM do Dynatrace, estáveis por sessão.
	dt dynatraceSession

	mu       sync.RWMutex
	token    string
	tokenExp time.Time
}

// New monta o scraper com uma combinação só. Cliente e jar devem vir de
// client.New.
//
// Equivale a NewWithRotation com um pool de um elemento; existe porque é a forma
// mais simples de montar o adapter e a que os testes usam.
func New(cfg *config.Config, httpClient tls_client.HttpClient, jar *cookiejar.Jar, log *slog.Logger) (*Scraper, error) {
	engine, err := client.EngineFor(cfg.TLSProfile)
	if err != nil {
		return nil, fmt.Errorf("configuração inválida: %w", err)
	}

	return NewWithRotation(cfg, fixed{fp: &client.Fingerprint{
		Profile: cfg.TLSProfile, Engine: engine, Client: httpClient, Jar: jar,
	}}, log)
}

// NewWithRotation monta o scraper sobre um rotator, que pode trocar de combinação
// quando o WAF recusa.
func NewWithRotation(cfg *config.Config, fps rotator, log *slog.Logger) (*Scraper, error) {
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("configuração inválida: %w", err)
	}
	if fps == nil {
		return nil, fmt.Errorf("configuração inválida: rotator é obrigatório")
	}

	dt, err := newDynatraceSession()
	if err != nil {
		return nil, fmt.Errorf("failed to build dynatrace session ids: %w", err)
	}

	return &Scraper{
		cfg:     cfg,
		log:     log,
		limiter: rate.NewLimiter(rate.Limit(cfg.RequestsPerSecond), cfg.Burst),
		fps:     fps,
		dt:      dt,
	}, nil
}

// Profile devolve o perfil TLS em uso agora. Muda quando há rotação.
func (s *Scraper) Profile() string { return s.fps.Current().Profile }

// Engine devolve a identidade HTTP em uso agora.
func (s *Scraper) Engine() client.Engine { return s.fps.Current().Engine }
