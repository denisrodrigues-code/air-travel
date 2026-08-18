package client

import (
	"errors"
	"log/slog"
	"sync"
	"testing"
	"time"

	http "github.com/bogdanfinn/fhttp"
)

// clock é um relógio manual: o cooldown é testável sem esperar.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func newClock() *clock {
	return &clock{t: time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC)}
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

var errBlocked = errors.New("WAF 403")

func newTestPool(t *testing.T, cl *clock, cooldown time.Duration, profiles ...string) *Pool {
	t.Helper()

	p, err := NewPool(PoolOptions{
		Profiles: profiles,
		BaseURL:  "https://booking.flytap.com",
		Cooldown: cooldown,
		Log:      slog.New(slog.DiscardHandler),
		Now:      cl.now,
	})
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	return p
}

// ---------------------------------------------------------------------------
// Montagem
// ---------------------------------------------------------------------------

// TestPoolFingerprintIsCoherent é a propriedade que o pool não pode quebrar:
// perfil TLS e identidade HTTP andam juntos. Cabeçalhos de Firefox sobre um
// ClientHello de Safari são mais fáceis de detectar do que qualquer um dos dois.
func TestPoolFingerprintIsCoherent(t *testing.T) {
	cl := newClock()
	p := newTestPool(t, cl, time.Minute, "firefox_148", "safari_ios_18_5", "chrome_151")

	seen := map[string]string{}
	for range p.Size() {
		fp := p.Current()
		seen[fp.Profile] = fp.Engine.Name

		want, err := EngineFor(fp.Profile)
		if err != nil {
			t.Fatalf("perfil %q sem motor: %v", fp.Profile, err)
		}
		if fp.Engine.Name != want.Name || fp.Engine.UserAgent != want.UserAgent {
			t.Errorf("perfil %q veio com o motor %q; esperado %q",
				fp.Profile, fp.Engine.Name, want.Name)
		}
		if fp.Client == nil || fp.Jar == nil {
			t.Errorf("perfil %q sem cliente ou jar", fp.Profile)
		}
		p.Blocked(fp.Profile, errBlocked)
	}

	if seen["firefox_148"] != "gecko" || seen["safari_ios_18_5"] != "webkit" || seen["chrome_151"] != "chromium" {
		t.Errorf("motores por perfil = %v", seen)
	}
}

func TestPoolRejectsBadOptions(t *testing.T) {
	log := slog.New(slog.DiscardHandler)

	if _, err := NewPool(PoolOptions{Log: log}); err == nil {
		t.Error("aceitou lista vazia de perfis")
	}
	if _, err := NewPool(PoolOptions{Profiles: []string{"firefox_148"}}); err == nil {
		t.Error("aceitou Log nil")
	}
	if _, err := NewPool(PoolOptions{Profiles: []string{"netscape_4"}, Log: log}); err == nil {
		t.Error("aceitou perfil inexistente")
	}
}

// TestPoolDedupesProfiles: o preferido costuma já constar de PassingProfiles, e
// repetido ele ganharia duas fatias da rotação.
func TestPoolDedupesProfiles(t *testing.T) {
	cl := newClock()
	p := newTestPool(t, cl, time.Minute, "firefox_148", "firefox_148", "", "firefox_147")

	if got := p.Size(); got != 2 {
		t.Errorf("Size() = %d, esperado 2", got)
	}
}

// TestPoolHonoursPreferenceOrder: o primeiro perfil é o usado até ser recusado.
func TestPoolHonoursPreferenceOrder(t *testing.T) {
	cl := newClock()
	p := newTestPool(t, cl, time.Minute, "safari_ios_18_5", "firefox_148")

	if got := p.Current().Profile; got != "safari_ios_18_5" {
		t.Errorf("primeiro perfil = %q, esperado o preferido safari_ios_18_5", got)
	}
}

// ---------------------------------------------------------------------------
// Rotação
// ---------------------------------------------------------------------------

// TestPoolRotatesOnBlock é a razão de o pool existir: com uma combinação só, um
// 403 encerra a coleta.
func TestPoolRotatesOnBlock(t *testing.T) {
	cl := newClock()
	p := newTestPool(t, cl, 10*time.Minute, "firefox_148", "firefox_147")

	if !p.Blocked("firefox_148", errBlocked) {
		t.Fatal("Blocked() = false; havia alternativa disponível")
	}
	if got := p.Current().Profile; got != "firefox_147" {
		t.Errorf("depois do bloqueio = %q, esperado firefox_147", got)
	}
}

// TestPoolSingleEntryHasNowhereToGo: com uma combinação só, o bloqueio é
// terminal — e dizer isso é o que permite ao adapter devolver o erro em vez de
// insistir.
func TestPoolSingleEntryHasNowhereToGo(t *testing.T) {
	cl := newClock()
	p := newTestPool(t, cl, time.Minute, "firefox_148")

	if p.Blocked("firefox_148", errBlocked) {
		t.Error("Blocked() = true sem alternativa no pool")
	}
}

