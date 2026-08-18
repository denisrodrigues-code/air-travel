// Transporte: execução, retry e montagem de cabeçalhos.

package tap

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"time"

	http "github.com/bogdanfinn/fhttp"

	"airtravel/internal/client"
	"airtravel/internal/config"
)

// ---------------------------------------------------------------------------
// Transporte
// ---------------------------------------------------------------------------

// doAuthed executa a requisição com JWT, renovando-o e repetindo uma vez em
// caso de 401.
func (s *Scraper) doAuthed(ctx context.Context, method, path string, query url.Values, body []byte) ([]byte, error) {
	token, err := s.ensureToken(ctx)
	if err != nil {
		return nil, err
	}

	raw, err := s.doWithRetry(ctx, method, path, query, body, token)
	if !errors.Is(err, ErrUnauthorized) {
		return raw, err
	}

	s.log.WarnContext(ctx, "JWT rejeitado, renovando")
	s.mu.Lock()
	s.token, s.tokenExp = "", time.Time{}
	s.mu.Unlock()

	if token, err = s.ensureToken(ctx); err != nil {
		return nil, err
	}
	return s.doWithRetry(ctx, method, path, query, body, token)
}

// doWithRetry aplica backoff exponencial em falhas transitórias e rotaciona o
// fingerprint em bloqueios.
//
// São dois orçamentos separados, e é deliberado: repetir resolve falha de rede,
// rotacionar resolve recusa de identidade. Se um bloqueio consumisse o orçamento
// de retry, um pool de quatro combinações nunca seria explorado com MaxRetries=3.
func (s *Scraper) doWithRetry(ctx context.Context, method, path string, query url.Values, body []byte, token string) ([]byte, error) {
	var lastErr error

	// maxRotations é um teto de segurança contra um rotator que sempre aceite
	// trocar. O limite real vem do próprio pool, que devolve false quando não há
	// combinação disponível.
	const maxRotations = 8
	attempts, rotations := 0, 0

	for attempts < s.cfg.MaxRetries && rotations <= maxRotations {
		if attempts > 0 {
			backoff := s.cfg.RetryBackoff
			if backoff <= 0 {
				backoff = 2 * time.Second
			}
			delay := time.Duration(1<<uint(attempts-1)) * backoff
			s.log.DebugContext(ctx, "aguardando para nova tentativa",
				"tentativa", attempts+1, "espera", delay.String())
			select {
			case <-ctx.Done():
				return nil, fmt.Errorf("cancelado durante espera de retry: %w", ctx.Err())
			case <-time.After(delay):
			}
		}

		// O perfil é lido ANTES da requisição para que o relato de bloqueio nomeie
		// a combinação que realmente falhou, e não a que já a substituiu.
		profile := s.fps.Current().Profile

		raw, err := s.do(ctx, method, path, query, body, token)
		if err == nil {
			return raw, nil
		}
		lastErr = err

		// Um bloqueio não se resolve repetindo a MESMA combinação, só trocando de
		// fingerprint. Havendo alternativa, a próxima vai imediatamente: não é falha
		// transitória, é recusa de identidade, e esperar não muda nada.
		if errors.Is(err, ErrAccessDenied) || errors.Is(err, ErrCloudflareChallenge) {
			if !s.fps.Blocked(profile, err) {
				return nil, s.blockError(err, rotations, path)
			}
			rotations++
			continue
		}

		// Erros definitivos não se resolvem com repetição.
		if errors.Is(err, ErrUnauthorized) ||
			errors.Is(err, context.Canceled) ||
			errors.Is(err, context.DeadlineExceeded) {
			return nil, err
		}

		attempts++
	}

	if rotations > 0 {
		return nil, s.blockError(lastErr, rotations, path)
	}
	return nil, fmt.Errorf("esgotadas %d tentativas: %w", s.cfg.MaxRetries, lastErr)
}

