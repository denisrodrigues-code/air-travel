package collect

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"airtravel/internal/models"
)

// O Service é o Collector que o Runner orquestra. A afirmação em tempo de
// compilação evita que uma mudança de assinatura só apareça no wiring.
var _ Collector = (*Service)(nil)

// Service coleta no provedor e persiste nos dois destinos.
type Service struct {
	provider Provider
	treated  TreatedStore
	raw      RawStore
	log      *slog.Logger

	// market compõe as chaves canônicas. Fica aqui para que nenhum chamador
	// precise carregá-lo — antes da refatoração ele aparecia em 18 lugares.
	market string

	// resumeMaxAge é a idade máxima de uma coleta para que a retomada a considere
	// aproveitável. Zero retoma qualquer coleta, por antiga que seja.
	resumeMaxAge time.Duration

	// now é injetável para tornar os testes determinísticos.
	now func() time.Time
}

// Options parametriza o serviço.
type Options struct {
	Market string
	// ResumeMaxAge limita a idade do que a retomada aproveita. Zero desliga o
	// limite — ver TreatedStore.Exists.
	ResumeMaxAge time.Duration
	// Now, se nil, usa time.Now().UTC().
	Now func() time.Time
}

// New monta o serviço.
func New(provider Provider, treated TreatedStore, raw RawStore, log *slog.Logger, opts Options) (*Service, error) {
	if provider == nil || treated == nil || raw == nil {
		return nil, errors.New("provider, treated e raw são obrigatórios")
	}
	if opts.Market == "" {
		return nil, errors.New("Market é obrigatório")
	}

	now := opts.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}

	if opts.ResumeMaxAge < 0 {
		return nil, fmt.Errorf("ResumeMaxAge não pode ser negativa, obtido %s", opts.ResumeMaxAge)
	}

	return &Service{
		provider:     provider,
		treated:      treated,
		raw:          raw,
		log:          log,
		market:       opts.Market,
		resumeMaxAge: opts.ResumeMaxAge,
		now:          now,
	}, nil
}

// Market devolve o mercado configurado, que determina moeda e tarifas.
func (s *Service) Market() string { return s.market }

// resumeCutoff devolve o instante a partir do qual uma coleta ainda serve.
// O zero significa "qualquer idade", e é assim que as portas o interpretam.
func (s *Service) resumeCutoff() time.Time {
	if s.resumeMaxAge <= 0 {
		return time.Time{}
	}
	return s.now().Add(-s.resumeMaxAge)
}

// ---------------------------------------------------------------------------
// A política de persistência, num único lugar
// ---------------------------------------------------------------------------

// persist grava o bruto e o tratado, devolvendo a chave do bruto e os avisos.
//
// Duas regras, e é para tê-las escritas uma vez que este pacote existe:
//
//  1. O bruto vai PRIMEIRO. O registro tratado guarda a chave dele
//     (`searches.raw_key`), então gravar na ordem inversa deixaria a coluna
//     apontando para o vazio numa falha.
//
//  2. Falha de persistência NÃO descarta a captura. Cada coleta custa uma
//     consulta ao GDS da TAP (3 a 9 s); jogar o dado fora porque o Redis caiu
//     seria o pior dos dois mundos. A falha vira aviso, não erro.
//
// Antes da refatoração as duas cópias divergiam neste ponto: o orquestrador do
// CLI tratava a falha como erro do job, a API devolvia 200 com avisos. A da API
// era a correta e é a que ficou.
func (s *Service) persist(
	ctx context.Context,
	key string,
	raw []byte,
	saveTreated func(rawKey string, at time.Time) error,
) (rawKey string, at time.Time, warnings []string) {
	at = s.now()

	rawKey, err := s.raw.SaveRaw(ctx, key, at, raw)
	if err != nil {
		s.log.ErrorContext(ctx, "falha ao gravar resposta bruta", "chave", key, "err", err)
		warnings = append(warnings, "resposta bruta não foi gravada no Redis")
	}

	if err := saveTreated(rawKey, at); err != nil {
		s.log.ErrorContext(ctx, "falha ao gravar dados tratados", "chave", key, "err", err)
		warnings = append(warnings, "dados tratados não foram gravados no PostgreSQL")
	}

	return rawKey, at, warnings
}