// TestPoolExhaustsThenRecovers percorre o ciclo completo: todas bloqueadas, e o
// cooldown devolvendo a primeira.
//
// Os bloqueios são espaçados de propósito. Aplicados no mesmo instante, os dois
// cooldowns expirariam juntos e o teste não distinguiria "a mais antiga voltou" de
// "todas voltaram".
func TestPoolExhaustsThenRecovers(t *testing.T) {
	cl := newClock()
	const cooldown = 10 * time.Minute
	p := newTestPool(t, cl, cooldown, "firefox_148", "firefox_147")

	// T+0: firefox_148 sai até T+10.
	if !p.Blocked("firefox_148", errBlocked) {
		t.Fatal("primeira rotação falhou")
	}
	// T+5: firefox_147 sai até T+15, e não há para onde ir.
	cl.advance(5 * time.Minute)
	if p.Blocked("firefox_147", errBlocked) {
		t.Error("rotacionou com as duas bloqueadas")
	}
	if got := p.Available(); got != 0 {
		t.Errorf("Available() = %d, esperado 0", got)
	}

	// Com tudo em cooldown, Current ainda devolve algo: tentar e talvez tomar 403 é
	// melhor que se recusar a tentar, e o chamador não tem como esperar sozinho.
	if p.Current() == nil {
		t.Error("Current() devolveu nil com tudo em cooldown")
	}

	// T+11: só firefox_148 voltou.
	cl.advance(6 * time.Minute)
	if got := p.Available(); got != 1 {
		t.Errorf("Available() = %d, esperado 1 (só firefox_148)", got)
	}
	if got := p.Current().Profile; got != "firefox_148" {
		t.Errorf("Current() = %q, esperado firefox_148 de volta", got)
	}
}

// TestPoolStaysOnAWorkingProfile fixa uma decisão que não é óbvia: quando a
// combinação preferida sai do cooldown, o pool NÃO volta para ela.
//
// Trocar de identidade no meio de uma coleta que está funcionando é, por si só,
// um comportamento estranho de se observar do outro lado — e não há ganho: a
// preferência de `-tls-profile` serve para escolher por onde começar, não para ser
// restaurada a cada dez minutos.
func TestPoolStaysOnAWorkingProfile(t *testing.T) {
	cl := newClock()
	const cooldown = 10 * time.Minute
	p := newTestPool(t, cl, cooldown, "firefox_148", "firefox_147")

	p.Blocked("firefox_148", errBlocked) // agora em firefox_147
	cl.advance(cooldown + time.Second)   // firefox_148 disponível de novo

	if got := p.Current().Profile; got != "firefox_147" {
		t.Errorf("Current() = %q, esperado continuar em firefox_147", got)
	}
	if got := p.Available(); got != 2 {
		t.Errorf("Available() = %d, esperado 2", got)
	}
}

// TestPoolCooldownGrowsWithRepeatedBlocks: quem falha sempre fica de fora por
// mais tempo, para não voltar à frente da fila a cada dez minutos.
//
// Medido com uma combinação só, onde o cooldown é observável sem interferência da
// rotação.
func TestPoolCooldownGrowsWithRepeatedBlocks(t *testing.T) {
	cl := newClock()
	const cooldown = 10 * time.Minute
	p := newTestPool(t, cl, cooldown, "firefox_148")

	// 1º bloqueio: fora por 10 min.
	p.Blocked("firefox_148", errBlocked)
	cl.advance(cooldown + time.Second)
	if got := p.Available(); got != 1 {
		t.Fatalf("Available() = %d, esperado 1 após o primeiro cooldown", got)
	}

	// 2º bloqueio: fora por 20 min.
	p.Blocked("firefox_148", errBlocked)
	cl.advance(cooldown + time.Second)
	if got := p.Available(); got != 0 {
		t.Errorf("Available() = %d; o segundo cooldown deveria passar de %s", got, cooldown)
	}
	cl.advance(cooldown)
	if got := p.Available(); got != 1 {
		t.Errorf("Available() = %d, esperado 1 após o cooldown dobrado", got)
	}
}