// blockError descreve um bloqueio terminal com o diagnóstico certo.
//
// "Esgotadas as combinações" descreve a recusa por IDENTIDADE, e nesse caso a ação
// é arranjar outro fingerprint. Quando o WAF está recusando todas as famílias de
// motor — bloqueio por volume — a ação é a oposta: esperar. Trocar de perfil não
// só não resolve, como queima o pool. Ver client.Pool e CLAUDE.md §4.
func (s *Scraper) blockError(err error, rotations int, path string) error {
	// O estado do jar vem PRIMEIRO, antes do diagnóstico de volume.
	//
	// Os dois produzem o mesmo sintoma — todas as combinações recusadas, ~1585 ms
	// cada — mas pedem ações opostas: jar vencido pede recapturar, volume pede
	// esperar. E a ordem importa porque o bloqueio por volume é INFERIDO (três
	// perfis distintos recusados numa janela), enquanto a idade do jar é MEDIDA.
	// Deixar a inferência falar primeiro foi o que fez a aplicação recomendar
	// "espere" para um problema que a espera não resolve — em 2026-08-06,
	// duas vezes: comigo e no log da API. Ver CLAUDE.md §4.
	if jar := s.clearanceProblem(path); jar != "" {
		return fmt.Errorf("o WAF recusou a requisição e %s — recapture os cookies de um "+
			"navegador real na MESMA identidade do perfil TLS em uso (%s/%s); "+
			"esperar não resolve: %w", jar, s.Profile(), s.Engine().Name, err)
	}

	if g, ok := s.fps.(globalBlocker); ok && g.GlobalBlockSuspected() {
		return fmt.Errorf("o WAF recusou todas as combinações tentadas (%d rotações) — "+
			"parece bloqueio por volume, não por fingerprint: espere antes de repetir, "+
			"trocar de perfil não resolve: %w", rotations, err)
	}
	if rotations > 0 {
		return fmt.Errorf("esgotadas as combinações após %d rotações: %w", rotations, err)
	}
	return err
}

// clearanceProblem descreve o que há de errado com o cf_clearance carregado, ou
// string vazia se não há nada a apontar.
//
// Devolve texto e não um erro porque isto não é uma condição própria: é um
// qualificador do 403 que o WAF já devolveu. O erro do provedor continua sendo a
// causa embrulhada.
func (s *Scraper) clearanceProblem(path string) string {
	// Só a rota de busca exige clearance. As de calendário responderam 200 a 72
	// requisições SEM cookie algum em 2026-08-06, na mesma janela em que a busca
	// recusava tudo (CLAUDE.md §4). Culpar o jar por um bloqueio de calendário
	// mandaria procurar no lugar errado — exatamente o defeito que esta função
	// existe para corrigir, só que na direção oposta.
	if path != pathAvailability {
		return ""
	}

	if !s.cfg.HasCookie("cf_clearance") {
		return "não há cf_clearance no jar"
	}

	age, ok := s.cfg.ClearanceAge(time.Now())
	if !ok {
		// Formato irreconhecível: não se afirma nada sobre a idade.
		return ""
	}
	if age > config.ClearanceTTL {
		return fmt.Sprintf("o cf_clearance tem %s, acima do limite de %s",
			age.Round(time.Minute), config.ClearanceTTL)
	}
	return ""
}

// do monta e executa uma requisição isolada, respeitando o rate limit e
// reproduzindo a ordem de cabeçalhos observada no navegador.
func (s *Scraper) do(ctx context.Context, method, path string, query url.Values, body []byte, token string) ([]byte, error) {
	// A combinação é tirada UMA vez e viaja junta: os cabeçalhos abaixo e o
	// cliente que envia a requisição vêm do mesmo fingerprint. Ler o motor de um
	// lugar e o cliente de outro permitiria que uma rotação concorrente casasse o
	// User-Agent de um perfil com o ClientHello de outro.
	fp := s.fps.Current()

	if err := s.limiter.Wait(ctx); err != nil {
		return nil, fmt.Errorf("failed waiting for rate limiter: %w", err)
	}

	target := s.cfg.BaseURL + path
	if len(query) > 0 {
		target += "?" + query.Encode()
	}

	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}

	req, err := http.NewRequestWithContext(ctx, method, target, reader)
	if err != nil {
		return nil, fmt.Errorf("failed to build request for %s: %w", target, err)
	}
	s.applyHeaders(req, fp.Engine, path, token, body != nil)

	resp, err := fp.Client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to execute %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()

	client.DecompressBody(resp)

	payload, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body from %s: %w", path, err)
	}

	if err := classifyResponse(resp, path, payload); err != nil {
		return nil, err
	}
	return payload, nil
}

