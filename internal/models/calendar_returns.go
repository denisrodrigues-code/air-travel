package models

import (
	"fmt"
	"time"
)

// ISODateLayout é o formato de data aceito por calendarReturns na requisição.
// Note que difere do DDMMYYYY usado em search e no calendário.
const ISODateLayout = "2006-01-02"

// ---------------------------------------------------------------------------
// POST /bfm/rest/booking/availability/calendarReturns/
// ---------------------------------------------------------------------------

// CalendarReturnsRequest pede os preços por data de retorno, fixada a data de
// ida.
//
// Duas particularidades verificadas no tráfego real (corpo de 127 bytes):
//
//   - Origin e Destination descrevem a perna de VOLTA, portanto vêm invertidos
//     em relação ao sentido da viagem: uma viagem LIS→RIO envia origin=RIO,
//     destination=LIS.
//   - DepartureDate é a data da IDA e usa ISO (2026-09-01), não DDMMYYYY.
//
// A ordem dos campos reproduz a do corpo capturado.
type CalendarReturnsRequest struct {
	CabinClass    string `json:"cabinClass"`
	Destination   string `json:"destination"` // = origem da viagem
	Market        string `json:"market"`
	Origin        string `json:"origin"`        // = destino da viagem
	TripType      string `json:"tripType"`      // sempre R
	DepartureDate string `json:"departureDate"` // ISODateLayout, data da ida
	PaxType       string `json:"paxType"`       // ADT
}

// CalendarReturnsResponse é a resposta. Como no calendário, não há envelope
// status/errors.
type CalendarReturnsResponse struct {
	Data      CalendarReturnsData `json:"data"`
	Translate Translate           `json:"translate"`
}

// CalendarReturnsData descreve a viagem e a série de datas de retorno.
//
// Origin e Destination aqui seguem o sentido da VIAGEM (não da volta) e o
// destino vem resolvido para o aeroporto concreto — uma busca por RIO devolve
// GIG, por exemplo.
type CalendarReturnsData struct {
	Market       string        `json:"market"`
	Entity       int           `json:"entity"`
	Origin       string        `json:"origin"`
	DirectFlight bool          `json:"directFlight"`
	TripType     string        `json:"tripType"`
	Cabin        string        `json:"cabin"`
	Destination  string        `json:"destination"`
	Currency     string        `json:"currency"`
	Returns      []ReturnPrice `json:"returns"`
}

// ReturnPrice é o preço total da viagem para uma data de retorno.
type ReturnPrice struct {
	ReturnDate     string  `json:"returnDate"` // CalendarDateLayout
	Price          float64 `json:"price"`
	Miles          int     `json:"miles"`
	MonthlyMinimum bool    `json:"monthlyMinimum"`
	MonthlyMaximum bool    `json:"monthlyMaximum"`
	SoldOut        bool    `json:"soldOut"`
	NoFlights      bool    `json:"noFlights"`
}

// Bookable informa se a data de retorno é comercializável.
func (r *ReturnPrice) Bookable() bool {
	return !r.NoFlights && !r.SoldOut && r.Price > 0
}

// Return interpreta ReturnDate.
func (r *ReturnPrice) Return() (time.Time, error) {
	t, err := time.Parse(CalendarDateLayout, r.ReturnDate)
	if err != nil {
		return time.Time{}, fmt.Errorf("failed to parse returnDate %q: %w", r.ReturnDate, err)
	}
	return t, nil
}

// Bookable devolve apenas as datas de retorno com voo.
func (d CalendarReturnsData) Bookable() []ReturnPrice {
	out := make([]ReturnPrice, 0, len(d.Returns))
	for i := range d.Returns {
		if d.Returns[i].Bookable() {
			out = append(out, d.Returns[i])
		}
	}
	return out
}

// Cheapest devolve a data de retorno mais barata, ou nil se não houver.
func (d CalendarReturnsData) Cheapest() *ReturnPrice {
	var best *ReturnPrice
	for i := range d.Returns {
		item := &d.Returns[i]
		if !item.Bookable() {
			continue
		}
		if best == nil || item.Price < best.Price {
			best = item
		}
	}
	return best
}

// ReturnsKey identifica uma consulta de datas de retorno. Origin/Destination
// seguem o sentido da viagem.
type ReturnsKey struct {
	Origin      string
	Destination string
	DepartDate  string // DDMMYYYY, a data da ida
	CabinClass  string
	Market      string
	Adults      int
}

// String devolve a chave canônica usada no armazenamento.
func (k ReturnsKey) String() string {
	return fmt.Sprintf("returns:%s:%s:%s:%s:%s:%d",
		k.Origin, k.Destination, k.DepartDate, k.CabinClass, k.Market, k.Adults)
}

// ReturnsInWindow recorta as datas de retorno ao intervalo [from, to],
// descartando as sem voo.
func ReturnsInWindow(prices []ReturnPrice, from, to time.Time) []ReturnPrice {
	out := make([]ReturnPrice, 0, len(prices))
	for i := range prices {
		item := &prices[i]
		if !item.Bookable() {
			continue
		}
		day, err := item.Return()
		if err != nil {
			continue
		}
		day = day.Truncate(24 * time.Hour)
		if day.Before(from) || day.After(to) {
			continue
		}
		out = append(out, *item)
	}
	return out
}
