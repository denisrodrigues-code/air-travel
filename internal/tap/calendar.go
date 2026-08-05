// Calendário de preços e matriz ida x volta.

package tap

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	http "github.com/bogdanfinn/fhttp"

	"airtravel/internal/models"
)

// Calendar consulta o calendário de melhores preços da rota.
//
// É o caminho preferencial de coleta: uma única requisição devolve um ano de
// preços diários (365 datas) e — ao contrário de /booking/availability/search —
// a rota não é bloqueada pelo WAF. Ver CLAUDE.md §4.
func (s *Scraper) Calendar(ctx context.Context, p models.SearchParams) (*models.CalendarResponse, []byte, error) {
	if err := p.Validate(); err != nil {
		return nil, nil, fmt.Errorf("parâmetros inválidos: %w", err)
	}

	body, err := json.Marshal(models.CalendarRequest{
		Origin:        p.Origin,
		Destination:   p.Destination,
		DepartureDate: p.DepartDate,
		TripType:      p.EffectiveTripType(),
		Market:        s.cfg.Market,
		Language:      s.cfg.Language,
		Adt:           p.Adults,
		CabinClass:    p.CabinClass,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("failed to marshal calendar request: %w", err)
	}

	raw, err := s.doAuthed(ctx, http.MethodPost, pathCalendar, nil, body)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to fetch calendar %s->%s: %w",
			p.Origin, p.Destination, err)
	}

	// A rota responde 200 com corpo vazio quando o payload não é aceito — por
	// exemplo se origin/destination forem enviados como arrays.
	if len(raw) == 0 {
		return nil, raw, fmt.Errorf("%w: calendário devolveu corpo vazio (payload rejeitado)", ErrAPIStatus)
	}

	var out models.CalendarResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, raw, fmt.Errorf("failed to decode calendar response: %w", err)
	}
	if len(out.Data.BestPriceForDates) == 0 {
		return &out, raw, fmt.Errorf("%w: calendário sem datas", ErrAPIStatus)
	}
	return &out, raw, nil
}

// CalendarReturns consulta os preços por data de retorno, fixada a data de ida.
//
// Complementa o Calendar: enquanto aquele dá o melhor preço por data de
// *partida*, este dá o preço total para cada data de *volta* possível — a
// dimensão que faltava para montar a matriz ida × volta.
//
// A rota também não é bloqueada pelo WAF.
func (s *Scraper) CalendarReturns(ctx context.Context, p models.SearchParams) (*models.CalendarReturnsResponse, []byte, error) {
	if err := p.Validate(); err != nil {
		return nil, nil, fmt.Errorf("parâmetros inválidos: %w", err)
	}

	departure, err := time.Parse(models.DateLayout, p.DepartDate)
	if err != nil {
		return nil, nil, fmt.Errorf("data de ida %q fora do formato DDMMYYYY: %w", p.DepartDate, err)
	}

	// origin/destination descrevem a perna de volta, por isso invertidos.
	body, err := json.Marshal(models.CalendarReturnsRequest{
		CabinClass:    p.CabinClass,
		Destination:   p.Origin,
		Market:        s.cfg.Market,
		Origin:        p.Destination,
		TripType:      "R",
		DepartureDate: departure.Format(models.ISODateLayout),
		PaxType:       "ADT",
	})
	if err != nil {
		return nil, nil, fmt.Errorf("failed to marshal calendar returns request: %w", err)
	}

	raw, err := s.doAuthed(ctx, http.MethodPost, pathCalendarReturns, nil, body)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to fetch return dates %s->%s on %s: %w",
			p.Origin, p.Destination, p.DepartDate, err)
	}
	if len(raw) == 0 {
		return nil, raw, fmt.Errorf("%w: calendarReturns devolveu corpo vazio", ErrAPIStatus)
	}

	var out models.CalendarReturnsResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, raw, fmt.Errorf("failed to decode calendar returns response: %w", err)
	}
	if len(out.Data.Returns) == 0 {
		return &out, raw, fmt.Errorf("%w: calendarReturns sem datas", ErrAPIStatus)
	}
	return &out, raw, nil
}
