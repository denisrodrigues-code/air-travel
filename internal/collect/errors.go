package collect

import "errors"

// Erros do caso de uso. São o vocabulário que os chamadores comparam com
// errors.Is — nem a camada HTTP nem o CLI precisam conhecer os sentinelas
// específicos de um provedor.
//
// Quem traduz é o adapter: `internal/tap` declara os seus sentinelas
// embrulhando estes, de modo que `errors.Is(err, collect.ErrBlocked)` funcione
// sem que ninguém saiba o que é um WAF ou um JA3.
//
// A direção é deliberada. O adapter depende da porta, não o contrário: assim
// trocar de provedor não toca nem o caso de uso nem a API.
var (
	// ErrBlocked indica recusa por proteção de borda (WAF, desafio interativo).
	// Não é erro do cliente: a requisição estava correta e foi barrada.
	ErrBlocked = errors.New("provedor bloqueou a requisição")

	// ErrRateLimited indica excesso de requisições ao provedor.
	ErrRateLimited = errors.New("limite de requisições do provedor atingido")

	// ErrUnauthorized indica credencial recusada pelo provedor.
	ErrUnauthorized = errors.New("credencial recusada pelo provedor")

	// ErrInvalidResponse indica resposta que chegou mas não é utilizável —
	// envelope com status de erro, corpo vazio, JSON ilegível.
	ErrInvalidResponse = errors.New("resposta inválida do provedor")

	// ErrInvalidParams indica entrada que não passa na validação de domínio.
	// Existe para que uma validação escapada da fronteira ainda resulte em 400,
	// não em 500.
	ErrInvalidParams = errors.New("parâmetros inválidos")
)