// applyHeaders monta os cabeçalhos coerentes com o motor de navegador em uso.
//
// Deliberadamente ausentes: Cookie (gerido pelo jar), Content-Length (calculado
// pelo cliente) e Host/Connection (geridos pelo transporte). Ainda assim eles
// constam da ordem anunciada, pois é a posição relativa que compõe a impressão
// digital HTTP/2.
//
// A ordem é a canônica do motor, projetada sobre os cabeçalhos realmente
// presentes: nunca se anuncia um cabeçalho que não foi enviado.
func (s *Scraper) applyHeaders(req *http.Request, e client.Engine, path, token string, hasBody bool) {
	profile := profileFor(path)

	req.Header.Set("user-agent", e.UserAgent)
	req.Header.Set("accept", "application/json, text/plain, */*")

	// O motor pode reduzir a entropia do accept-language: o Brave manda duas
	// entradas onde o Chrome manda quatro. Vazio usa o valor da configuração.
	acceptLang := s.cfg.AcceptLang
	if e.AcceptLang != "" {
		acceptLang = e.AcceptLang
	}
	req.Header.Set("accept-language", acceptLang)
	req.Header.Set("accept-encoding", "gzip, deflate, br, zstd")
	req.Header.Set("origin", s.cfg.BaseURL)
	req.Header.Set("referer", s.cfg.BaseURL+profile.refererPath)

	if hasBody {
		req.Header.Set("content-type", "application/json")
	}
	if token != "" {
		req.Header.Set("authorization", "Bearer "+token)
	}

	if e.SecFetch {
		req.Header.Set("sec-fetch-site", "same-origin")
		req.Header.Set("sec-fetch-mode", "cors")
		req.Header.Set("sec-fetch-dest", "empty")
	}

	if e.ClientHints {
		req.Header.Set("sec-ch-ua", e.SecCHUA)
		req.Header.Set("sec-ch-ua-mobile", "?0")
		req.Header.Set("sec-ch-ua-platform", e.SecCHUAPlatform)
		req.Header.Set("priority", "u=1, i")
	}

	// Os cabeçalhos de RUM do Dynatrace são da aplicação, não do motor: a SPA os
	// envia nas chamadas disparadas da página de resultados.
	//
	// "Em qualquer navegador" era a suposição original, e ela caiu: o capture do
	// Brave em 2026-08-06 não traz nenhum dos quatro, porque ele bloqueia o script
	// do Dynatrace como rastreador. Então a condição é dupla — a rota tem de
	// disparar a telemetria E o navegador tem de deixá-la rodar.
	if profile.dynatrace && !e.BlocksTrackers {
		traceparent, tracestate := s.dt.trace()
		req.Header.Set("x-dtreferer", s.cfg.BaseURL+"/booking")
		req.Header.Set("x-dtpc", s.dt.dtpc)
		req.Header.Set("traceparent", traceparent)
		req.Header.Set("tracestate", tracestate)
	}

	for name, value := range e.Extra {
		req.Header.Set(name, value)
	}

	req.Header[http.HeaderOrderKey] = e.ProjectOrder(func(name string) bool {
		return req.Header.Get(name) != ""
	})
	req.Header[http.PHeaderOrderKey] = []string{
		":method",
		":authority",
		":scheme",
		":path",
	}
}
