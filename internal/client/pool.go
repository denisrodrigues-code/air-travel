package client

import (
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"time"

	http "github.com/bogdanfinn/fhttp"
	"github.com/bogdanfinn/fhttp/cookiejar"

	"airtravel/internal/config"
)

// Doer é o mínimo que uma requisição precisa do cliente HTTP.
//
// tls_client.HttpClient satisfaz a interface; declará-la aqui é o que permite ao
// adapter e aos testes trabalharem com dublês.
type Doer interface {
	Do(*http.Request) (*http.Response, error)
}

// Fingerprint é uma combinação COERENTE de perfil TLS e identidade HTTP.
//
// Engine e Client andam juntos de propósito. Rotacionar o perfil TLS sem rotacionar
// o User-Agent produziria exatamente a incoerência que `engine.go` existe para
// tornar impossível — cabeçalhos de Firefox sobre um ClientHello de Safari são mais
// fáceis de detectar do que qualquer um dos dois isolado.
type Fingerprint struct {
	Profile string
	Engine  Engine
	Client  Doer
	Jar     *cookiejar.Jar
}

// PassingProfiles são as combinações medidas como capazes de atravessar o WAF em
// booking/availability/search (ver `cmd/wafprobe` e CLAUDE.md §4).
//
// Os perfis Chromium ficam FORA de propósito: foram medidos como bloqueados nessa
// rota, então incluí-los na rotação gastaria uma requisição e um 403 para
// descobrir o que já se sabe.
var PassingProfiles = []string{
	"firefox_148",
	"firefox_147",
	"firefox_135",
	"safari_ios_18_5",
}

// DefaultCooldown é quanto tempo uma combinação bloqueada fica de fora.
//
// É uma ESCOLHA, não uma medição: não se sabe por quanto tempo o WAF da TAP
// lembra de um fingerprint recusado. Dez minutos é curto o bastante para a
// combinação voltar na mesma execução longa e longo o bastante para não insistir
// em algo que acabou de falhar. Ajuste com dados, se os tiver.
const DefaultCooldown = 10 * time.Minute

// Limiares da detecção de bloqueio global.
//
// O WAF da TAP tem DOIS bloqueios distintos na rota availability/search, e a
// diferença muda o que fazer:
//
//   - Por IDENTIDADE: permanente para a família Chromium, independente de volume.
//     Rotacionar resolve — é para isso que o pool existe.
//   - Por VOLUME: temporário, e atinge TODAS as famílias de motor ao mesmo tempo.
//     Rotacionar não resolve nada: queima as quatro combinações em segundos e
//     manda todas para o cooldown, deixando a coleta sem para onde ir por causa
//     de algo que passaria sozinho.
//
// Medido em 2026-08-04: depois de algumas dezenas de buscas do mesmo IP, os seis
// perfis passaram a responder 403 — Gecko e WebKit incluídos — com latência
// uniforme de ~1590 ms, contra ~4700 ms de uma busca que chega ao GDS. A janela
// durou mais de 16 minutos de espera dedicada.
//
// Os valores são ESCOLHA, não medição: três perfis distintos recusados dentro de
// um minuto é improvável de acontecer por identidade — o pool só tem combinações
// já medidas como aprovadas — e é o padrão típico do bloqueio por volume.
const (
	globalBlockThreshold = 3
	globalBlockWindow    = time.Minute
)

// PoolOptions parametriza o pool.
type PoolOptions struct {
	// Profiles é a ordem de preferência. O primeiro é o usado até ser bloqueado.
	Profiles []string
	// BaseURL e Cookies semeiam o jar de CADA combinação: cookies pertencem a uma
	// sessão, que por sua vez pertence a um fingerprint.
	BaseURL string
	Cookies []config.Cookie

	ProxyURL       string
	TimeoutSeconds int
	Cooldown       time.Duration
	Log            *slog.Logger
	// Now é injetável para tornar o cooldown testável sem esperar.
	Now func() time.Time
}

// entry é uma combinação e seu histórico de bloqueios.
type entry struct {
	fp *Fingerprint
	// until é quando a combinação volta a estar disponível.
	until time.Time
	// blocks conta os bloqueios acumulados, para alongar o cooldown de quem
	// falha repetidamente.
	blocks int
}

