// Package config carrega e valida a configuração do scraper.
package config

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"time"
)

// Valores públicos observados no bundle do frontend do booking.flytap.com.
// O clientId é o mesmo valor que aparece no claim "sub" do JWT anônimo.
const (
	DefaultBaseURL      = "https://booking.flytap.com"
	DefaultClientID     = "-bqBinBiHz4Yg+87BN+PU3TaXUWyRrn1T/iV/LjxgeSA="
	DefaultClientSecret = "DxKLkFeWzANc4JSIIarjoPSr6M+cXv1rcqWry2QV2Azr5EutGYR/oJ79IT3fMR+qM5H/RArvIPtyquvjHebM1Q=="
	DefaultReferralID   = "h7g+cmbKWJ3XmZajrMhyUpp9.cms35"

	// DefaultTLSProfile é Firefox porque é a família que atravessa o WAF em
	// /bfm/rest/booking/availability/search — medido, ver CLAUDE.md §4.
	//
	// Perfis Chromium recebem 403 nessa rota mesmo com o JA4 idêntico ao do
	// Chrome real: o problema é a coerência entre TLS e client hints, que o
	// Gecko não tem por não anunciar client hint nenhum.
	DefaultTLSProfile = "firefox_148"
)

// Cookie é um par nome/valor preservando a ordem de leitura.
type Cookie struct {
	Name  string
	Value string
}

// Config reúne todos os parâmetros de execução do scraper.
type Config struct {
	BaseURL  string
	ProxyURL string

	// Credenciais do cliente anônimo usadas em /bfm/rest/session/resetValues.
	ClientID     string
	ClientSecret string
	ReferralID   string

	// Mercado e idioma determinam a moeda e os textos devolvidos pela API.
	Market   string
	Language string

	// TLSProfile é a chave do perfil em bogdanfinn/tls-client/profiles.
	TLSProfile string

	// Cookies pré-semeados no jar, na ordem em que aparecem no arquivo.
	//
	// Opcionais para os modos calendar e returns: verificou-se que essas rotas
	// respondem 200 sem cookie algum. Só o modo search precisa de cf_clearance
	// — e mesmo com ele o WAF bloqueia (ver CLAUDE.md §4).
	//
	// A ordem é preservada de propósito: um map produziria uma sequência
	// diferente a cada execução e, com ela, um cabeçalho Cookie instável.
	Cookies []Cookie

	// AcceptLang é independente do motor.
	//
	// User-Agent, client hints e ordem de cabeçalhos NÃO ficam aqui: são
	// derivados do TLSProfile por client.EngineFor, justamente para que não seja
	// possível configurar uma combinação incoerente — a incoerência era o que
	// fazia o WAF recusar os perfis Chromium.
	AcceptLang string

	// Rotate liga a troca automática de fingerprint quando o WAF recusa.
	//
	// Com uma combinação só, um 403 encerra a coleta. A lista de alternativas NÃO
	// fica aqui: é composta em internal/platform a partir de client.PassingProfiles,
	// porque config não pode importar client (seria ciclo) e duplicar a lista
	// deixaria as duas divergirem.
	Rotate bool
	// RotationCooldown é quanto tempo uma combinação recusada fica de fora. Zero
	// usa o padrão de client.DefaultCooldown.
	RotationCooldown time.Duration

	// Controle de carga.
	RequestsPerSecond float64
	Burst             int
	Concurrency       int
	Timeout           time.Duration
	MaxRetries        int
	// RetryBackoff é a espera antes da segunda tentativa; dobra a cada repetição.
	// Configurável para que os testes não durmam segundos reais.
	RetryBackoff time.Duration

	// Persistência: PostgreSQL guarda os dados tratados, Redis a resposta bruta.
	PostgresDSN string
	RedisAddr   string
	RedisPass   string
	RedisDB     int
	// RawTTL define por quanto tempo a resposta bruta fica no Redis.
	RawTTL time.Duration

	// ResumeMaxAge é a idade máxima de uma coleta para que a retomada a aproveite.
	//
	// Não é medição, é escolha: preço de passagem muda todo dia, então 24 h é o
	// ponto em que "já coletei isso" deixa de ser uma boa razão para não coletar
	// de novo. Zero retoma qualquer coleta, por antiga que seja — que era o
	// comportamento anterior e tornava a segunda execução de `./run.sh calendar`
	// um no-op permanente, porque a calendar_key não inclui data.
	ResumeMaxAge time.Duration
}

