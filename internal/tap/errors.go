// Erros do adapter e a classificação das respostas HTTP da TAP.

package tap

import (
	"bytes"
	"fmt"
	"regexp"
	"strings"

	http "github.com/bogdanfinn/fhttp"

	"airtravel/internal/collect"
)

// Erros sentinela do adapter.
//
// Cada um embrulha o erro correspondente de collect, o vocabulário do caso de
// uso. Assim `errors.Is(err, collect.ErrBlocked)` funciona sem que a camada HTTP
// conheça WAF, JA3 ou Cloudflare — e continua possível comparar com o sentinela
// específico daqui quando o detalhe importa (nos testes deste pacote, por
// exemplo).
//
// O embrulho é feito na declaração, não nos pontos de chamada: acrescentar um
// erro novo é uma linha, e não há como esquecer de traduzi-lo.
var (
	// ErrCloudflareChallenge indica a página interativa da Cloudflare ("Just a
	// moment"): o cf_clearance expirou ou não vale para este IP/fingerprint.
	// Resolve-se recoletando os cookies num navegador real.
	ErrCloudflareChallenge = fmt.Errorf("%w: desafio da Cloudflare recebido", collect.ErrBlocked)

	// ErrAccessDenied indica a página "ACCESS DENIED" da própria TAP, servida
	// pelo WAF. É bloqueio por rota e por família de motor — perfis Chromium são
	// recusados em availability/search, Gecko passa. Ver CLAUDE.md §4.
	ErrAccessDenied = fmt.Errorf("%w: acesso negado pelo WAF da TAP", collect.ErrBlocked)

	// ErrUnauthorized indica JWT inválido ou expirado.
	ErrUnauthorized = fmt.Errorf("%w: não autorizado", collect.ErrUnauthorized)

	// ErrRateLimited indica bloqueio por excesso de requisições.
	ErrRateLimited = fmt.Errorf("%w: limite de requisições atingido", collect.ErrRateLimited)

	// ErrAPIStatus indica envelope com status lógico diferente de 200, ou corpo
	// que não pode ser aproveitado.
	ErrAPIStatus = fmt.Errorf("%w: status lógico de erro na resposta da API", collect.ErrInvalidResponse)
)

// classifyResponse traduz o resultado HTTP em erros de domínio.
func classifyResponse(resp *http.Response, path string, payload []byte) error {
	switch {
	case resp.StatusCode == http.StatusOK:
		return nil
	case resp.StatusCode == http.StatusUnauthorized:
		return fmt.Errorf("%w: HTTP 401 em %s", ErrUnauthorized, path)
	case resp.StatusCode == http.StatusTooManyRequests:
		return fmt.Errorf("%w: HTTP 429 em %s", ErrRateLimited, path)
	case isAccessDenied(payload):
		return fmt.Errorf("%w: HTTP %d em %s (%s)",
			ErrAccessDenied, resp.StatusCode, path, blockDetails(payload))
	case isCloudflareChallenge(resp, payload):
		return fmt.Errorf("%w: HTTP %d em %s — renove cf_clearance a partir de um navegador real",
			ErrCloudflareChallenge, resp.StatusCode, path)
	default:
		return fmt.Errorf("HTTP %d inesperado em %s: %s",
			resp.StatusCode, path, truncate(payload, 300))
	}
}

// isAccessDenied detecta a página "ACCESS DENIED" da TAP. É verificada antes do
// desafio da Cloudflare porque toda página do site carrega o beacon da CF, cujo
// script contém "challenge-platform" e provocaria um falso positivo.
func isAccessDenied(payload []byte) bool {
	return bytes.Contains(payload, []byte("ACCESS DENIED")) &&
		bytes.Contains(payload, []byte("blocked by our security services"))
}

// blockDetails extrai geolocalização, IP e identificador do bloqueio, que a
// página expõe e que a TAP pede em qualquer contato de suporte.
func blockDetails(payload []byte) string {
	fields := make([]string, 0, 3)
	for _, label := range []string{"Geolocation", "Your IP", "ID"} {
		if value := matchAfterLabel(payload, label); value != "" {
			fields = append(fields, label+": "+value)
		}
	}
	if len(fields) == 0 {
		return "sem detalhes na página de bloqueio"
	}
	return strings.Join(fields, " | ")
}

// blockFieldPattern captura o valor que segue um rótulo na página de bloqueio,
// ignorando as tags HTML intercaladas.
//
// Sensível à maiúscula e exigindo os dois-pontos de propósito: uma variante
// case-insensitive casava o "id" de `width=device-width` na meta viewport e
// devolvia lixo como identificador do bloqueio.
var blockFieldPattern = regexp.MustCompile(`(?s)` +
	`\b(Geolocation|Your IP|ID)\s*:\s*(?:<[^>]*>\s*)*([^<\s][^<]*?)\s*(?:<|$)`)

func matchAfterLabel(payload []byte, label string) string {
	for _, m := range blockFieldPattern.FindAllSubmatch(payload, -1) {
		if strings.EqualFold(string(m[1]), label) {
			return strings.TrimSpace(string(m[2]))
		}
	}
	return ""
}

// isCloudflareChallenge detecta a página interativa de desafio, que chega como
// HTML em vez do JSON esperado.
func isCloudflareChallenge(resp *http.Response, payload []byte) bool {
	if resp.Header.Get("cf-mitigated") != "" {
		return true
	}
	if resp.StatusCode != http.StatusForbidden && resp.StatusCode != http.StatusServiceUnavailable {
		return false
	}
	lower := bytes.ToLower(payload)
	return bytes.Contains(lower, []byte("cf-browser-verification")) ||
		bytes.Contains(lower, []byte("just a moment"))
}