// Pool mantém várias combinações e troca de uma para outra quando o WAF recusa.
//
// É a diferença entre degradar e parar: com uma combinação só, um 403 em
// firefox_148 encerra a coleta. Com o pool, a coleta continua na combinação
// seguinte e a recusada volta depois do cooldown.
type Pool struct {
	mu       sync.Mutex
	entries  []*entry
	current  int
	cooldown time.Duration
	now      func() time.Time
	log      *slog.Logger

	// recent registra os bloqueios da última janela, para distinguir recusa por
	// identidade de bloqueio por volume. Guarda um instante por PERFIL, não um por
	// relato: numa coleta concorrente o mesmo perfil é reportado várias vezes, e
	// contar relatos faria oito goroutines parecerem oito perfis recusados.
	recent map[string]time.Time
	// globalUntil é até quando o pool considera que há bloqueio por volume em
	// curso. Serve de diagnóstico e evita repetir o aviso a cada relato.
	globalUntil time.Time
}

// NewPool monta uma combinação por perfil informado.
//
// Os clientes são construídos de uma vez: são só configuração, sem conexão de
// rede, e montá-los sob demanda deixaria a troca mais lenta justamente no momento
// em que ela é urgente.
func NewPool(opts PoolOptions) (*Pool, error) {
	profiles := dedupe(opts.Profiles)
	if len(profiles) == 0 {
		return nil, errors.New("Profiles é obrigatório")
	}
	if opts.Log == nil {
		return nil, errors.New("Log é obrigatório")
	}

	cooldown := opts.Cooldown
	if cooldown <= 0 {
		cooldown = DefaultCooldown
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}

	entries := make([]*entry, 0, len(profiles))
	for _, name := range profiles {
		engine, err := EngineFor(name)
		if err != nil {
			return nil, fmt.Errorf("perfil %q: %w", name, err)
		}

		httpClient, jar, err := New(Options{
			ProxyURL:       opts.ProxyURL,
			ProfileName:    name,
			TimeoutSeconds: opts.TimeoutSeconds,
		})
		if err != nil {
			return nil, fmt.Errorf("perfil %q: %w", name, err)
		}
		if err := SeedCookies(jar, opts.BaseURL, opts.Cookies); err != nil {
			return nil, fmt.Errorf("perfil %q: %w", name, err)
		}

		entries = append(entries, &entry{fp: &Fingerprint{
			Profile: name, Engine: engine, Client: httpClient, Jar: jar,
		}})
	}

	return &Pool{
		entries:  entries,
		cooldown: cooldown,
		now:      now,
		log:      opts.Log,
		recent:   make(map[string]time.Time, len(entries)),
	}, nil
}

// Size é quantas combinações o pool tem.
func (p *Pool) Size() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.entries)
}

// Current devolve a combinação a usar agora.
//
// Se a corrente estiver em cooldown, avança para a primeira disponível. Se TODAS
// estiverem, devolve a que sai do cooldown mais cedo: tentar e talvez tomar 403 é
// melhor que recusar-se a tentar, e o chamador não tem como esperar sozinho.
//
// Quando a combinação preferida sai do cooldown, o pool NÃO volta para ela.
// Trocar de identidade no meio de uma coleta que está funcionando é, por si só,
// estranho de se observar do outro lado, e não há ganho: a preferência de
// `-tls-profile` serve para escolher por onde começar, não para ser restaurada a
// cada dez minutos. Fixado em TestPoolStaysOnAWorkingProfile.
func (p *Pool) Current() *Fingerprint {
	p.mu.Lock()
	defer p.mu.Unlock()

	now := p.now()
	if !p.entries[p.current].until.After(now) {
		return p.entries[p.current].fp
	}

	for offset := 1; offset < len(p.entries); offset++ {
		i := (p.current + offset) % len(p.entries)
		if !p.entries[i].until.After(now) {
			p.current = i
			return p.entries[i].fp
		}
	}

	soonest := 0
	for i, e := range p.entries {
		if e.until.Before(p.entries[soonest].until) {
			soonest = i
		}
	}
	p.current = soonest
	p.log.Warn("todas as combinações em cooldown; usando a que expira primeiro",
		"perfil", p.entries[soonest].fp.Profile,
		"disponivel_em", p.entries[soonest].until.Sub(now).Round(time.Second).String())

	return p.entries[soonest].fp
}

