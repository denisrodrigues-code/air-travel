package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeCookies grava um arquivo temporário e devolve o caminho.
func writeCookies(t *testing.T, content string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "cookies.txt")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("failed to write fixture: %v", err)
	}
	return path
}

// TestLoadCookiesPreservesOrder protege a decisão de usar fatia em vez de map.
//
// Regressão: com map, a iteração aleatória do Go produzia um cabeçalho Cookie
// diferente a cada execução — um sinal de automação.
func TestLoadCookiesPreservesOrder(t *testing.T) {
	path := writeCookies(t, `# comentário ignorado

_ga=GA1.1.123
cf_clearance=abc
__cf_bm=def
   httpECX=ghi
`)

	cfg := Default()
	if err := cfg.LoadCookiesFile(path); err != nil {
		t.Fatalf("erro inesperado: %v", err)
	}

	want := []string{"_ga", "cf_clearance", "__cf_bm", "httpECX"}
	if len(cfg.Cookies) != len(want) {
		t.Fatalf("len(Cookies) = %d, esperado %d: %+v", len(cfg.Cookies), len(want), cfg.Cookies)
	}
	for i, name := range want {
		if cfg.Cookies[i].Name != name {
			t.Errorf("Cookies[%d].Name = %q, esperado %q", i, cfg.Cookies[i].Name, name)
		}
	}
	if got := cfg.Cookies[1].Value; got != "abc" {
		t.Errorf("valor de cf_clearance = %q, esperado abc", got)
	}
}

// TestLoadCookiesValueWithEquals: valores base64 e JSON contêm '=' e não podem
// ser truncados.
func TestLoadCookiesValueWithEquals(t *testing.T) {
	path := writeCookies(t, "cf_clearance=a=b=c\n__rtbh.uid=%7B%22id%22%3A%22x%22%7D\n")

	cfg := Default()
	if err := cfg.LoadCookiesFile(path); err != nil {
		t.Fatalf("erro inesperado: %v", err)
	}
	if got := cfg.Cookies[0].Value; got != "a=b=c" {
		t.Errorf("valor = %q, esperado a=b=c (o corte deve ser no primeiro '=')", got)
	}
}

// TestLoadCookiesMissingFileIsNotFatal fixa o contrato que permite os modos
// calendar e returns rodarem sem preparação nenhuma.
func TestLoadCookiesMissingFileIsNotFatal(t *testing.T) {
	cfg := Default()
	err := cfg.LoadCookiesFile(filepath.Join(t.TempDir(), "naoexiste.txt"))

	if !errors.Is(err, ErrCookiesFileMissing) {
		t.Errorf("err = %v, esperado ErrCookiesFileMissing", err)
	}
	if len(cfg.Cookies) != 0 {
		t.Errorf("Cookies = %+v, esperado vazio", cfg.Cookies)
	}
}

// TestLoadCookiesMalformedLine: uma linha sem '=' é erro, com o número da linha.
func TestLoadCookiesMalformedLine(t *testing.T) {
	path := writeCookies(t, "cf_clearance=abc\nlinha_sem_igual\n")

	cfg := Default()
	err := cfg.LoadCookiesFile(path)
	if err == nil {
		t.Fatal("linha malformada foi aceita")
	}
	if !strings.Contains(err.Error(), "linha 2") {
		t.Errorf("erro não indica a linha: %v", err)
	}
}

// TestValidateDoesNotRequireCookies: exigir cf_clearance era barreira inútil —
// as rotas de calendário respondem sem cookie algum.
func TestValidateDoesNotRequireCookies(t *testing.T) {
	cfg := Default()
	if err := cfg.Validate(); err != nil {
		t.Errorf("Validate() sem cookies = %v, esperado nil", err)
	}
	if err := cfg.RequireClearance(); !errors.Is(err, ErrNoCookies) {
		t.Errorf("RequireClearance() = %v, esperado ErrNoCookies", err)
	}

	cfg.Cookies = []Cookie{{Name: "cf_clearance", Value: "x"}}
	if err := cfg.RequireClearance(); err != nil {
		t.Errorf("RequireClearance() com o cookie = %v, esperado nil", err)
	}
	if !cfg.HasCookie("cf_clearance") || cfg.HasCookie("inexistente") {
		t.Error("HasCookie devolveu resultado incorreto")
	}
}

// TestValidateRejectsBadConfig cobre os campos obrigatórios.
func TestValidateRejectsBadConfig(t *testing.T) {
	tests := []struct {
		name  string
		mutar func(*Config)
	}{
		{"BaseURL vazia", func(c *Config) { c.BaseURL = "" }},
		{"sem ClientID", func(c *Config) { c.ClientID = "" }},
		{"sem ClientSecret", func(c *Config) { c.ClientSecret = "" }},
		{"sem Market", func(c *Config) { c.Market = "" }},
		{"sem Language", func(c *Config) { c.Language = "" }},
		{"Concurrency zero", func(c *Config) { c.Concurrency = 0 }},
		{"RPS zero", func(c *Config) { c.RequestsPerSecond = 0 }},
		{"RPS negativo", func(c *Config) { c.RequestsPerSecond = -1 }},
	}

	for _, tc := range tests {
		cfg := Default()
		tc.mutar(cfg)
		if err := cfg.Validate(); err == nil {
			t.Errorf("%s: aceito indevidamente", tc.name)
		}
	}
}

// TestDefaultIsUsable garante que o padrão passa na própria validação — é o que
// permite rodar sem configurar nada.
func TestDefaultIsUsable(t *testing.T) {
	cfg := Default()

	if err := cfg.Validate(); err != nil {
		t.Fatalf("Default() não passa em Validate(): %v", err)
	}
	if cfg.TLSProfile != "firefox_148" {
		t.Errorf("TLSProfile = %q; o padrão deve ser um perfil Gecko, que atravessa o WAF", cfg.TLSProfile)
	}
	if cfg.Market != "PT" || cfg.Language != "pt" {
		t.Errorf("mercado/idioma = %q/%q, esperado PT/pt", cfg.Market, cfg.Language)
	}
	if cfg.RawTTL <= 0 {
		t.Error("RawTTL deve ser positivo, senão o Redis descarta na hora")
	}
}

// TestEnvOverridesDefault cobre a leitura de variável de ambiente.
func TestEnvOverridesDefault(t *testing.T) {
	const dsn = "postgres://u:p@host:5432/db"
	t.Setenv("POSTGRES_DSN", dsn)
	t.Setenv("REDIS_ADDR", "redis:6379")

	cfg := Default()
	if cfg.PostgresDSN != dsn {
		t.Errorf("PostgresDSN = %q, esperado %q", cfg.PostgresDSN, dsn)
	}
	if cfg.RedisAddr != "redis:6379" {
		t.Errorf("RedisAddr = %q", cfg.RedisAddr)
	}
}
