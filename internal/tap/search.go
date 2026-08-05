// Busca de disponibilidade e o aquecimento que a antecede.

package tap

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"

	http "github.com/bogdanfinn/fhttp"

	"airtravel/internal/models"
)

// buildRequest converte os parâmetros no corpo exato que o frontend envia.
//
// Os literais "string" em cmsId/session e a repetição de returnDate no modo
// somente-ida reproduzem fielmente o payload capturado do navegador.
func (s *Scraper) buildRequest(p models.SearchParams) models.SearchRequest {
	tripType := p.EffectiveTripType()
	oneWay := tripType == "O"

	returnDate := p.ReturnDate
	if returnDate == "" {
		returnDate = p.DepartDate
	}

	pax := p.Pax()
	totalSeats := p.TotalSeats()

	return models.SearchRequest{
		Adt:                  p.Adults,
		AirlineID:            "TP",
		BfmModule:            "BFM_BOOKING",
		C14:                  0,
		CabinClass:           p.CabinClass,
		ChangeReturn:         false,
		ChannelDetectionName: "",
		Chd:                  p.Children,
		CmsID:                "string",
		Communities:          []string{},
		DepartureDate:        []string{p.DepartDate},
		Destination:          []string{p.Destination},
		Groups:               []string{},
		Inf:                  p.Infants,
		Language:             s.cfg.Language,
		Market:               s.cfg.Market,
		MultiCityTripType:    false,
		NumSeat:              totalSeats,
		NumSeats:             totalSeats,
		OneWay:               oneWay,
		Origin:               []string{p.Origin},
		Passengers:           pax,
		PaxSearch:            pax,
		PermittedCabins:      []string{},
		PreferredCarrier:     []string{},
		Promocode:            "",
		PromotionID:          "",
		ReturnDate:           returnDate,
		RoundTripType:        !oneWay,
		SearchPoint:          true,
		Session:              "string",
		TripType:             tripType,
		ValidTripType:        true,
		Student:              false,
		Resident:             false,
		Yth:                  p.Youths,
	}
}

// Search executa uma busca e devolve a resposta decodificada junto com o JSON
// bruto — este último vai para o Redis e serve ao powhttp_validate_schema.
func (s *Scraper) Search(ctx context.Context, p models.SearchParams) (*models.SearchResponse, []byte, error) {
	if err := p.Validate(); err != nil {
		return nil, nil, fmt.Errorf("parâmetros de busca inválidos: %w", err)
	}

	body, err := json.Marshal(s.buildRequest(p))
	if err != nil {
		return nil, nil, fmt.Errorf("failed to marshal search request: %w", err)
	}

	// O navegador nunca chama a busca isoladamente: ele consulta as regras de
	// passageiros e valida a rota antes, na mesma conexão. Reproduzir essa
	// sequência mantém a sessão do backend no mesmo estado.
	if err := s.warmup(ctx, p); err != nil {
		s.log.WarnContext(ctx, "aquecimento falhou, seguindo para a busca", "err", err)
	}

	query := url.Values{}
	query.Set("payWithMiles", boolString(p.PayWithMiles))
	query.Set("starAlliance", boolString(p.StarAlliance))

	raw, err := s.doAuthed(ctx, http.MethodPost, pathAvailability, query, body)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to search %s->%s on %s: %w",
			p.Origin, p.Destination, p.DepartDate, err)
	}

	var out models.SearchResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, raw, fmt.Errorf("failed to decode search response: %w", err)
	}
	if !out.OK() {
		return &out, raw, fmt.Errorf("%w: status=%q errors=%s", ErrAPIStatus, out.Status, out.Errors)
	}
	return &out, raw, nil
}

// warmup reproduz as duas chamadas que o frontend faz imediatamente antes da
// busca: regras de passageiros e validação da rota.
//
// Falhas aqui não abortam a busca — o aquecimento é uma tentativa de fidelidade
// comportamental, não um pré-requisito conhecido.
func (s *Scraper) warmup(ctx context.Context, p models.SearchParams) error {
	tripType := p.EffectiveTripType()

	if _, err := s.PaxTypes(ctx, p.Origin, p.Destination, tripType); err != nil {
		return fmt.Errorf("failed to fetch pax types: %w", err)
	}

	body, err := json.Marshal(models.StopoverRequest{
		Destination:  p.Destination,
		Language:     s.cfg.Language,
		Origin:       p.Origin,
		TripType:     tripType,
		PayWithMiles: p.PayWithMiles,
	})
	if err != nil {
		return fmt.Errorf("failed to marshal stopover request: %w", err)
	}

	if _, err := s.doAuthed(ctx, http.MethodPost, pathStopover, nil, body); err != nil {
		return fmt.Errorf("failed to validate route: %w", err)
	}
	return nil
}

// PaxTypes consulta as regras de passageiros da rota. É uma requisição leve,
// útil para validar sessão e cookies antes de uma busca completa.
func (s *Scraper) PaxTypes(ctx context.Context, origin, destination, tripType string) ([]byte, error) {
	query := url.Values{}
	query.Set("market", s.cfg.Market)
	query.Add("journeyList", origin)
	query.Add("journeyList", destination)
	query.Set("tripType", tripType)

	raw, err := s.doAuthed(ctx, http.MethodGet, pathPaxTypes, query, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch pax types: %w", err)
	}
	return raw, nil
}