// ---------------------------------------------------------------------------
// Casos de uso
// ---------------------------------------------------------------------------

// Search coleta voos e tarifas.
//
// Com resume, uma busca já persistida é devolvida como Skipped sem tocar a rede.
func (s *Service) Search(ctx context.Context, p models.SearchParams, resume bool) (SearchResult, error) {
	if err := p.Validate(); err != nil {
		return SearchResult{}, fmt.Errorf("%w: %w", ErrInvalidParams, err)
	}

	key := p.Key(s.market)
	result := SearchResult{Key: key}

	if resume {
		exists, err := s.treated.Exists(ctx, key.String(), s.resumeCutoff())
		if err != nil {
			return result, fmt.Errorf("failed to check resume state: %w", err)
		}
		if exists {
			result.Skipped = true
			return result, nil
		}
	}

	resp, raw, err := s.provider.Search(ctx, p)
	if err != nil {
		return result, err
	}

	rawKey, at, warnings := s.persist(ctx, key.String(), raw,
		func(rawKey string, at time.Time) error {
			return s.treated.SaveSearch(ctx, key, resp, rawKey, at)
		})

	result.Response, result.RawKey, result.ScrapedAt, result.Warnings = resp, rawKey, at, warnings
	return result, nil
}

// Calendar consulta o calendário de melhores preços.
//
// Uma requisição cobre ~365 datas, então a chave não inclui data — só rota,
// cabine e tipo de viagem.
func (s *Service) Calendar(ctx context.Context, p models.SearchParams, resume bool) (CalendarResult, error) {
	if err := p.Validate(); err != nil {
		return CalendarResult{}, fmt.Errorf("%w: %w", ErrInvalidParams, err)
	}

	key := p.CalendarKeyFor(s.market)
	result := CalendarResult{Key: key}

	if resume {
		exists, err := s.treated.CalendarExists(ctx, key.String(), s.resumeCutoff())
		if err != nil {
			return result, fmt.Errorf("failed to check resume state: %w", err)
		}
		if exists {
			result.Skipped = true
			return result, nil
		}
	}

	resp, raw, err := s.provider.Calendar(ctx, p)
	if err != nil {
		return result, err
	}

	rawKey, at, warnings := s.persist(ctx, key.String(), raw,
		func(rawKey string, at time.Time) error {
			return s.treated.SaveCalendar(ctx, key, resp, rawKey, at)
		})

	result.Response, result.RawKey, result.ScrapedAt, result.Warnings = resp, rawKey, at, warnings
	return result, nil
}

// Returns consulta a matriz ida × volta para uma data de ida.
func (s *Service) Returns(ctx context.Context, p models.SearchParams, resume bool) (ReturnsResult, error) {
	if err := p.Validate(); err != nil {
		return ReturnsResult{}, fmt.Errorf("%w: %w", ErrInvalidParams, err)
	}

	key := p.ReturnsKeyFor(s.market)
	result := ReturnsResult{Key: key}

	if resume {
		exists, err := s.treated.ReturnsExists(ctx, key.String(), s.resumeCutoff())
		if err != nil {
			return result, fmt.Errorf("failed to check resume state: %w", err)
		}
		if exists {
			result.Skipped = true
			return result, nil
		}
	}

	resp, raw, err := s.provider.CalendarReturns(ctx, p)
	if err != nil {
		return result, err
	}

	rawKey, at, warnings := s.persist(ctx, key.String(), raw,
		func(rawKey string, at time.Time) error {
			return s.treated.SaveReturns(ctx, key, resp, rawKey, at)
		})

	result.Response, result.RawKey, result.ScrapedAt, result.Warnings = resp, rawKey, at, warnings
	return result, nil
}
