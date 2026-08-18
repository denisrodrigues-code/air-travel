package models

import (
	"fmt"
	"strings"
)

// SearchKey identifica univocamente uma busca. Serve de chave de deduplicação
// e de retomada no BadgerDB.
type SearchKey struct {
	Origin      string
	Destination string
	DepartDate  string // DDMMYYYY
	ReturnDate  string // DDMMYYYY, vazio para one-way
	CabinClass  string
	Market      string
	Adults      int
}

// String devolve a chave canônica usada como chave primária no armazenamento.
func (k SearchKey) String() string {
	ret := k.ReturnDate
	if ret == "" {
		ret = "OW"
	}
	return fmt.Sprintf("search:%s:%s:%s:%s:%s:%s:%d",
		k.Origin, k.Destination, k.DepartDate, ret, k.CabinClass, k.Market, k.Adults)
}

// OfferRecord é a linha achatada e pronta para análise: uma oferta com o
// itinerário já resolvido. É o que vai para o JSONL.
type OfferRecord struct {
	// Identificação da busca que produziu o registro.
	SearchKey   string `json:"searchKey"`
	ScrapedAt   string `json:"scrapedAt"`
	Origin      string `json:"origin"`
	Destination string `json:"destination"`
	DepartDate  string `json:"departDate"`
	Market      string `json:"market"`

	// Oferta.
	OfferID           int     `json:"offerId"`
	Currency          string  `json:"currency"`
	Cabin             string  `json:"cabin"`
	FareFamily        string  `json:"fareFamily"`
	CommercialFareFam string  `json:"commercialFareFamily"`
	FareFamilyRank    int     `json:"fareFamilyHierarchy"`
	TotalPrice        float64 `json:"totalPrice"`
	BasePrice         float64 `json:"basePrice"`
	Tax               float64 `json:"tax"`
	SuperSaver        bool    `json:"superSaver"`
	DiscountedPromo   bool    `json:"discountedWithPromocode"`

	// OutboundPrice é o preço da PERNA DE IDA, não da viagem.
	//
	// É o número que a TAP exibe nos cartões de voo ("Economy 460,21 EUR"),
	// enquanto TotalPrice é o total de ida e volta (1.305,10 na mesma oferta,
	// medido em 05/08/2026). Os dois são corretos e respondem a perguntas
	// diferentes; guardar só o total tornava impossível reproduzir o que o
	// usuário vê na tela.
	//
	// Ponteiro porque Offer.Outbound é opcional: nil distingue "a API não trouxe
	// a perna" de "a perna custa zero".
	OutboundPrice *float64 `json:"outboundPrice,omitempty"`

	// Itinerário associado.
	FlightID      int `json:"flightId"`
	DurationMin   int `json:"durationMinutes"`
	NumberOfStops int `json:"numberOfStops"`
	// TechnicalStops conta as paradas técnicas de todos os segmentos. Somadas a
	// NumberOfStops dão o número de escalas que a TAP mostra ao usuário — ver
	// TechnicalStop.
	TechnicalStops int      `json:"technicalStops"`
	Carriers       []string `json:"carriers"`
	FlightNumbers  []string `json:"flightNumbers"`
	FareBasis      []string `json:"fareBasis"`
	RBD            []string `json:"rbd"`
	DepartureTime  string   `json:"departureTime"`
	ArrivalTime    string   `json:"arrivalTime"`
	Route          []string `json:"route"`
}

// ID é a chave única da oferta dentro do armazenamento.
func (r OfferRecord) ID() string {
	return fmt.Sprintf("offer:%s:%d", r.SearchKey, r.OfferID)
}

// FlattenOffers cruza data.offers.listOffers com data.listOutbound usando
// GroupFlights[].IDOutBound e devolve um registro por oferta.
//
// Ofertas cujo voo referenciado não exista na resposta são ignoradas: sem o
// itinerário o registro não tem valor analítico.
func FlattenOffers(key SearchKey, scrapedAt string, resp *SearchResponse) []OfferRecord {
	flightsByID := make(map[int]*Flight, len(resp.Data.ListOutbound))
	for i := range resp.Data.ListOutbound {
		f := &resp.Data.ListOutbound[i]
		flightsByID[f.IDFlight] = f
	}

	records := make([]OfferRecord, 0, len(resp.Data.Offers.ListOffers))
	for i := range resp.Data.Offers.ListOffers {
		offer := &resp.Data.Offers.ListOffers[i]

		for _, group := range offer.GroupFlights {
			flight, ok := flightsByID[group.IDOutBound]
			if !ok {
				continue
			}

			rec := OfferRecord{
				SearchKey:         key.String(),
				ScrapedAt:         scrapedAt,
				Origin:            key.Origin,
				Destination:       key.Destination,
				DepartDate:        key.DepartDate,
				Market:            key.Market,
				OfferID:           offer.IDOffer,
				Currency:          resp.Data.Offers.Currency,
				Cabin:             offer.OutCabin,
				FareFamily:        offer.OutFareFamily,
				CommercialFareFam: offer.OutCommercialFareFamily,
				FareFamilyRank:    offer.OutFareFamilyHierarchy,
				TotalPrice:        offer.TotalPrice.Price,
				BasePrice:         offer.TotalPrice.BasePrice,
				Tax:               offer.TotalPrice.Tax,
				SuperSaver:        offer.TotalPrice.SuperSaver,
				DiscountedPromo:   offer.DiscountedWithPromocode,
				FlightID:          flight.IDFlight,
				DurationMin:       flight.Duration,
				NumberOfStops:     flight.NumberOfStops,
			}

			if offer.Outbound != nil {
				rec.FareBasis = offer.Outbound.FareBasis
				rec.RBD = offer.Outbound.Rbd
				price := offer.Outbound.TotalPrice.Price
				rec.OutboundPrice = &price
			}

			for j := range flight.ListSegment {
				seg := &flight.ListSegment[j]
				rec.TechnicalStops += len(seg.TechnicalStops)
				rec.Carriers = append(rec.Carriers, seg.Carrier)
				rec.FlightNumbers = append(rec.FlightNumbers, seg.Carrier+seg.FlightNumber)
				if j == 0 {
					rec.DepartureTime = seg.DepartureDate
					rec.Route = append(rec.Route, seg.DepartureAirport)
				}
				rec.Route = append(rec.Route, seg.ArrivalAirport)
				rec.ArrivalTime = seg.ArrivalDate
			}

			records = append(records, rec)
		}
	}
	return records
}

// RouteString representa a rota de forma legível, ex.: "LIS-GRU-SDU".
func (r OfferRecord) RouteString() string { return strings.Join(r.Route, "-") }
