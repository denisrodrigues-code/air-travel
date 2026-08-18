package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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

// TestLoadCookiesAcceptsHeaderFormat: o jar copiado do DevTools vem numa linha
// só, no formato do cabeçalho Cookie.
//
// Regressão medida em 07/08/2026: o parser cortava no primeiro '=' da LINHA, e
// um jar assim virava um único cookie chamado `_vwo_uuid_v2` com todo o resto no
// valor. O cf_clearance não chegava ao fio, sem erro nenhum — o wafprobe anunciou
// "com o jar de ..." e mediu sem jar.
func TestLoadCookiesAcceptsHeaderFormat(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
	}{
		{"ponto e vírgula", "cf_clearance=abc; __cf_bm=def; httpECX=ghi\n"},
		{"só espaço", "cf_clearance=abc __cf_bm=def httpECX=ghi\n"},
		{"sem espaço após o ';'", "cf_clearance=abc;__cf_bm=def;httpECX=ghi\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := Default()
			if err := cfg.LoadCookiesFile(writeCookies(t, tc.body)); err != nil {
				t.Fatalf("erro inesperado: %v", err)
			}

			want := []Cookie{
				{Name: "cf_clearance", Value: "abc"},
				{Name: "__cf_bm", Value: "def"},
				{Name: "httpECX", Value: "ghi"},
			}
			if len(cfg.Cookies) != len(want) {
				t.Fatalf("len(Cookies) = %d, esperado %d: %+v", len(cfg.Cookies), len(want), cfg.Cookies)
			}
			for i, w := range want {
				if cfg.Cookies[i] != w {
					t.Errorf("Cookies[%d] = %+v, esperado %+v", i, cfg.Cookies[i], w)
				}
			}
			if !cfg.HasCookie("cf_clearance") {
				t.Error("cf_clearance não foi carregado — é exatamente a falha silenciosa que motivou o teste")
			}
		})
	}
}

// TestLoadCookiesHeaderFormatKeepsAwkwardValues: numa linha única, os
// separadores também aparecem DENTRO dos valores do jar real da TAP.
//
// Os dois casos são do jar capturado: o identificador do Adobe termina em '='
// de padding base64, e o OptanonConsent é uma query string inteira, com '=' e
// '&'. Cortar a linha pelos separadores truncaria os dois.
func TestLoadCookiesHeaderFormatKeepsAwkwardValues(t *testing.T) {
	const adobe = "CiYwNTg5Nzc0MDc1MTg3MTgzNzUzMDI5MTEwODgyMjEwODc0MTU3OVISCMTOsOD9MxABGAEqA1ZBNjAA8AHEzrDg_TM="
	const optanon = "isGpcEnabled=0&version=202502.1.0&groups=C0001%3A1%2CC0002%3A1"

	cfg := Default()
	path := writeCookies(t, "kndctr_identity="+adobe+"; OptanonConsent="+optanon+"; httpECX=ghi\n")
	if err := cfg.LoadCookiesFile(path); err != nil {
		t.Fatalf("erro inesperado: %v", err)
	}

	if len(cfg.Cookies) != 3 {
		t.Fatalf("len(Cookies) = %d, esperado 3: %+v", len(cfg.Cookies), cfg.Cookies)
	}
	if got := cfg.Cookies[0].Value; got != adobe {
		t.Errorf("o '=' final do base64 foi perdido:\n  got  %q\n  want %q", got, adobe)
	}
	if got := cfg.Cookies[1].Value; got != optanon {
		t.Errorf("a query string do OptanonConsent foi truncada:\n  got  %q\n  want %q", got, optanon)
	}
}