// Blocked reporta que uma combinação foi recusada e devolve se houve troca.
//
// O nome do perfil é conferido contra o corrente de propósito: numa coleta
// concorrente, várias goroutines tomam 403 com a MESMA combinação quase ao mesmo
// tempo. Sem essa checagem, oito relatos consumiriam oito rotações e queimariam o
// pool inteiro por um único perfil ruim.
//
// false significa "não há para onde ir": o chamador deve tratar como terminal.
func (p *Pool) Blocked(profile string, reason error) bool {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.entries[p.current].fp.Profile != profile {
		// Outra goroutine já rotacionou por causa deste mesmo bloqueio.
		return true
	}

	now := p.now()
	p.recent[profile] = now

	// Bloqueio por VOLUME atinge todas as famílias de motor: rotacionar não
	// resolve e só queima o pool. Melhor parar e deixar o chamador esperar.
	if distinct := p.recentDistinct(now); distinct >= globalBlockThreshold {
		if !p.globalUntil.After(now) {
			p.log.Warn("bloqueio parece ser por VOLUME, não por identidade — rotação suspensa",
				"perfis_recusados", distinct,
				"janela", globalBlockWindow.String(),
				"acao", "espere antes de tentar de novo; trocar de fingerprint não resolve",
				"motivo", reason)
		}
		p.globalUntil = now.Add(globalBlockWindow)

		// O cooldown aplicado é o base, sem a progressão: o perfil não é culpado
		// pelo bloqueio, e inflá-lo o puniria por algo que passa sozinho.
		blocked := p.entries[p.current]
		if !blocked.until.After(now) {
			blocked.until = now.Add(p.cooldown)
		}
		return false
	}

	blocked := p.entries[p.current]
	blocked.blocks++
	// Quem falha repetidamente fica de fora por mais tempo.
	blocked.until = now.Add(p.cooldown * time.Duration(blocked.blocks))

	if len(p.entries) == 1 {
		p.log.Warn("combinação bloqueada e não há alternativa no pool",
			"perfil", profile, "motivo", reason)
		return false
	}

	for offset := 1; offset < len(p.entries); offset++ {
		i := (p.current + offset) % len(p.entries)
		if !p.entries[i].until.After(now) {
			p.log.Warn("combinação bloqueada, rotacionando",
				"de", profile, "para", p.entries[i].fp.Profile,
				"cooldown", blocked.until.Sub(now).Round(time.Second).String(),
				"motivo", reason)
			p.current = i
			return true
		}
	}

	p.log.Warn("combinação bloqueada e todas as alternativas em cooldown",
		"perfil", profile, "motivo", reason)
	return false
}

// recentDistinct conta quantos PERFIS distintos foram recusados na última janela.
//
// Chamado com o mutex retido. Entradas velhas são descartadas aqui em vez de por
// um temporizador: o mapa tem no máximo um item por perfil do pool, então a
// varredura é trivial e não há estado a expirar em background.
func (p *Pool) recentDistinct(now time.Time) int {
	n := 0
	for name, at := range p.recent {
		if now.Sub(at) > globalBlockWindow {
			delete(p.recent, name)
			continue
		}
		n++
	}
	return n
}

// GlobalBlockSuspected informa se o pool detectou bloqueio por volume em curso.
//
// Existe para diagnóstico: distinguir "o WAF recusou esta identidade" de "o WAF
// está recusando tudo agora" é a diferença entre trocar de perfil e esperar. Ver
// os limiares acima.
func (p *Pool) GlobalBlockSuspected() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.globalUntil.After(p.now())
}

// Available conta as combinações fora de cooldown. Serve para diagnóstico.
func (p *Pool) Available() int {
	p.mu.Lock()
	defer p.mu.Unlock()

	now := p.now()
	n := 0
	for _, e := range p.entries {
		if !e.until.After(now) {
			n++
		}
	}
	return n
}

// dedupe preserva a ordem e descarta repetições e vazios.
func dedupe(names []string) []string {
	out := make([]string, 0, len(names))
	for _, name := range names {
		if name != "" && !slices.Contains(out, name) {
			out = append(out, name)
		}
	}
	return out
}