// TestPoolIgnoresStaleBlockReports é a proteção contra o cenário concorrente
// real: várias goroutines tomam 403 com a MESMA combinação quase ao mesmo tempo.
//
// Sem a checagem do perfil, oito relatos consumiriam oito rotações e queimariam o
// pool inteiro por um único perfil ruim.
func TestPoolIgnoresStaleBlockReports(t *testing.T) {
	cl := newClock()
	p := newTestPool(t, cl, 10*time.Minute, "firefox_148", "firefox_147", "firefox_135")

	// Oito goroutines relatam o MESMO perfil.
	for range 8 {
		if !p.Blocked("firefox_148", errBlocked) {
			t.Fatal("um relato repetido foi tratado como esgotamento do pool")
		}
	}

	if got := p.Current().Profile; got != "firefox_147" {
		t.Errorf("Current() = %q; oito relatos do mesmo perfil devem custar UMA rotação", got)
	}
	if got := p.Available(); got != 2 {
		t.Errorf("Available() = %d, esperado 2; só firefox_148 deveria estar em cooldown", got)
	}
}

// ---------------------------------------------------------------------------
// Concorrência
// ---------------------------------------------------------------------------

// TestPoolUnderContention exercita o pool como a coleta o usa: várias goroutines
// pedindo combinação e relatando bloqueio ao mesmo tempo.
//
// ATENÇÃO ao que este teste NÃO prova: sem `-race` (indisponível neste ambiente,
// exige CGO_ENABLED=1 e gcc) ele não detecta corrida de dados. O que ele pega é
// erro de lógica sob contenção — invariante violada, índice fora de faixa,
// deadlock — e é por isso que existe.
func TestPoolUnderContention(t *testing.T) {
	cl := newClock()
	p := newTestPool(t, cl, time.Minute, PassingProfiles...)

	const workers, rounds = 16, 200
	var wg sync.WaitGroup

	for range workers {
		// WaitGroup.Go (Go 1.25) faz o Add e o Done, então não há como esquecer um
		// deles nem como o defer se perder numa saída antecipada — e este corpo tem
		// dois `return`.
		wg.Go(func() {
			for range rounds {
				fp := p.Current()
				if fp == nil || fp.Profile == "" || fp.Client == nil {
					t.Errorf("Current() devolveu combinação incompleta: %+v", fp)
					return
				}
				// A coerência tem de valer em qualquer momento da rotação.
				if want, err := EngineFor(fp.Profile); err != nil || want.Name != fp.Engine.Name {
					t.Errorf("combinação incoerente: perfil %q com motor %q", fp.Profile, fp.Engine.Name)
					return
				}
				p.Blocked(fp.Profile, errBlocked)
				p.Available()
			}
		})
	}
	wg.Wait()

	// O pool sobreviveu e continua utilizável.
	if p.Current() == nil {
		t.Error("pool inutilizável depois da contenção")
	}
	if got := p.Size(); got != len(PassingProfiles) {
		t.Errorf("Size() = %d, esperado %d — o pool não deve perder combinações",
			got, len(PassingProfiles))
	}
}

// TestPoolDoesNotShareJars: cookies pertencem a uma sessão, que pertence a um
// fingerprint. Um jar compartilhado levaria o cf_clearance de uma identidade para
// outra.
func TestPoolDoesNotShareJars(t *testing.T) {
	cl := newClock()
	p := newTestPool(t, cl, time.Minute, "firefox_148", "firefox_147")

	first := p.Current()
	p.Blocked(first.Profile, errBlocked)
	second := p.Current()

	if first.Jar == second.Jar {
		t.Error("as duas combinações compartilham o mesmo cookie jar")
	}
	if first.Client == second.Client {
		t.Error("as duas combinações compartilham o mesmo cliente HTTP")
	}
}

// TestPoolClientIsUsable confirma que o cliente montado é um cliente de verdade,
// e não um zero value.
func TestPoolClientIsUsable(t *testing.T) {
	cl := newClock()
	p := newTestPool(t, cl, time.Minute, "firefox_148")

	fp := p.Current()
	req, err := http.NewRequest(http.MethodGet, "https://booking.flytap.com/", nil)
	if err != nil {
		t.Fatalf("failed to build request: %v", err)
	}
	// Não há chamada de rede aqui: só se confirma que a interface está satisfeita
	// por um valor não nulo.
	var _ Doer = fp.Client
	_ = req
}

// ---------------------------------------------------------------------------
// Bloqueio por volume x bloqueio por identidade
// ---------------------------------------------------------------------------