// TestLoadCookiesMixesFormats: o arquivo pode misturar as duas formas, porque
// quem edita à mão acrescenta uma linha nova em vez de reescrever a existente.
func TestLoadCookiesMixesFormats(t *testing.T) {
	cfg := Default()
	path := writeCookies(t, "# jar do navegador\n_ga=GA1.1.123; _fbp=fb.1.9\ncf_clearance=abc\n")
	if err := cfg.LoadCookiesFile(path); err != nil {
		t.Fatalf("erro inesperado: %v", err)
	}

	want := []string{"_ga", "_fbp", "cf_clearance"}
	if len(cfg.Cookies) != len(want) {
		t.Fatalf("len(Cookies) = %d, esperado %d: %+v", len(cfg.Cookies), len(want), cfg.Cookies)
	}
	for i, name := range want {
		if cfg.Cookies[i].Name != name {
			t.Errorf("Cookies[%d].Name = %q, esperado %q", i, cfg.Cookies[i].Name, name)
		}
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
	// Antes este teste fixava o literal "firefox_148", e virou obstáculo quando a
	// medição de 2026-08-06 mostrou que aquele perfil deixou de atravessar o WAF: o
	// padrão TEM de acompanhar a medição, então prendê-lo por nome aqui só produz
	// atrito. Quem afirma que o padrão é um perfil que passa é
	// TestDefaultProfileIsRegisteredAndPassing, em internal/client, que pode
	// importar config e o registro ao mesmo tempo — config não pode importar client.
	//
	// O que se checa aqui é a armadilha que este pacote pode ver sozinho: o nome do
	// perfil é a chave do registro, sempre MINÚSCULA. Usar o nome da variável Go da
	// biblioteca (`Firefox_135`) faz o pool falhar ao montar e a aplicação não subir.
	if cfg.TLSProfile == "" {
		t.Error("TLSProfile vazio; o padrão precisa nomear um perfil")
	}
	if cfg.TLSProfile != strings.ToLower(cfg.TLSProfile) {
		t.Errorf("TLSProfile = %q; as chaves do registro são minúsculas, e o nome da "+
			"variável Go da biblioteca não serve como chave", cfg.TLSProfile)
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

// TestClearanceAge extrai a idade do cf_clearance a partir do timestamp que o
// próprio valor do cookie embute.
//
// Existe porque um jar VENCIDO e um jar AUSENTE produzem o mesmo 403, e ambos são
// diferentes de bloqueio por volume — cuja ação é a oposta (esperar). Sem medir a
// idade, o diagnóstico não separa os três casos.
func TestClearanceAge(t *testing.T) {
	now := time.Unix(1786026963, 0)

	// O valor real capturado do navegador em 2026-08-06, emitido em 1786021712.
	const real = "t0UZ7BGCUjFYpe9qcVbFTqz05bCQUygavPBpyyNSquA-1786021712-1.2.1.1-zSSdZo3MtXl9_O_w"

	for _, tc := range []struct {
		name    string
		cookies []Cookie
		wantOK  bool
		wantAge time.Duration
	}{
		{
			name:    "valor real do navegador",
			cookies: []Cookie{{Name: "cf_clearance", Value: real}},
			wantOK:  true,
			wantAge: time.Duration(1786026963-1786021712) * time.Second,
		},
		{
			name:    "sem cf_clearance",
			cookies: []Cookie{{Name: "__cf_bm", Value: "x-1786021712-1"}},
			wantOK:  false,
		},
		{
			name:    "sem timestamp",
			cookies: []Cookie{{Name: "cf_clearance", Value: "semtimestamp"}},
			wantOK:  false,
		},
		{
			name:    "segundo campo não numérico",
			cookies: []Cookie{{Name: "cf_clearance", Value: "abc-naoehnumero-1.2"}},
			wantOK:  false,
		},
		{
			name:    "jar vazio",
			cookies: nil,
			wantOK:  false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := Default()
			cfg.Cookies = tc.cookies

			age, ok := cfg.ClearanceAge(now)
			if ok != tc.wantOK {
				t.Fatalf("ClearanceAge ok = %v, esperado %v", ok, tc.wantOK)
			}
			if ok && age != tc.wantAge {
				t.Errorf("ClearanceAge = %v, esperado %v", age, tc.wantAge)
			}
		})
	}
}

// TestClearanceTTLErrsOnTheShortSide fixa o LADO para o qual a constante deve
// errar, porque um valor exato não é obtenível.
//
// As duas medições de 2026-08-06 se contradizem: um jar de Chrome funcionou aos 17
// min, um de Brave já falhava aos ~16. Então a idade não é a única variável e
// nenhum limite descreve os dois casos — a primeira versão deste teste exigia
// `ClearanceTTL > 17min`, calibrada só com o jar de Chrome, e teria barrado a
// correção depois de o segundo jar aparecer.
//
// O que se pode afirmar é a POLÍTICA: errar curto. A mensagem só aparece num 403
// que já aconteceu, então sugerir recaptura para um jar válido custa uma linha;
// calar custa mandar esperar por algo que a espera não resolve.
func TestClearanceTTLErrsOnTheShortSide(t *testing.T) {
	// Abaixo do menor jar medido como MORTO: senão a mensagem cala justamente no
	// caso que a motivou.
	const menorMorto = 16 * time.Minute
	if ClearanceTTL > menorMorto {
		t.Errorf("ClearanceTTL = %v, acima do menor jar medido morto (%v) — o "+
			"diagnóstico calaria sobre um jar vencido", ClearanceTTL, menorMorto)
	}

	// E não tão curto que perca o sentido: abaixo de 1 min, todo 403 viraria
	// "recapture os cookies", inclusive os que não têm nada a ver com o jar.
	if ClearanceTTL < time.Minute {
		t.Errorf("ClearanceTTL = %v é curto demais: todo 403 viraria conselho de "+
			"recaptura, e a mensagem deixaria de discriminar", ClearanceTTL)
	}
}
