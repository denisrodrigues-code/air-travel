package models

import (
	"errors"
	"fmt"
)

// DateLayout é o formato de data que a API BFM exige nos corpos de busca e
// calendário. Não é ISO: é dia, mês e ano concatenados.
const DateLayout = "02012006" // DDMMYYYY

// SearchParams descreve uma coleta em termos de domínio.
//
// Vive aqui, e não no adapter da TAP, porque é a entrada do caso de uso: tanto
// `collect` quanto o adapter quanto a camada HTTP a usam, e nenhum deles precisa
// depender do outro por causa dela.
type SearchParams struct {
	Origin       string // código IATA de aeroporto ou de cidade, ex.: LIS
	Destination  string // ex.: RIO
	DepartDate   string // DateLayout
	ReturnDate   string // DateLayout; vazio => somente ida
	Adults       int
	Youths       int
	Children     int
	Infants      int
	CabinClass   string // E economy, W premium, C executiva
	PayWithMiles bool
	StarAlliance bool
	// TripType força O (só ida) ou R (ida e volta). Vazio deriva de ReturnDate.
	//
	// No calendário a distinção muda o preço: com R a API devolve a tarifa de
	// ida e volta, tipicamente mais barata que a soma de duas de só ida. A data
	// de retorno em si não é usada pelo calendário — apenas o tipo de viagem.
	TripType string
}

// EffectiveTripType resolve o tipo de viagem a usar.
func (p SearchParams) EffectiveTripType() string {
	if p.TripType != "" {
		return p.TripType
	}
	if p.ReturnDate == "" {
		return "O"
	}
	return "R"
}

// TotalSeats é a contagem de assentos vendáveis (bebês vão no colo).
func (p SearchParams) TotalSeats() int {
	return p.Adults + p.Youths + p.Children
}

// Pax devolve a contagem por tipo de passageiro.
func (p SearchParams) Pax() PaxCount {
	return PaxCount{ADT: p.Adults, YTH: p.Youths, CHD: p.Children, INF: p.Infants}
}

// Key devolve a chave canônica da busca completa.
func (p SearchParams) Key(market string) SearchKey {
	return SearchKey{
		Origin:      p.Origin,
		Destination: p.Destination,
		DepartDate:  p.DepartDate,
		ReturnDate:  p.ReturnDate,
		CabinClass:  p.CabinClass,
		Market:      market,
		Adults:      p.Adults,
	}
}

// CalendarKeyFor devolve a chave canônica da consulta ao calendário. O
// calendário cobre um ano inteiro, portanto a chave não inclui datas.
func (p SearchParams) CalendarKeyFor(market string) CalendarKey {
	return CalendarKey{
		Origin:      p.Origin,
		Destination: p.Destination,
		CabinClass:  p.CabinClass,
		TripType:    p.EffectiveTripType(),
		Market:      market,
		Adults:      p.Adults,
	}
}

// ReturnsKeyFor devolve a chave canônica da matriz ida × volta, que é por data
// de ida.
func (p SearchParams) ReturnsKeyFor(market string) ReturnsKey {
	return ReturnsKey{
		Origin:      p.Origin,
		Destination: p.Destination,
		DepartDate:  p.DepartDate,
		CabinClass:  p.CabinClass,
		Market:      market,
		Adults:      p.Adults,
	}
}

// Validate confere os parâmetros mínimos de uma coleta.
func (p SearchParams) Validate() error {
	if p.Origin == "" || p.Destination == "" {
		return errors.New("Origin e Destination são obrigatórios")
	}
	if p.Origin == p.Destination {
		return fmt.Errorf("Origin e Destination não podem ser iguais (%q)", p.Origin)
	}
	if len(p.DepartDate) != 8 {
		return fmt.Errorf("DepartDate deve estar em DDMMYYYY, obtido %q", p.DepartDate)
	}
	if p.ReturnDate != "" && len(p.ReturnDate) != 8 {
		return fmt.Errorf("ReturnDate deve estar em DDMMYYYY, obtido %q", p.ReturnDate)
	}
	if p.Adults < 1 {
		return fmt.Errorf("Adults deve ser >= 1, obtido %d", p.Adults)
	}
	// O enum é o mesmo declarado no OpenAPI. Validar aqui — e não na borda HTTP —
	// cobre o CLI pelo mesmo caminho: uma cabine inexistente faz a TAP responder
	// 200 com corpo vazio, gastando uma requisição para não dizer nada.
	switch p.CabinClass {
	case "E", "W", "C":
	case "":
		return errors.New("CabinClass é obrigatória (E, W ou C)")
	default:
		return fmt.Errorf("CabinClass %q inválida (use E, W ou C)", p.CabinClass)
	}
	if t := p.EffectiveTripType(); t != "O" && t != "R" {
		return fmt.Errorf("TripType %q inválido (use O ou R)", t)
	}
	return nil
}
