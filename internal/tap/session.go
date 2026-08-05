// Sessão anônima: obtenção e renovação do JWT.

package tap

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	http "github.com/bogdanfinn/fhttp"

	"airtravel/internal/models"
)

// ---------------------------------------------------------------------------
// Autenticação
// ---------------------------------------------------------------------------

// Authenticate obtém um JWT anônimo em /bfm/rest/session/create.
func (s *Scraper) Authenticate(ctx context.Context) error {
	body, err := json.Marshal(models.SessionRequest{
		ClientID:     s.cfg.ClientID,
		ClientSecret: s.cfg.ClientSecret,
		ReferralID:   s.cfg.ReferralID,
		Market:       s.cfg.Market,
		Language:     s.cfg.Language,
		UserProfile:  json.RawMessage("null"),
		AppModule:    "0",
		IDOperation:  json.RawMessage("null"),
	})
	if err != nil {
		return fmt.Errorf("failed to marshal session request: %w", err)
	}

	raw, err := s.do(ctx, http.MethodPost, pathSessionCreate, nil, body, "")
	if err != nil {
		return fmt.Errorf("failed to authenticate: %w", err)
	}

	var out models.SessionResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return fmt.Errorf("failed to decode session response: %w", err)
	}
	if out.ID == "" {
		return fmt.Errorf("%w: resposta de sessão sem JWT (status %q)", ErrUnauthorized, out.Status)
	}

	exp, err := jwtExpiry(out.ID)
	if err != nil {
		// Sem exp legível, adota-se uma validade conservadora.
		s.log.WarnContext(ctx, "não foi possível ler o exp do JWT", "err", err)
		exp = time.Now().Add(30 * time.Minute)
	}

	s.mu.Lock()
	s.token, s.tokenExp = out.ID, exp
	s.mu.Unlock()

	s.log.InfoContext(ctx, "autenticado", "expira_em", exp.Format(time.RFC3339))
	return nil
}

// ensureToken garante um JWT válido, renovando quando necessário.
func (s *Scraper) ensureToken(ctx context.Context) (string, error) {
	s.mu.RLock()
	token, exp := s.token, s.tokenExp
	s.mu.RUnlock()

	if token != "" && time.Now().Before(exp.Add(-tokenRefreshMargin)) {
		return token, nil
	}
	if err := s.Authenticate(ctx); err != nil {
		return "", err
	}

	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.token, nil
}

// jwtExpiry extrai o claim exp sem validar a assinatura: aqui interessa apenas
// saber quando renovar.
func jwtExpiry(token string) (time.Time, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return time.Time{}, fmt.Errorf("JWT malformado: %d segmentos", len(parts))
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return time.Time{}, fmt.Errorf("failed to decode jwt payload: %w", err)
	}
	var claims struct {
		Exp int64 `json:"exp"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return time.Time{}, fmt.Errorf("failed to unmarshal jwt claims: %w", err)
	}
	if claims.Exp == 0 {
		return time.Time{}, errors.New("claim exp ausente")
	}
	return time.Unix(claims.Exp, 0), nil
}
