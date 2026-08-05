package models

import (
	"fmt"
	"time"
)

// CalendarDateLayout é o formato das datas do calendário: um timestamp sem
// fuso, portanto NÃO é RFC3339 (ex.: "2026-08-03T00:00:00").
const CalendarDateLayout = "2006-01-02T15:04:05"

// ---------------------------------------------------------------------------
// POST /bfm/rest/booking/availability/calendar/
// ---------------------------------------------------------------------------

// CalendarRequest é o corpo da consulta ao calendário de preços.
//
// Ao contrário de SearchRequest, aqui origin/destination/departureDate são
// escalares, não arrays — enviar arrays devolve 200 com corpo vazio. A ordem
// dos campos reproduz o corpo de 120 bytes observado no navegador.
type CalendarRequest struct {
	Origin        string `json:"origin"`
	Destination   string `json:"destination"`
	DepartureDate string `json:"departureDate"` // DDMMYYYY
	TripType      string `json:"tripType"`      // O só ida, R ida e volta
	Market        string `json:"market"`
	Language      string `json:"language"`
	Adt           int    `json:"adt"`
	CabinClass    string `json:"cabinClass"`
}

// CalendarResponse é a resposta do calendário.
//
// Note a ausência do envelope status/errors usado nos demais endpoints: aqui
// vêm apenas data e translate.
type CalendarResponse struct {
	Data      CalendarData `json:"data"`
	Translate Translate    `json:"translate"`
}

// CalendarData contém a série de melhores preços por data.
type CalendarData struct {
	BestPriceForDates []BestPriceForDate `json:"bestPriceForDates"`
}

// BestPriceForDate é o menor preço encontrado para uma data de partida.
//
// Uma consulta devolve um ano inteiro (365 itens), o que torna este endpoint
// muito mais econômico que a busca completa: uma requisição por rota, em vez de
// uma por data.
type BestPriceForDate struct {
	ArrivalAirport   string  `json:"arrivalAirport"`
	BestTotalPrice   float64 `json:"bestTotalPrice"`
	BestTotalMiles   int     `json:"bestTotalMiles"`
	CabinClass       string  `json:"cabinClass"`
	Currency         string  `json:"currency"`
	DepartureAirport string  `json:"departureAirport"`
	DepartureDate    string  `json:"departureDate"` // CalendarDateLayout
	InsertionDate    string  `json:"insertionDate"` // CalendarDateLayout
	Market           string  `json:"market"`
	TripType         string  `json:"tripType"`
	MonthlyMinimum   bool    `json:"monthlyMinimum"`
	MonthlyMaximum   bool    `json:"monthlyMaximum"`
	StarAlliance     bool    `json:"starAlliance"`
	SoldOut          bool    `json:"soldOut"`
	NoFlights        bool    `json:"noFlights"`
}

// Bookable informa se a data tem voo comercializável.
func (b *BestPriceForDate) Bookable() bool {
	return !b.NoFlights && !b.SoldOut && b.BestTotalPrice > 0
}

// Departure interpreta DepartureDate.
func (b *BestPriceForDate) Departure() (time.Time, error) {
	t, err := time.Parse(CalendarDateLayout, b.DepartureDate)
	if err != nil {
		return time.Time{}, fmt.Errorf("failed to parse departureDate %q: %w", b.DepartureDate, err)
	}
	return t, nil
}

// Inserted interpreta InsertionDate, o momento em que a TAP calculou o preço.
func (b *BestPriceForDate) Inserted() (time.Time, error) {
	t, err := time.Parse(CalendarDateLayout, b.InsertionDate)
	if err != nil {
		return time.Time{}, fmt.Errorf("failed to parse insertionDate %q: %w", b.InsertionDate, err)
	}
	return t, nil
}

// Bookable devolve apenas as datas com voo disponível.
func (d CalendarData) Bookable() []BestPriceForDate {
	out := make([]BestPriceForDate, 0, len(d.BestPriceForDates))
	for i := range d.BestPriceForDates {
		if d.BestPriceForDates[i].Bookable() {
			out = append(out, d.BestPriceForDates[i])
		}
	}
	return out
}

// Cheapest devolve a data mais barata com voo, ou nil se não houver nenhuma.
func (d CalendarData) Cheapest() *BestPriceForDate {
	var best *BestPriceForDate
	for i := range d.BestPriceForDates {
		item := &d.BestPriceForDates[i]
		if !item.Bookable() {
			continue
		}
		if best == nil || item.BestTotalPrice < best.BestTotalPrice {
			best = item
		}
	}
	return best
}

// InWindow devolve as datas de partida dentro do intervalo [from, to],
// inclusive nos extremos. Datas sem voo são descartadas.
//
// A API sempre devolve um ano inteiro; o recorte é feito aqui para que a
// requisição continue sendo uma só.
func InWindow(prices []BestPriceForDate, from, to time.Time) []BestPriceForDate {
	out := make([]BestPriceForDate, 0, len(prices))
	for i := range prices {
		item := &prices[i]
		if !item.Bookable() {
			continue
		}
		departure, err := item.Departure()
		if err != nil {
			continue
		}
		day := departure.Truncate(24 * time.Hour)
		if day.Before(from) || day.After(to) {
			continue
		}
		out = append(out, *item)
	}
	return out
}

// CalendarKey identifica uma consulta de calendário, servindo de chave de
// deduplicação e retomada.
type CalendarKey struct {
	Origin      string
	Destination string
	CabinClass  string
	TripType    string
	Market      string
	Adults      int
}

// String devolve a chave canônica usada no armazenamento.
func (k CalendarKey) String() string {
	return fmt.Sprintf("calendar:%s:%s:%s:%s:%s:%d",
		k.Origin, k.Destination, k.CabinClass, k.TripType, k.Market, k.Adults)
}
