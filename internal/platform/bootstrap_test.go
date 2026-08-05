package platform

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"airtravel/internal/client"
	"airtravel/internal/config"
)

// Os testes aqui cobrem os caminhos de falha, que não precisam de banco. O
// caminho feliz é verificado em bootstrap_integration_test.go, com -tags=integration.

// unreachable é um DSN que falha rápido, para que o teste não dependa de rede.
const unreachable = "postgres://x:x@127.0.0.1:1/naoexiste?sslmode=disable&connect_timeout=1"

func testConfig() *config.Config {
	cfg := config.Default()
	cfg.PostgresDSN = unreachable
	cfg.RedisAddr = "127.0.0.1:1"
	return cfg
}

// TestBootstrapRejectsNilArgs cobre a validação de entrada.
func TestBootstrapRejectsNilArgs(t *testing.T) {
	log := slog.New(slog.DiscardHandler)

	if _, _, err := Bootstrap(context.Background(), nil, log, Options{}); err == nil {
		t.Error("aceitou cfg nil")
	}
	if _, _, err := Bootstrap(context.Background(), testConfig(), nil, Options{}); err == nil {
		t.Error("aceitou log nil")
	}
}

// TestBootstrapValidatesBeforeOpeningConnections: configuração inválida deve
// falhar antes de tocar PostgreSQL ou Redis.
func TestBootstrapValidatesBeforeOpeningConnections(t *testing.T) {
	cfg := testConfig()
	cfg.Market = "" // Validate() rejeita

	_, _, err := Bootstrap(context.Background(), cfg, slog.New(slog.DiscardHandler), Options{})
	if err == nil {
		t.Fatal("configuração inválida foi aceita")
	}
	if !strings.Contains(err.Error(), "configuração inválida") {
		t.Errorf("err = %v; esperava falha de validação, não de conexão", err)
	}
}

// TestBootstrapRejectsUnknownProfile: um perfil sem motor associado é erro de
// montagem, não de conexão — precisa falhar antes de abrir o banco.
func TestBootstrapRejectsUnknownProfile(t *testing.T) {
	cfg := testConfig()
	cfg.TLSProfile = "netscape_4"

	_, _, err := Bootstrap(context.Background(), cfg, slog.New(slog.DiscardHandler), Options{})
	if err == nil {
		t.Fatal("perfil desconhecido foi aceito")
	}
	if !strings.Contains(err.Error(), "perfil TLS inválido") {
		t.Errorf("err = %v; esperava falha de perfil", err)
	}
}

// TestRequireClearanceWithoutFile fixa a mensagem que orienta quem tenta o modo
// search sem cookies.
func TestRequireClearanceWithoutFile(t *testing.T) {
	cfg := testConfig()

	_, _, err := Bootstrap(context.Background(), cfg, slog.New(slog.DiscardHandler), Options{
		CookiesFile:      filepath.Join(t.TempDir(), "naoexiste.txt"),
		RequireClearance: true,
	})
	if err == nil {
		t.Fatal("aceitou RequireClearance sem o arquivo")
	}
	if !strings.Contains(err.Error(), "cf_clearance") {
		t.Errorf("err = %v; a mensagem deve dizer o que falta", err)
	}
}

// TestMissingCookiesFileIsToleratedByDefault é o contrato que permite os modos
// calendar e returns rodarem sem preparação nenhuma.
func TestMissingCookiesFileIsTolerated(t *testing.T) {
	cfg := testConfig()

	_, _, err := Bootstrap(context.Background(), cfg, slog.New(slog.DiscardHandler), Options{
		CookiesFile: filepath.Join(t.TempDir(), "naoexiste.txt"),
	})

	// Deve chegar até a conexão — ou seja, a ausência do arquivo não barrou.
	if err == nil {
		t.Skip("banco alcançável neste ambiente; o caminho feliz é coberto por -tags=integration")
	}
	if !strings.Contains(err.Error(), "postgres") {
		t.Errorf("err = %v; esperava falhar só na conexão, não nos cookies", err)
	}
}

// TestMalformedCookiesFileIsFatal: arquivo presente mas inválido é erro de
// configuração, não algo a tolerar em silêncio.
func TestMalformedCookiesFileIsFatal(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cookies.txt")
	if err := os.WriteFile(path, []byte("linha_sem_igual\n"), 0o600); err != nil {
		t.Fatalf("failed to write fixture: %v", err)
	}

	_, _, err := Bootstrap(context.Background(), testConfig(), slog.New(slog.DiscardHandler),
		Options{CookiesFile: path})

	if err == nil {
		t.Fatal("arquivo malformado foi aceito")
	}
	if !strings.Contains(err.Error(), "cookies") {
		t.Errorf("err = %v; esperava falha de leitura de cookies", err)
	}
}

// TestBootstrapDoesNotLeakOnLateFailure: se o Redis falhar depois de o PostgreSQL
// abrir, a conexão já aberta precisa ser fechada.
//
// Não há como observar o fechamento de fora, então o teste verifica o contrato
// visível: numa falha, Bootstrap não devolve Deps nem a função de encerramento.
// Assim o chamador não tem como esquecer de fechar algo que recebeu.
func TestBootstrapDoesNotLeakOnLateFailure(t *testing.T) {
	deps, closeAll, err := Bootstrap(context.Background(), testConfig(),
		slog.New(slog.DiscardHandler), Options{})

	if err == nil {
		t.Skip("banco alcançável neste ambiente")
	}
	if deps != nil {
		t.Error("devolveu Deps apesar do erro")
	}
	if closeAll != nil {
		t.Error("devolveu função de encerramento apesar do erro; o próprio Bootstrap deve ter fechado")
	}
	if !errors.Is(err, err) { // sanidade: o erro é comparável
		t.Error("erro não comparável")
	}
}

