// Package client constrói o cliente HTTP com fingerprint TLS de navegador.
package client

import (
	"fmt"
	"net/url"
	"sort"

	http "github.com/bogdanfinn/fhttp"
	"github.com/bogdanfinn/fhttp/cookiejar"
	tls_client "github.com/bogdanfinn/tls-client"
	"github.com/bogdanfinn/tls-client/profiles"
	tls "github.com/bogdanfinn/utls"

	"airtravel/internal/config"
)

// profileRegistry expõe por nome os perfis relevantes.
//
// O registro é explícito, e não derivado de profiles.MappedTLSClients, porque a
// chave daquele mapa não é `strings.ToLower` do nome da variável em 34 das 79
// entradas: as variantes PSK mantêm o sufixo maiúsculo (`chrome_146_PSK`) e
// alguns perfis mudam de nome por completo (`CloudflareCustom` → `cloudscraper`).
// Derivar os nomes convidaria a esse erro; aqui a normalização é nossa.
var profileRegistry = map[string]profiles.ClientProfile{
	"chrome_131":     profiles.Chrome_131,
	"chrome_133":     profiles.Chrome_133,
	"chrome_133_psk": profiles.Chrome_133_PSK,
	"chrome_144":     profiles.Chrome_144,
	"chrome_144_psk": profiles.Chrome_144_PSK,
	"chrome_146":     profiles.Chrome_146,
	"chrome_146_psk": profiles.Chrome_146_PSK,
	// Perfil próprio: Chrome_144 + os ML-DSA do Chrome 151. Ver
	// profile_chrome151.go.
	"chrome_151": chrome151,

	// Família Gecko e WebKit: relevantes porque não anunciam client hints, o
	// que dá menos margem para incoerência entre TLS e cabeçalhos.
	"firefox_148":     profiles.Firefox_148,
	"firefox_147":     profiles.Firefox_147,
	"firefox_135":     profiles.Firefox_135,
	"safari_ios_18_5": profiles.Safari_IOS_18_5,

	// Os dois Safari mais recentes da biblioteca depois do 18.5 acima.
	//
	// Medido offline (ver TestSafariProfilesMeasuredRelations): o
	// Safari_IOS_26_0 é o ÚNICO Safari novo com ClientHello distinto — deixa de
	// enviar a extensão de padding. Já o Safari_IOS_18_0 é byte a byte igual ao
	// 18.5 já registrado, então o que ele acrescenta é só uma identidade HTTP
	// diferente, não um fingerprint TLS diferente. O mesmo vale para
	// Safari_16_0 e Safari_15_6_1, que também são idênticos ao 18.5.
	"safari_ios_26_0": profiles.Safari_IOS_26_0,
	"safari_ios_18_0": profiles.Safari_IOS_18_0,

	// Não há perfil de Brave aqui, e a razão é medida: o ClientHello do Brave 151
	// É o do Chrome 151 — capture de 2026-08-06, JA4 idêntico byte a byte na lista
	// crua. Um `brave_151` seria o spec do chrome_151 com outra identidade HTTP,
	// e as duas medições que se fez dele não mostraram ganho: passou exatamente
	// onde o chrome_151 passa e falhou onde ele falha. Ver CLAUDE.md §4.
	//
	// Os perfis `Brave_146`/`Brave_146_PSK` da biblioteca também não constam:
	// medidos não reproduzindo o navegador que nomeiam, e recusados na rota search
	// até com o cf_clearance legítimo do próprio Brave, enquanto o chrome_151
	// passou com aquele mesmo jar.

	// Opera: as duas versões mais recentes da biblioteca.
	//
	// Duas advertências medidas. Primeira: Opera_89, _90 e _91 têm ClientHello
	// IDÊNTICO entre si e igual ao do Chrome_103/105 — são todos Chromium da
	// era 2022, então acrescentar duas versões diversifica o User-Agent, não o
	// fingerprint TLS. Segunda: o spec deles NÃO sai por GetClientHelloSpec(),
	// que devolve "please implement this method"; o handshake funciona porque o
	// utls resolve por uma tabela interna. Use specFor() para derivá-los.
	"opera_91": profiles.Opera_91,
	"opera_90": profiles.Opera_90,
}

