package tap

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	http "github.com/bogdanfinn/fhttp"

	"airtravel/internal/config"
)

// Este arquivo fixa o terceiro diagnóstico de bloqueio: **o estado do jar**.
//
// Havia dois — "esgotadas as combinações" (arranje outro fingerprint) e "parece
// bloqueio por volume" (espere) — e eles cobrem causas cujas ações são opostas.
// Faltava a causa que de fato ocorre desde 2026-08-06: a rota `search` passou a
// exigir `cf_clearance`, e sem ele (ou com ele vencido) a recusa é permanente.
//
// O custo de não ter essa terceira mensagem foi medido duas vezes no mesmo dia:
// uma sessão de medição esperou 38 min por uma "janela de volume" inexistente, e
// o log da API repetiu o mesmo conselho — "espere antes de repetir, trocar de
// perfil não resolve" — para um problema que a espera não resolve.
//
// Ver CLAUDE.md §4 e MEDICOES-PERFIS.md.

// clearanceCookie monta um cf_clearance com o instante de emissão embutido, no
// formato real do cookie da Cloudflare.
func clearanceCookie(issued time.Time) config.Cookie {
	return config.Cookie{
		Name: "cf_clearance",
		Value: fmt.Sprintf("t0UZ7BGCUjFYpe9qcVbFTqz05bCQUygavPBpyyNSquA-%d-1.2.1.1-zSSdZo3MtXl9",
			issued.Unix()),
	}
}

// blockOn executa uma requisição bloqueada no caminho informado e devolve a
// mensagem de erro final.
func blockOn(t *testing.T, path string, cookies []config.Cookie, global bool) string {
	t.Helper()

	a := &stubDoer{responses: []*http.Response{blockedResponse(t)}}
	b := &stubDoer{responses: []*http.Response{blockedResponse(t)}}

	s, rot := newRotatingScraper(t, a, b)
	s.cfg.Cookies = cookies
	s.fps = &globalStub{stubRotator: *rot, global: global}

	_, err := s.doWithRetry(context.Background(), http.MethodPost, path, nil, nil, "tok")
	if err == nil {
		t.Fatal("erro esperado")
	}
	return err.Error()
}

// TestSearchBlockWithoutClearanceSaysToRecapture é o caso do log da API de
// 2026-08-06: sem jar, a mensagem mandava esperar.
func TestSearchBlockWithoutClearanceSaysToRecapture(t *testing.T) {
	msg := blockOn(t, pathAvailability, nil, true)

	for _, want := range []string{"não há cf_clearance", "recapture", "esperar não resolve"} {
		if !strings.Contains(msg, want) {
			t.Errorf("mensagem não menciona %q:\n  %s", want, msg)
		}
	}
	// O diagnóstico de volume manda ESPERAR — a ação oposta. Se ele vencer, a
	// mensagem manda procurar no lugar errado, que é o defeito original.
	if strings.Contains(msg, "espere antes de repetir") {
		t.Errorf("o diagnóstico de volume venceu o de jar ausente:\n  %s", msg)
	}
}

// TestSearchBlockWithStaleClearanceReportsAge distingue jar vencido de jar
// ausente. A ação é a mesma (recapturar), mas a causa dita a mensagem — e a idade
// é o que permite ao usuário confirmar sozinho.
func TestSearchBlockWithStaleClearanceReportsAge(t *testing.T) {
	stale := time.Now().Add(-90 * time.Minute)
	msg := blockOn(t, pathAvailability, []config.Cookie{clearanceCookie(stale)}, true)

	if !strings.Contains(msg, "recapture") {
		t.Errorf("mensagem não manda recapturar:\n  %s", msg)
	}
	if !strings.Contains(msg, "1h30m0s") {
		t.Errorf("mensagem não informa a idade medida do jar:\n  %s", msg)
	}
	if strings.Contains(msg, "não há cf_clearance") {
		t.Errorf("jar vencido foi reportado como ausente:\n  %s", msg)
	}
}

// TestFreshClearanceFallsThroughToTheOtherDiagnostics: com jar recente, o jar não
// é o suspeito, e os dois diagnósticos originais voltam a valer.
//
// Importa porque o bloqueio por volume continua existindo (medido em 2026-08-04):
// a terceira mensagem não pode engolir as outras duas.
func TestFreshClearanceFallsThroughToTheOtherDiagnostics(t *testing.T) {
	fresh := []config.Cookie{clearanceCookie(time.Now().Add(-2 * time.Minute))}

	if msg := blockOn(t, pathAvailability, fresh, true); !strings.Contains(msg, "bloqueio por volume") {
		t.Errorf("com jar recente, o diagnóstico de volume deveria valer:\n  %s", msg)
	}
	if msg := blockOn(t, pathAvailability, fresh, false); !strings.Contains(msg, "esgotadas as combinações") {
		t.Errorf("com jar recente e sem volume, deveria dizer esgotadas:\n  %s", msg)
	}
}

// TestCalendarBlockDoesNotBlameTheJar é a guarda contra corrigir o defeito na
// direção oposta.
//
// As rotas de calendário responderam 200 a 72 requisições **sem cookie algum** em
// 2026-08-06, na mesma janela em que a busca recusava tudo. Culpar o jar por um
// bloqueio de calendário mandaria o usuário capturar cookies que aquela rota não
// usa.
func TestCalendarBlockDoesNotBlameTheJar(t *testing.T) {
	for _, path := range []string{pathCalendar, pathCalendarReturns} {
		msg := blockOn(t, path, nil, true)

		if strings.Contains(msg, "cf_clearance") {
			t.Errorf("bloqueio em %s culpou o jar, mas essa rota dispensa cookies:\n  %s",
				path, msg)
		}
		if !strings.Contains(msg, "bloqueio por volume") {
			t.Errorf("bloqueio em %s perdeu o diagnóstico de volume:\n  %s", path, msg)
		}
	}
}

// TestMalformedClearanceMakesNoClaim: se o formato do cookie muda e a idade não é
// extraível, não se afirma nada sobre ela.
//
// A alternativa — tratar irreconhecível como vencido — inventaria um diagnóstico a
// partir de um valor não compreendido, que é a classe de erro que este trabalho
// todo existe para não repetir.
func TestMalformedClearanceMakesNoClaim(t *testing.T) {
	weird := []config.Cookie{{Name: "cf_clearance", Value: "semtimestamp"}}
	msg := blockOn(t, pathAvailability, weird, true)

	if strings.Contains(msg, "acima do limite") {
		t.Errorf("afirmou idade de um cf_clearance sem timestamp:\n  %s", msg)
	}
	if !strings.Contains(msg, "bloqueio por volume") {
		t.Errorf("deveria cair nos diagnósticos originais:\n  %s", msg)
	}
}
