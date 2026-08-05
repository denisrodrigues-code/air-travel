// Package api expõe a coleta e o histórico por HTTP, com OpenAPI embutido.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	"airtravel/internal/collect"
	"airtravel/internal/storage"
)

// Problem é o corpo de erro devolvido pela API, no espírito do RFC 9457.
type Problem struct {
	// Status repete o código HTTP, para clientes que só têm o corpo.
	Status int `json:"status"`
	// Code é um identificador estável, adequado para lógica no cliente.
	Code string `json:"code"`
	// Detail explica o caso concreto.
	Detail string `json:"detail"`
}

// Erros de validação da própria API.
var (
	errBadRequest = errors.New("requisição inválida")
)

// statusFor traduz um erro em código HTTP e identificador estável.
//
// A tradução vive aqui, e não nos handlers, para que a política seja única: um
// bloqueio do provedor é sempre 502 (falha upstream, não do cliente) e um limite
// de requisições é sempre 429.
//
// Compara apenas com os erros de collect. Esta camada não conhece WAF, JA3 nem
// Cloudflare — o adapter é que traduz as suas falhas para esse vocabulário.
func statusFor(err error) (int, string) {
	switch {
	case errors.Is(err, storage.ErrNotFound):
		return http.StatusNotFound, "not_found"

	// Validação da fronteira e do domínio produzem o mesmo status: uma entrada
	// que escapou de toParams e foi barrada pelo serviço ainda é erro do cliente.
	case errors.Is(err, errBadRequest), errors.Is(err, collect.ErrInvalidParams):
		return http.StatusBadRequest, "bad_request"

	// O cliente não errou: o provedor recusou a nossa requisição.
	case errors.Is(err, collect.ErrBlocked):
		return http.StatusBadGateway, "upstream_blocked"

	case errors.Is(err, collect.ErrRateLimited):
		return http.StatusTooManyRequests, "upstream_rate_limited"

	case errors.Is(err, collect.ErrUnauthorized):
		return http.StatusBadGateway, "upstream_unauthorized"

	case errors.Is(err, collect.ErrInvalidResponse):
		return http.StatusBadGateway, "upstream_invalid_response"

	case errors.Is(err, context.Canceled):
		// 499: o cliente desistiu antes da resposta.
		return 499, "client_closed_request"

	case errors.Is(err, context.DeadlineExceeded):
		return http.StatusGatewayTimeout, "upstream_timeout"

	default:
		return http.StatusInternalServerError, "internal_error"
	}
}

// writeJSON serializa v com o status informado.
func writeJSON(w http.ResponseWriter, log *slog.Logger, status int, v any) {
	w.Header().Set("content-type", "application/json; charset=utf-8")
	w.WriteHeader(status)

	if v == nil {
		return
	}
	if err := json.NewEncoder(w).Encode(v); err != nil {
		// O status já foi escrito; só resta registrar.
		log.Error("falha ao serializar resposta", "err", err)
	}
}

// writeError responde com um Problem derivado do erro.
func writeError(w http.ResponseWriter, log *slog.Logger, err error) {
	status, code := statusFor(err)

	if status >= http.StatusInternalServerError {
		log.Error("erro ao atender requisição", "err", err, "status", status)
	} else {
		log.Warn("requisição recusada", "err", err, "status", status)
	}

	writeJSON(w, log, status, Problem{
		Status: status,
		Code:   code,
		Detail: err.Error(),
	})
}

// badRequest embrulha uma mensagem de validação como erro de cliente.
func badRequest(format string, args ...any) error {
	return fmt.Errorf("%w: %s", errBadRequest, fmt.Sprintf(format, args...))
}