// ProfileNames devolve os nomes de todos os perfis registrados, ordenados.
//
// Existe para que ferramentas de medição não mantenham a própria lista: o
// cmd/wafprobe trazia seis nomes fixos contra os dezoito do registro, então
// "medir todos os perfis" media um terço deles e não dizia nada sobre o resto.
// A ordenação é para a saída ser comparável entre execuções.
func ProfileNames() []string {
	names := make([]string, 0, len(profileRegistry))
	for name := range profileRegistry {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// specFor deriva o ClientHelloSpec de um perfil replicando o fallback que o
// próprio utls faz no handshake (utls/u_parrots.go:3239).
//
// Existe porque ClientProfile.GetClientHelloSpec() falha nos perfis herdados —
// Opera_89/90/91, Chrome_103..109, Safari_16_0 e outros são declarados com
// tls.EmptyClientHelloSpecFactory, um stub que só devolve erro. Eles funcionam
// em rede porque applyPresetByID cai em UTLSIdToSpec, uma tabela interna que os
// cobre. Sem esta função, um teste byte a byte no estilo de
// profile_chrome151_test.go simplesmente não consegue inspecionar o Opera.
func specFor(p profiles.ClientProfile) (tls.ClientHelloSpec, error) {
	spec, err := p.GetClientHelloSpec()
	if err == nil {
		return spec, nil
	}

	spec, fallbackErr := tls.UTLSIdToSpec(p.GetClientHelloId())
	if fallbackErr != nil {
		return spec, fmt.Errorf("failed to derive ClientHelloSpec (SpecFactory: %v): %w", err, fallbackErr)
	}
	return spec, nil
}

// Options parametriza a construção do cliente.
type Options struct {
	ProxyURL       string
	ProfileName    string
	TimeoutSeconds int
	// RandomExtensionOrder embaralha a ordem das extensões TLS.
	//
	// Mantido desligado por padrão: ao ativá-lo, o tls-client v1.15.1 acrescenta
	// a extensão 0xca34, que o Chrome real não envia. O resultado observado foi
	// JA4 t13d1517h2 (15 extensões listadas) contra t13d1516h2 (14) do
	// navegador — uma divergência de impressão digital, justamente o contrário
	// do pretendido.
	RandomExtensionOrder bool
}

// New devolve um cliente com cookie jar próprio e o fingerprint TLS pedido.
func New(opts Options) (tls_client.HttpClient, *cookiejar.Jar, error) {
	profile, ok := profileRegistry[opts.ProfileName]
	if !ok {
		return nil, nil, fmt.Errorf("perfil TLS desconhecido %q", opts.ProfileName)
	}

	timeout := opts.TimeoutSeconds
	if timeout <= 0 {
		timeout = 30
	}

	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create cookie jar: %w", err)
	}

	clientOpts := []tls_client.HttpClientOption{
		tls_client.WithTimeoutSeconds(timeout),
		tls_client.WithClientProfile(profile),
		tls_client.WithNotFollowRedirects(),
		tls_client.WithCookieJar(jar),
		// O tráfego capturado do navegador é todo HTTP/2; desativar o H3 evita
		// que uma negociação divergente apareça na impressão digital.
		tls_client.WithDisableHttp3(),
	}

	if opts.RandomExtensionOrder {
		clientOpts = append(clientOpts, tls_client.WithRandomTLSExtensionOrder())
	}

	if opts.ProxyURL != "" {
		if _, err := url.Parse(opts.ProxyURL); err != nil {
			return nil, nil, fmt.Errorf("failed to parse proxy url %q: %w", opts.ProxyURL, err)
		}
		clientOpts = append(clientOpts, tls_client.WithProxyUrl(opts.ProxyURL))
	}

	c, err := tls_client.NewHttpClient(tls_client.NewNoopLogger(), clientOpts...)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create tls client: %w", err)
	}
	return c, jar, nil
}

// SeedCookies injeta cookies obtidos de um navegador real no jar. É o único
// caminho para levar cf_clearance/__cf_bm para dentro do scraper.
//
// A ordem da fatia é respeitada para que o cabeçalho Cookie saia igual entre
// execuções — um cabeçalho que muda de ordem a cada corrida é sinal de bot.
func SeedCookies(jar *cookiejar.Jar, baseURL string, cookies []config.Cookie) error {
	if len(cookies) == 0 {
		return nil
	}

	u, err := url.Parse(baseURL)
	if err != nil {
		return fmt.Errorf("failed to parse base url %q: %w", baseURL, err)
	}

	list := make([]*http.Cookie, 0, len(cookies))
	for _, c := range cookies {
		list = append(list, &http.Cookie{
			Name:   c.Name,
			Value:  c.Value,
			Path:   "/",
			Domain: u.Hostname(),
		})
	}
	jar.SetCookies(u, list)
	return nil
}

// DecompressBody replica o tratamento que o wrapper CFFI do tls-client faz:
// como o Accept-Encoding é definido manualmente, o fhttp não descomprime a
// resposta automaticamente.
func DecompressBody(resp *http.Response) {
	if resp == nil || resp.Body == nil || resp.Uncompressed {
		return
	}
	encoding := resp.Header.Get("Content-Encoding")
	if encoding == "" {
		return
	}
	resp.Body = http.DecompressBodyByType(resp.Body, encoding)
	resp.Uncompressed = true
	resp.ContentLength = -1
}