// Default devolve uma configuração pronta para uso, espelhando exatamente os
// valores observados na captura do navegador.
func Default() *Config {
	return &Config{
		BaseURL:           DefaultBaseURL,
		ClientID:          DefaultClientID,
		ClientSecret:      DefaultClientSecret,
		ReferralID:        DefaultReferralID,
		Market:            "PT",
		Language:          "pt",
		TLSProfile:        DefaultTLSProfile,
		Rotate:            true,
		AcceptLang:        "pt-PT,pt;q=0.9,en-US;q=0.8,en;q=0.7",
		RequestsPerSecond: 0.5,
		Burst:             1,
		Concurrency:       3,
		Timeout:           60 * time.Second,
		MaxRetries:        3,
		RetryBackoff:      2 * time.Second,
		PostgresDSN:       envOr("POSTGRES_DSN", "postgres://airtravel:airtravel@localhost:5432/airtravel?sslmode=disable"),
		RedisAddr:         envOr("REDIS_ADDR", "localhost:6379"),
		RedisPass:         os.Getenv("REDIS_PASSWORD"),
		RedisDB:           0,
		RawTTL:            7 * 24 * time.Hour,
		ResumeMaxAge:      24 * time.Hour,
	}
}

// envOr devolve a variável de ambiente ou o padrão informado.
func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// ErrNoCookies indica ausência do cookie cf_clearance. Relevante apenas para o
// modo search; as rotas de calendário dispensam cookies.
var ErrNoCookies = errors.New("cookie cf_clearance ausente")

// ErrCookiesFileMissing indica que o arquivo de cookies não existe.
var ErrCookiesFileMissing = errors.New("arquivo de cookies inexistente")

// Validate garante que a configuração é utilizável antes de qualquer I/O.
//
// Não exige cookies: as rotas de calendário funcionam sem eles.
func (c *Config) Validate() error {
	if c.BaseURL == "" {
		return errors.New("BaseURL vazia")
	}
	if c.ClientID == "" || c.ClientSecret == "" {
		return errors.New("ClientID e ClientSecret são obrigatórios")
	}
	if c.Market == "" || c.Language == "" {
		return errors.New("Market e Language são obrigatórios")
	}
	if c.Concurrency < 1 {
		return fmt.Errorf("Concurrency deve ser >= 1, obtido %d", c.Concurrency)
	}
	if c.RequestsPerSecond <= 0 {
		return fmt.Errorf("RequestsPerSecond deve ser > 0, obtido %f", c.RequestsPerSecond)
	}
	return nil
}

// RequireClearance exige o cf_clearance. Chamado só pelo modo search, que é a
// única rota protegida pelo WAF.
func (c *Config) RequireClearance() error {
	if !c.HasCookie("cf_clearance") {
		return ErrNoCookies
	}
	return nil
}

// HasCookie informa se um cookie foi carregado.
func (c *Config) HasCookie(name string) bool {
	return slices.ContainsFunc(c.Cookies, func(k Cookie) bool { return k.Name == name })
}

// LoadCookiesFile lê um arquivo de cookies no formato "nome=valor" por linha.
// Linhas vazias e iniciadas por '#' são ignoradas. É o caminho prático para
// transferir cf_clearance/__cf_bm de um navegador real para o scraper.
//
// A ausência do arquivo não é erro: devolve ErrCookiesFileMissing, que o
// chamador trata conforme o modo. Assim os modos calendar e returns rodam sem
// nenhuma preparação.
func (c *Config) LoadCookiesFile(path string) error {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("%w: %s", ErrCookiesFileMissing, path)
	}
	if err != nil {
		return fmt.Errorf("failed to open cookies file %q: %w", path, err)
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for line := 1; sc.Scan(); line++ {
		text := strings.TrimSpace(sc.Text())
		if text == "" || strings.HasPrefix(text, "#") {
			continue
		}
		name, value, ok := strings.Cut(text, "=")
		if !ok {
			return fmt.Errorf("cookies file %q linha %d: esperado 'nome=valor'", path, line)
		}
		c.Cookies = append(c.Cookies, Cookie{
			Name:  strings.TrimSpace(name),
			Value: strings.TrimSpace(value),
		})
	}
	if err := sc.Err(); err != nil {
		return fmt.Errorf("failed to read cookies file %q: %w", path, err)
	}
	return nil
}
