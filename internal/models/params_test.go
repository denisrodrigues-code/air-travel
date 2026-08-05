package models

import (
	"strings"
	"testing"
	"time"
)

// validParams é uma coleta que passa em Validate — os testes partem dela e
// estragam um campo por vez.
func validParams() SearchParams {
	return SearchParams{
		Origin: "LIS", Destination: "RIO", DepartDate: "01092026",
		Adults: 1, CabinClass: "E",
	}
}

func TestValidateAcceptsMinimalParams(t *testing.T) {
	if err := validParams().Validate(); err != nil {
		t.Errorf("Validate() = %v, esperado nil", err)
	}
}

// TestValidateRejectsBadParams cobre cada regra. A validação é a última barreira
// antes de gastar uma requisição contra o GDS da TAP, que leva de 3 a 9 s.
func TestValidateRejectsBadParams(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*SearchParams)
	}{
		{"sem origem", func(p *SearchParams) { p.Origin = "" }},
		{"sem destino", func(p *SearchParams) { p.Destination = "" }},
		{"origem igual ao destino", func(p *SearchParams) { p.Destination = p.Origin }},
		{"data em ISO", func(p *SearchParams) { p.DepartDate = "2026-09-01" }},
		{"data curta", func(p *SearchParams) { p.DepartDate = "010926" }},
		{"data vazia", func(p *SearchParams) { p.DepartDate = "" }},
		{"retorno malformado", func(p *SearchParams) { p.ReturnDate = "19-09-2026" }},
		{"sem adultos", func(p *SearchParams) { p.Adults = 0 }},
		{"adultos negativo", func(p *SearchParams) { p.Adults = -1 }},
		{"sem cabine", func(p *SearchParams) { p.CabinClass = "" }},
		{"tipo de viagem inválido", func(p *SearchParams) { p.TripType = "X" }},
	}

	for _, tc := range tests {
		p := validParams()
		tc.mutate(&p)

		if err := p.Validate(); err == nil {
			t.Errorf("%s: aceito indevidamente (%+v)", tc.name, p)
		}
	}
}

// TestValidateEnforcesCabinEnum é a lacuna que os testes do adapter expuseram: a
// mensagem de erro sempre prometeu "E, W ou C" e o OpenAPI declara o enum, mas
// só o campo vazio era rejeitado. Uma cabine inexistente chegava à TAP, que
// responde 200 com corpo vazio — uma requisição gasta para não dizer nada.
func TestValidateEnforcesCabinEnum(t *testing.T) {
	for _, cabin := range []string{"E", "W", "C"} {
		p := validParams()
		p.CabinClass = cabin
		if err := p.Validate(); err != nil {
			t.Errorf("cabine %q rejeitada: %v", cabin, err)
		}
	}

	// Minúscula inclusive: a API distingue, e quem normaliza é o chamador.
	for _, cabin := range []string{"Z", "Y", "e", "economy", "EE"} {
		p := validParams()
		p.CabinClass = cabin

		err := p.Validate()
		if err == nil {
			t.Errorf("cabine %q aceita indevidamente", cabin)
			continue
		}
		if !strings.Contains(err.Error(), cabin) {
			t.Errorf("erro para %q não cita o valor recusado: %v", cabin, err)
		}
	}
}

// TestValidateAcceptsRoundTrip: com data de volta, o tipo derivado é R e passa.
func TestValidateAcceptsRoundTrip(t *testing.T) {
	p := validParams()
	p.ReturnDate = "19092026"

	if err := p.Validate(); err != nil {
		t.Errorf("Validate() = %v, esperado nil", err)
	}
	if got := p.EffectiveTripType(); got != "R" {
		t.Errorf("EffectiveTripType() = %q, esperado R", got)
	}
}

// TestEffectiveTripType fixa a derivação. Ela importa porque no calendário o
// tipo muda o preço: com R a TAP devolve a tarifa de ida e volta, mais barata que
// a soma de duas de só ida.
func TestEffectiveTripType(t *testing.T) {
	tests := []struct {
		explicit, returnDate, want string
	}{
		{"", "", "O"},          // só ida
		{"", "19092026", "R"},  // volta implica ida e volta
		{"R", "", "R"},         // explícito vence a ausência de data
		{"O", "19092026", "O"}, // explícito vence a presença de data
	}

	for _, tc := range tests {
		p := SearchParams{TripType: tc.explicit, ReturnDate: tc.returnDate}
		if got := p.EffectiveTripType(); got != tc.want {
			t.Errorf("TripType=%q ReturnDate=%q: obtido %q, esperado %q",
				tc.explicit, tc.returnDate, got, tc.want)
		}
	}
}

