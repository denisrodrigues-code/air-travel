// Package client constrói o cliente HTTP com fingerprint TLS de navegador.
package client

import (
	"fmt"
	"net/url"

	http "github.com/bogdanfinn/fhttp"
	"github.com/bogdanfinn/fhttp/cookiejar"
	tls_client "github.com/bogdanfinn/tls-client"
	"github.com/bogdanfinn/tls-client/profiles"

	"airtravel/internal/config"
)

// profileRegistry expõe por nome os perfis relevantes. As variantes _PSK não
// constam de profiles.MappedTLSClients, por isso o registro é explícito.
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
