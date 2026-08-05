package tap

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"airtravel/internal/collect"
)

// TestSentinelsTranslateToUseCase é a garantia da Etapa 3: cada falha do adapter
// é reconhecível pelo vocabulário do caso de uso, sem que o chamador conheça
// WAF, Cloudflare ou JA3.
//
// Se um sentinela novo for acrescentado sem embrulhar um erro de collect, a
// camada HTTP o mapearia para 500 em silêncio. Este teste falha em vez disso.
func TestSentinelsTranslateToUseCase(t *testing.T) {
	tests := []struct {
		name     string
		sentinel error
		want     error
	}{
		{"ErrAccessDenied", ErrAccessDenied, collect.ErrBlocked},
		{"ErrCloudflareChallenge", ErrCloudflareChallenge, collect.ErrBlocked},
		{"ErrRateLimited", ErrRateLimited, collect.ErrRateLimited},
		{"ErrUnauthorized", ErrUnauthorized, collect.ErrUnauthorized},
		{"ErrAPIStatus", ErrAPIStatus, collect.ErrInvalidResponse},
	}

	for _, tc := range tests {
		if !errors.Is(tc.sentinel, tc.want) {
			t.Errorf("%s não é reconhecido como %v", tc.name, tc.want)
		}

		// O sentinela específico continua comparável — é o que permite decisões
		// de política dentro do adapter (403 do WAF é terminal, 429 tem backoff).
		if !errors.Is(tc.sentinel, tc.sentinel) {
			t.Errorf("%s deixou de ser comparável consigo mesmo", tc.name)
		}

		// E sobrevive ao embrulho que os pontos de chamada fazem.
		wrapped := fmt.Errorf("failed to search LIS->RIO: %w", tc.sentinel)
		if !errors.Is(wrapped, tc.want) {
			t.Errorf("%s perde a tradução ao ser embrulhado", tc.name)
		}
		if !errors.Is(wrapped, tc.sentinel) {
			t.Errorf("%s perde a identidade ao ser embrulhado", tc.name)
		}
	}
}

// TestSentinelsAreDistinct evita que dois erros distintos colapsem no mesmo
// sentinela do caso de uso por descuido de copiar e colar.
func TestSentinelsAreDistinct(t *testing.T) {
	// Bloqueio e credencial recusada exigem reações opostas: o primeiro pede
	// trocar de perfil TLS, o segundo renovar o token.
	if errors.Is(ErrAccessDenied, collect.ErrUnauthorized) {
		t.Error("ErrAccessDenied confundido com credencial recusada")
	}
	if errors.Is(ErrUnauthorized, collect.ErrBlocked) {
		t.Error("ErrUnauthorized confundido com bloqueio")
	}
	if errors.Is(ErrRateLimited, collect.ErrBlocked) {
		t.Error("ErrRateLimited confundido com bloqueio")
	}
}

// TestBlockDetailsSurvivesTranslation confirma que a mensagem útil da página de
// bloqueio (geolocalização, IP, Ray ID) continua chegando ao cliente.
func TestBlockDetailsSurvivesTranslation(t *testing.T) {
	err := fmt.Errorf("%w: HTTP 403 em /rota (Geolocation: BR | ID: abc123)", ErrAccessDenied)

	if !errors.Is(err, collect.ErrBlocked) {
		t.Fatal("perdeu a tradução")
	}
	for _, want := range []string{"Geolocation: BR", "ID: abc123", "WAF da TAP"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("mensagem perdeu %q: %s", want, err)
		}
	}
}