// TestPoolDetectsGlobalBlock: quando o WAF recusa VÁRIAS famílias de motor em
// sequência, o problema não é a identidade — e rotacionar só queima o pool.
//
// Medido em 2026-08-04: depois de algumas dezenas de buscas do mesmo IP, os seis
// perfis passaram a tomar 403, Gecko e WebKit incluídos, e a janela durou mais de
// 16 minutos. Antes desta detecção o pool gastava as quatro combinações em
// segundos e mandava todas para o cooldown por causa de algo que passa sozinho.
func TestPoolDetectsGlobalBlock(t *testing.T) {
	cl := newClock()
	p := newTestPool(t, cl, 10*time.Minute, PassingProfiles...)

	if p.GlobalBlockSuspected() {
		t.Fatal("suspeita de bloqueio global antes de qualquer recusa")
	}

	// Os dois primeiros são tratados como recusa de identidade: rotaciona.
	for i := range globalBlockThreshold - 1 {
		profile := p.Current().Profile
		if !p.Blocked(profile, errBlocked) {
			t.Fatalf("recusa %d: deveria ter rotacionado", i+1)
		}
		if p.GlobalBlockSuspected() {
			t.Errorf("recusa %d de %d já acusou bloqueio global", i+1, globalBlockThreshold)
		}
		cl.advance(2 * time.Second)
	}

	// O terceiro perfil distinto dentro da janela muda o diagnóstico.
	if p.Blocked(p.Current().Profile, errBlocked) {
		t.Error("Blocked() = true; com bloqueio global não há para onde rotacionar")
	}
	if !p.GlobalBlockSuspected() {
		t.Errorf("GlobalBlockSuspected() = false após %d perfis distintos recusados",
			globalBlockThreshold)
	}

	// E as combinações ainda não recusadas permanecem utilizáveis: o bloqueio é
	// do momento, não delas.
	if got := p.Available(); got != len(PassingProfiles)-globalBlockThreshold {
		t.Errorf("Available() = %d, esperado %d — só as recusadas entram em cooldown",
			got, len(PassingProfiles)-globalBlockThreshold)
	}
}

// TestPoolRepeatedReportsDoNotLookGlobal: oito goroutines relatando o MESMO
// perfil é o cenário concorrente normal, não bloqueio por volume.
//
// A contagem é por perfil distinto, não por relato — senão a proteção contra
// relatos repetidos (TestPoolIgnoresStaleBlockReports) seria desfeita por esta.
func TestPoolRepeatedReportsDoNotLookGlobal(t *testing.T) {
	cl := newClock()
	p := newTestPool(t, cl, 10*time.Minute, PassingProfiles...)

	// O perfil relatado sai de Current(), não de um literal.
	//
	// Fixar "firefox_148" aqui acoplava o teste à ORDEM de PassingProfiles: quando a
	// medição de 2026-08-06 reescreveu aquela lista, o relato passou a nomear um
	// perfil que não era o corrente, virou relato obsoleto — pela própria proteção
	// que o item 3 do §4.3 descreve — e não houve rotação. O teste falhou sem que
	// nada da lógica do pool tivesse mudado.
	first := p.Current().Profile
	for range 8 {
		p.Blocked(first, errBlocked)
	}

	if p.GlobalBlockSuspected() {
		t.Error("relatos repetidos do mesmo perfil foram lidos como bloqueio por volume")
	}
	if got, want := p.Current().Profile, PassingProfiles[1]; got != want {
		t.Errorf("Current() = %q, esperado %q (uma rotação só)", got, want)
	}
}

// TestPoolSpacedBlocksAreNotGlobal: recusas espalhadas no tempo são recusa de
// identidade, e para essas a rotação é a resposta certa.
func TestPoolSpacedBlocksAreNotGlobal(t *testing.T) {
	cl := newClock()
	p := newTestPool(t, cl, 10*time.Minute, PassingProfiles...)

	for range globalBlockThreshold + 1 {
		p.Blocked(p.Current().Profile, errBlocked)
		// Além da janela: cada recusa é um evento isolado.
		cl.advance(globalBlockWindow + time.Second)

		if p.GlobalBlockSuspected() {
			t.Fatal("recusas espaçadas foram lidas como bloqueio por volume")
		}
	}
}

// TestPoolGlobalBlockDoesNotInflateCooldown: no bloqueio por volume o perfil não
// é culpado, então não deve herdar o cooldown progressivo — que existe para punir
// quem falha sempre, não quem estava em uso na hora errada.
func TestPoolGlobalBlockDoesNotInflateCooldown(t *testing.T) {
	cl := newClock()
	const cooldown = 10 * time.Minute
	p := newTestPool(t, cl, cooldown, PassingProfiles...)

	// Dispara o diagnóstico global.
	for range globalBlockThreshold {
		p.Blocked(p.Current().Profile, errBlocked)
		cl.advance(time.Second)
	}
	if !p.GlobalBlockSuspected() {
		t.Fatal("o cenário não disparou o diagnóstico global")
	}

	// Passado o cooldown base, tudo volta: nenhum perfil ficou com cooldown dobrado.
	cl.advance(cooldown + time.Second)
	if got := p.Available(); got != len(PassingProfiles) {
		t.Errorf("Available() = %d, esperado %d — algum cooldown foi inflado",
			got, len(PassingProfiles))
	}
	if p.GlobalBlockSuspected() {
		t.Error("a suspeita de bloqueio global não expirou com o tempo")
	}
}