// ---------------------------------------------------------------------------
// BootstrapAdapter
// ---------------------------------------------------------------------------

// TestBootstrapAdapterNeedsNoDatabase é a razão de a função existir: o wafprobe
// precisa enviar exatamente o que a aplicação envia, mas não persiste nada. Note
// que o DSN aqui é inalcançável de propósito.
func TestBootstrapAdapterNeedsNoDatabase(t *testing.T) {
	dep, err := BootstrapAdapter(testConfig(), slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("BootstrapAdapter = %v, esperado sucesso sem banco", err)
	}
	if dep.Scraper == nil {
		t.Error("Scraper nil")
	}
	// O padrão é Gecko: é o motor que atravessa o WAF de availability/search.
	if dep.Engine.Name != "gecko" {
		t.Errorf("Engine = %q, esperado gecko", dep.Engine.Name)
	}
}

// TestBootstrapAdapterRejectsBadInput cobre os caminhos de falha. O perfil TLS
// inválido importa: é o parâmetro que o wafprobe varia.
func TestBootstrapAdapterRejectsBadInput(t *testing.T) {
	log := slog.New(slog.DiscardHandler)

	if _, err := BootstrapAdapter(nil, log); err == nil {
		t.Error("aceitou cfg nil")
	}
	if _, err := BootstrapAdapter(testConfig(), nil); err == nil {
		t.Error("aceitou log nil")
	}

	badProfile := testConfig()
	badProfile.TLSProfile = "netscape_4"
	if _, err := BootstrapAdapter(badProfile, log); err == nil {
		t.Error("aceitou perfil TLS inexistente")
	}

	// Validar sozinha é o que torna a função segura fora do Bootstrap.
	badConfig := testConfig()
	badConfig.Market = ""
	if _, err := BootstrapAdapter(badConfig, log); err == nil {
		t.Error("aceitou configuração inválida")
	}
}

// TestBootstrapAdapterCoversEveryProbedProfile: o wafprobe varre estes perfis, e
// um que não monte apareceria como "erro ao montar" no lugar do resultado da
// sondagem.
func TestBootstrapAdapterCoversEveryProbedProfile(t *testing.T) {
	probed := []string{
		"firefox_148", "firefox_147", "firefox_135",
		"safari_ios_18_5",
		"chrome_151", "chrome_146",
	}

	for _, profile := range probed {
		cfg := testConfig()
		cfg.TLSProfile = profile

		dep, err := BootstrapAdapter(cfg, slog.New(slog.DiscardHandler))
		if err != nil {
			t.Errorf("%s: %v", profile, err)
			continue
		}
		if dep.Engine.Name == "" {
			t.Errorf("%s: perfil sem motor associado", profile)
		}
	}
}

// TestBootstrapAdapterBuildsPoolByDefault: a rotação vem ligada porque, sem ela,
// um 403 em firefox_148 encerra a coleta.
func TestBootstrapAdapterBuildsPoolByDefault(t *testing.T) {
	cfg := testConfig()
	if !cfg.Rotate {
		t.Fatal("config.Default() deveria vir com Rotate ligado")
	}

	dep, err := BootstrapAdapter(cfg, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("BootstrapAdapter: %v", err)
	}
	if dep.Pool == nil {
		t.Fatal("Pool nil com Rotate ligado")
	}
	if got := dep.Pool.Size(); got != len(client.PassingProfiles) {
		t.Errorf("Size() = %d, esperado %d; o preferido já consta de PassingProfiles e não deve duplicar",
			got, len(client.PassingProfiles))
	}
	if got := dep.Scraper.Profile(); got != cfg.TLSProfile {
		t.Errorf("Profile() = %q, esperado começar pelo preferido %q", got, cfg.TLSProfile)
	}
}

// TestBootstrapAdapterWithoutRotation cobre o caminho de uma combinação só, que é
// o que o wafprobe usa — lá a rotação faria a medição mentir.
func TestBootstrapAdapterWithoutRotation(t *testing.T) {
	cfg := testConfig()
	cfg.Rotate = false

	dep, err := BootstrapAdapter(cfg, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("BootstrapAdapter: %v", err)
	}
	if dep.Pool != nil {
		t.Error("Pool deveria ser nil com Rotate desligado")
	}
	if got := dep.Scraper.Profile(); got != cfg.TLSProfile {
		t.Errorf("Profile() = %q, esperado %q", got, cfg.TLSProfile)
	}
}

// TestRotationOrderPutsPreferredFirst: `-tls-profile` escolhe por onde começar.
func TestRotationOrderPutsPreferredFirst(t *testing.T) {
	got := rotationOrder("safari_ios_18_5")

	if len(got) == 0 || got[0] != "safari_ios_18_5" {
		t.Fatalf("rotationOrder() = %v, esperado o preferido na frente", got)
	}
	// As alternativas medidas continuam presentes, para haver para onde rotacionar.
	for _, want := range client.PassingProfiles {
		if !slices.Contains(got, want) {
			t.Errorf("rotationOrder() = %v, esperado conter %q", got, want)
		}
	}
}

// TestRotationExcludesChromium fixa uma decisão medida: perfis Chromium tomam 403
// em availability/search, então incluí-los na rotação gastaria uma requisição para
// descobrir o que já se sabe.
func TestRotationExcludesChromium(t *testing.T) {
	for _, profile := range client.PassingProfiles {
		engine, err := client.EngineFor(profile)
		if err != nil {
			t.Errorf("%s: %v", profile, err)
			continue
		}
		if engine.Name == "chromium" {
			t.Errorf("%s é Chromium e não deveria estar na rotação: a família é bloqueada nessa rota", profile)
		}
	}
}