// TestTotalSeatsExcludesInfants: bebê de colo não ocupa assento, e contá-lo
// mudaria a disponibilidade pedida à TAP.
func TestTotalSeatsExcludesInfants(t *testing.T) {
	p := SearchParams{Adults: 2, Youths: 1, Children: 3, Infants: 4}

	if got := p.TotalSeats(); got != 6 {
		t.Errorf("TotalSeats() = %d, esperado 6 (2+1+3, sem os bebês)", got)
	}
	if got := p.Pax(); got.ADT != 2 || got.YTH != 1 || got.CHD != 3 || got.INF != 4 {
		t.Errorf("Pax() = %+v", got)
	}
}

// TestDateLayoutIsNotISO documenta o formato exigido pela API BFM nos corpos de
// busca e calendário — a fonte de um erro fácil, já que calendarReturns usa ISO.
func TestDateLayoutIsNotISO(t *testing.T) {
	day := time.Date(2026, time.September, 1, 0, 0, 0, 0, time.UTC)

	if got := day.Format(DateLayout); got != "01092026" {
		t.Errorf("DateLayout produz %q, esperado 01092026", got)
	}
	if got := day.Format(ISODateLayout); got != "2026-09-01" {
		t.Errorf("ISODateLayout produz %q, esperado 2026-09-01", got)
	}
}

// ---------------------------------------------------------------------------
// Chaves canônicas
// ---------------------------------------------------------------------------

// TestCalendarKeyIgnoresDates é a razão de existirem três tipos de chave: uma
// consulta ao calendário cobre ~365 datas, então incluir a data na chave faria o
// -resume repetir a mesma requisição 365 vezes.
//
// O tipo de viagem é fixado nos dois lados de propósito: ele compõe a chave (ver
// abaixo), e deixá-lo derivar da data de volta faria este teste medir a coisa
// errada.
func TestCalendarKeyIgnoresDates(t *testing.T) {
	a, b := validParams(), validParams()
	a.TripType, b.TripType = "R", "R"
	b.DepartDate = "15122026"
	b.ReturnDate = "20122026"

	if ka, kb := a.CalendarKeyFor("PT"), b.CalendarKeyFor("PT"); ka.String() != kb.String() {
		t.Errorf("chaves de calendário divergem por data:\n%s\n%s", ka, kb)
	}
}

// TestCalendarKeyDistinguishesTripType: com R a TAP devolve a tarifa de ida e
// volta, mais barata que a de só ida. São capturas diferentes da mesma rota e não
// podem colidir na mesma chave.
func TestCalendarKeyDistinguishesTripType(t *testing.T) {
	oneWay, roundTrip := validParams(), validParams()
	oneWay.TripType, roundTrip.TripType = "O", "R"

	if oneWay.CalendarKeyFor("PT").String() == roundTrip.CalendarKeyFor("PT").String() {
		t.Error("chaves de calendário iguais para O e R")
	}
}

// TestSearchKeyDistinguishesWhatChangesThePrice: cada dimensão que altera o
// preço tem de entrar na chave, senão o -resume devolveria a captura errada.
func TestSearchKeyDistinguishesWhatChangesThePrice(t *testing.T) {
	base := validParams().Key("PT").String()

	tests := map[string]func(*SearchParams){
		"origem":  func(p *SearchParams) { p.Origin = "OPO" },
		"destino": func(p *SearchParams) { p.Destination = "GRU" },
		"ida":     func(p *SearchParams) { p.DepartDate = "02092026" },
		"volta":   func(p *SearchParams) { p.ReturnDate = "19092026" },
		"cabine":  func(p *SearchParams) { p.CabinClass = "C" },
		"adultos": func(p *SearchParams) { p.Adults = 2 },
	}

	for name, mutate := range tests {
		p := validParams()
		mutate(&p)

		if got := p.Key("PT").String(); got == base {
			t.Errorf("mudar %s não mudou a chave: %s", name, got)
		}
	}

	// O mercado determina moeda e tarifa: PT dá EUR, BR dá BRL.
	if validParams().Key("BR").String() == base {
		t.Error("mudar o mercado não mudou a chave")
	}
}

// TestReturnsKeyIsPerOutboundDate: a matriz é consultada por data de ida, e cada
// ida devolve sua própria série de voltas.
func TestReturnsKeyIsPerOutboundDate(t *testing.T) {
	a, b := validParams(), validParams()
	b.DepartDate = "02092026"

	if a.ReturnsKeyFor("PT").String() == b.ReturnsKeyFor("PT").String() {
		t.Error("chaves de retorno iguais para idas diferentes")
	}

	// A data de volta NÃO entra: a consulta devolve todas as voltas de uma vez.
	c := validParams()
	c.ReturnDate = "19092026"
	if a.ReturnsKeyFor("PT").String() != c.ReturnsKeyFor("PT").String() {
		t.Error("a data de volta não deveria compor a chave da matriz")
	}
}
