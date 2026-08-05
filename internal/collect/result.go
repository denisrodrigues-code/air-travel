package collect

import (
	"time"

	"airtravel/internal/models"
)

// SearchResult é o desfecho de uma coleta de voos e tarifas.
type SearchResult struct {
	Key       models.SearchKey
	Response  *models.SearchResponse
	RawKey    string
	ScrapedAt time.Time
	// Skipped indica que a coleta foi dispensada por já estar persistida.
	Skipped bool
	// Warnings descreve falhas de persistência que NÃO invalidaram a captura.
	Warnings []string
}

// Offers devolve as ofertas achatadas, com o itinerário resolvido.
func (r SearchResult) Offers() []models.OfferRecord {
	if r.Response == nil {
		return nil
	}
	return models.FlattenOffers(r.Key, r.ScrapedAt.Format(time.RFC3339), r.Response)
}

// Flights conta os itinerários devolvidos.
func (r SearchResult) Flights() int {
	if r.Response == nil {
		return 0
	}
	return len(r.Response.Data.ListOutbound)
}

// OfferCount conta as ofertas devolvidas.
func (r SearchResult) OfferCount() int {
	if r.Response == nil {
		return 0
	}
	return len(r.Response.Data.Offers.ListOffers)
}

// CalendarResult é o desfecho de uma consulta ao calendário de preços.
type CalendarResult struct {
	Key       models.CalendarKey
	Response  *models.CalendarResponse
	RawKey    string
	ScrapedAt time.Time
	Skipped   bool
	Warnings  []string
}

// Dates conta as datas devolvidas, incluindo as sem voo.
func (r CalendarResult) Dates() int {
	if r.Response == nil {
		return 0
	}
	return len(r.Response.Data.BestPriceForDates)
}

// Bookable devolve apenas as datas com voo comercializável.
func (r CalendarResult) Bookable() []models.BestPriceForDate {
	if r.Response == nil {
		return nil
	}
	return r.Response.Data.Bookable()
}

// Cheapest devolve a data mais barata, ou nil.
func (r CalendarResult) Cheapest() *models.BestPriceForDate {
	if r.Response == nil {
		return nil
	}
	return r.Response.Data.Cheapest()
}

// ReturnsResult é o desfecho de uma consulta à matriz ida × volta.
type ReturnsResult struct {
	Key       models.ReturnsKey
	Response  *models.CalendarReturnsResponse
	RawKey    string
	ScrapedAt time.Time
	Skipped   bool
	Warnings  []string
}

// Dates conta as datas de retorno devolvidas.
func (r ReturnsResult) Dates() int {
	if r.Response == nil {
		return 0
	}
	return len(r.Response.Data.Returns)
}

// Bookable devolve apenas as datas de retorno com voo.
func (r ReturnsResult) Bookable() []models.ReturnPrice {
	if r.Response == nil {
		return nil
	}
	return r.Response.Data.Bookable()
}

// Cheapest devolve a data de retorno mais barata, ou nil.
func (r ReturnsResult) Cheapest() *models.ReturnPrice {
	if r.Response == nil {
		return nil
	}
	return r.Response.Data.Cheapest()
}

// ResolvedDestination devolve o aeroporto concreto que a TAP resolveu.
func (r ReturnsResult) ResolvedDestination() string {
	if r.Response == nil {
		return ""
	}
	return r.Response.Data.Destination
}
