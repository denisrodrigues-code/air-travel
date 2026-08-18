// Package models descreve os payloads da API BFM do booking.flytap.com.
//
// A tipagem segue exatamente o que foi observado no tráfego real. Dois pontos
// merecem atenção porque quebram o palpite intuitivo:
//
//   - "status" é uma string ("200"), não um número.
//   - "basePrice" aparece tanto como inteiro quanto como decimal, portanto é
//     float64 obrigatoriamente.
//
// Campos que só foram observados como null usam json.RawMessage: preserva o
// valor bruto sem arriscar um erro de unmarshal quando a API o preencher.
package models

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// ---------------------------------------------------------------------------
// Autenticação: POST /bfm/rest/session/resetValues
// ---------------------------------------------------------------------------

// SessionRequest é o corpo enviado para obter um JWT anônimo.
type SessionRequest struct {
	ClientID     string          `json:"clientId"`
	ClientSecret string          `json:"clientSecret"`
	ReferralID   string          `json:"referralId"`
	Market       string          `json:"market"`
	Language     string          `json:"language"`
	UserProfile  json.RawMessage `json:"userProfile"`
	AppModule    string          `json:"appModule"`
	IDOperation  json.RawMessage `json:"idOperation"`
}

// SessionResponse traz o JWT no campo "id" e um token opaco de sessão.
type SessionResponse struct {
	Status      string          `json:"status"`
	Errors      json.RawMessage `json:"errors"`
	TimeService json.RawMessage `json:"timeService"`
	TimeBackend json.RawMessage `json:"timeBackend"`
	Token       string          `json:"token"`
	CookieExist bool            `json:"cookieExist"`
	GoogleSSO   bool            `json:"googleSSO"`
	ID          string          `json:"id"` // JWT usado em Authorization: Bearer
}

// ---------------------------------------------------------------------------
// Aquecimento: POST /bfm/rest/journey/stopover/search
// ---------------------------------------------------------------------------

// StopoverRequest valida a rota antes da busca. O frontend sempre a envia
// imediatamente antes de /booking/availability/search, na mesma conexão.
type StopoverRequest struct {
	Destination  string `json:"destination"`
	Language     string `json:"language"`
	Origin       string `json:"origin"`
	TripType     string `json:"tripType"`
	PayWithMiles bool   `json:"payWithMiles"`
}

// ---------------------------------------------------------------------------
// Busca de disponibilidade: POST /bfm/rest/booking/availability/search
// ---------------------------------------------------------------------------

// PaxCount é a contagem de passageiros por tipo. A ordem dos campos reproduz
// a do corpo capturado.
type PaxCount struct {
	ADT int `json:"ADT"`
	YTH int `json:"YTH"`
	CHD int `json:"CHD"`
	INF int `json:"INF"`
}

// SearchRequest é o corpo da busca. A ordem de declaração dos campos espelha
// a ordem das chaves no JSON capturado do navegador, já que encoding/json
// serializa na ordem de declaração.
type SearchRequest struct {
	Adt                  int      `json:"adt"`
	AirlineID            string   `json:"airlineId"`
	BfmModule            string   `json:"bfmModule"`
	C14                  int      `json:"c14"`
	CabinClass           string   `json:"cabinClass"`
	ChangeReturn         bool     `json:"changeReturn"`
	ChannelDetectionName string   `json:"channelDetectionName"`
	Chd                  int      `json:"chd"`
	CmsID                string   `json:"cmsId"`
	Communities          []string `json:"communities"`
	DepartureDate        []string `json:"departureDate"`
	Destination          []string `json:"destination"`
	Groups               []string `json:"groups"`
	Inf                  int      `json:"inf"`
	Language             string   `json:"language"`
	Market               string   `json:"market"`
	MultiCityTripType    bool     `json:"multiCityTripType"`
	NumSeat              int      `json:"numSeat"`
	NumSeats             int      `json:"numSeats"`
	OneWay               bool     `json:"oneWay"`
	Origin               []string `json:"origin"`
	Passengers           PaxCount `json:"passengers"`
	PaxSearch            PaxCount `json:"paxSearch"`
	PermittedCabins      []string `json:"permittedCabins"`
	PreferredCarrier     []string `json:"preferredCarrier"`
	Promocode            string   `json:"promocode"`
	PromotionID          string   `json:"promotionId"`
	ReturnDate           string   `json:"returnDate"`
	RoundTripType        bool     `json:"roundTripType"`
	SearchPoint          bool     `json:"searchPoint"`
	Session              string   `json:"session"`
	TripType             string   `json:"tripType"`
	ValidTripType        bool     `json:"validTripType"`
	Student              bool     `json:"student"`
	Resident             bool     `json:"resident"`
	Yth                  int      `json:"yth"`
}

// SearchResponse é o envelope padrão da API BFM.
type SearchResponse struct {
	Status      string          `json:"status"`
	Errors      json.RawMessage `json:"errors"`
	TimeService int             `json:"timeService"`
	TimeBackend json.RawMessage `json:"timeBackend"`
	Data        SearchData      `json:"data"`
	Translate   Translate       `json:"translate"`
}

// OK informa se a API respondeu com sucesso lógico (o status vem como string).
func (r *SearchResponse) OK() bool { return r.Status == "200" }

// HasErrors indica se o campo errors traz conteúdo além de null/[].
func (r *SearchResponse) HasErrors() bool {
	return hasContent(r.Errors)
}

// SearchData é o corpo útil da resposta.
type SearchData struct {
	AvailabilityID    json.RawMessage `json:"availabilityId"`
	Cff               json.RawMessage `json:"cff"`
	ContinuousPricing bool            `json:"continuousPricing"`
	DedicatedPricing  bool            `json:"dedicatedPricing"`
	GdsSession        json.RawMessage `json:"gdsSession"`
	HitAvail          bool            `json:"hitAvail"`
	HitCalendar       bool            `json:"hitCalendar"`
	InPanel           *DatePanel      `json:"inPanel"`
	ListInbound       []Flight        `json:"listInbound"`
	ListOutbound      []Flight        `json:"listOutbound"`
	ListPaxNum        []PaxNum        `json:"listPaxNum"`
	MatrixPanel       json.RawMessage `json:"matrixPanel"`
	NotAvailable      bool            `json:"notAvailable"`
	OfferMatrix       *OfferMatrix    `json:"offerMatrix"`
	Offers            Offers          `json:"offers"`
	OfficeID          string          `json:"officeId"`
	OutPanel          *DatePanel      `json:"outPanel"`
	Premium           bool            `json:"premium"`
	TotalAvailHits    int             `json:"totalAvailHits"`
	TotalCalendarHits int             `json:"totalCalendarHits"`
}

// PaxNum associa uma quantidade a um tipo de passageiro.
type PaxNum struct {
	PaxNum  int    `json:"paxNum"`
	PaxType string `json:"paxType"`
}

// ---------------------------------------------------------------------------
// Voos
// ---------------------------------------------------------------------------

// Flight é um itinerário completo (uma perna, com um ou mais segmentos).
// IDFlight é a chave referenciada por Offer.GroupFlights[].IDOutBound.
type Flight struct {
	AirportChange         bool            `json:"airportChange"`
	CodeshareWarning      bool            `json:"codeshareWarning"`
	Duration              int             `json:"duration"` // minutos
	EconomyWarning        bool            `json:"economyWarning"`
	EconomyWarningFlights json.RawMessage `json:"economyWarningFlights"`
	EmbargoEndDate        json.RawMessage `json:"embargoEndDate"`
	EmbargoStartDate      json.RawMessage `json:"embargoStartDate"`
	EmbargoWarning        bool            `json:"embargoWarning"`
	IDFlight              int             `json:"idFlight"`
	ListSegment           []Segment       `json:"listSegment"`
	MilesGoDiscount       bool            `json:"milesGoDiscount"`
	NumberOfStops         int             `json:"numberOfStops"`
	OalWarning            bool            `json:"oalWarning"`
	OalWarningFlights     json.RawMessage `json:"oalWarningFlights"`
	RelateOffer           json.RawMessage `json:"relateOffer"`
}

// Segment é um trecho operado por um único número de voo.
type Segment struct {
	AirportChange         bool                       `json:"airportChange"`
	ArrivalAirport        string                     `json:"arrivalAirport"`
	ArrivalDate           string                     `json:"arrivalDate"` // RFC3339
	ArrivalTerminal       *string                    `json:"arrivalTerminal"`
	BaggageForPax         map[string]json.RawMessage `json:"baggageForPax"`
	CabinMeal             map[string]json.RawMessage `json:"cabinMeal"`
	Carrier               string                     `json:"carrier"`
	CodeshareFlight       bool                       `json:"codeshareFlight"`
	DepartureAirport      string                     `json:"departureAirport"`
	DepartureDate         string                     `json:"departureDate"` // RFC3339
	DepartureHours        json.RawMessage            `json:"departureHours"`
	DepartureTerminal     *string                    `json:"departureTerminal"`
	DepartureTimezone     json.RawMessage            `json:"departureTimezone"`
	DirectFlightGovAlert  bool                       `json:"directFlightGovAlert"`
	Duration              int                        `json:"duration"` // minutos
	Equipment             string                     `json:"equipment"`
	FareFamily            json.RawMessage            `json:"fareFamily"`
	FlightFlown           bool                       `json:"flightFlown"`
	FlightNumber          string                     `json:"flightNumber"`
	Haul                  string                     `json:"haul"`
	IDInfoSegment         int                        `json:"idInfoSegment"`
	LongLayover           bool                       `json:"longLayover"`
	NotAvailable          bool                       `json:"notAvailable"`
	OalSegment            bool                       `json:"oalSegment"`
	OperationCarrier      string                     `json:"operationCarrier"`
	OperationalDisclosure json.RawMessage            `json:"operationalDisclosure"`
	Status                json.RawMessage            `json:"status"`
	StopTime              int                        `json:"stopTime"` // minutos de conexão
	TechnicalStops        []TechnicalStop            `json:"technicalStops"`
}

// TechnicalStop é uma parada técnica: o avião pousa e volta a decolar com o
// MESMO número de voo, então não há conexão nem troca de aeronave.
//
// Está aqui, e não em Flight.NumberOfStops, que conta apenas conexões. É por
// isso que um voo com numberOfStops == 0 aparece no site como "1 escala":
// verificado no TP67 LIS→GIG de 01/09/2026, que para 105 min em Curitiba e leva
// 14h15 contra as 9h55 dos diretos da mesma rota. Ignorar este campo faz a
// coleta contradizer a interface da TAP sem estar errada — que é o pior tipo de
// divergência, porque parece bug de parsing.
//
// Vem null na maioria dos segmentos (64 dos 65 da fixture), daí o slice nil.
type TechnicalStop struct {
	ArrivalDate   string `json:"arrivalDate"`   // DD/MM/AAAA
	ArrivalTime   string `json:"arrivalTime"`   // HH:MM
	DepartureDate string `json:"departureDate"` // DD/MM/AAAA
	DepartureTime string `json:"departureTime"` // HH:MM
	Duration      int    `json:"duration"`      // minutos em solo
	Location      string `json:"location"`      // código IATA da escala
}

// StopWallClockLayout combina data e hora de uma parada técnica.
//
// Formato próprio: DD/MM/AAAA e HH:MM em campos separados, diferente do
// RFC3339-com-Z-falso dos segmentos e do CalendarDateLayout do calendário. São
// três formatos de data na mesma API.
const StopWallClockLayout = "02/01/2006 15:04"

// ArrivalAt devolve a chegada à escala como hora de parede do aeroporto dela.
//
// Vale a mesma regra dos segmentos: não há fuso no valor e não se deve subtrair
// dois instantes para obter duração — o campo Duration já a traz.
func (s TechnicalStop) ArrivalAt() (time.Time, error) {
	return parseStopWallClock(s.ArrivalDate, s.ArrivalTime)
}

// DepartureAt devolve a saída da escala como hora de parede do aeroporto dela.
func (s TechnicalStop) DepartureAt() (time.Time, error) {
	return parseStopWallClock(s.DepartureDate, s.DepartureTime)
}

// parseStopWallClock junta data e hora. Devolve o zero sem erro quando qualquer
// das duas falta: uma escala sem horário anunciado é ausência de informação, não
// resposta malformada, e derrubar a gravação de uma busca inteira por causa
// disso seria desproporcional. Já um valor presente e ilegível é erro.
func parseStopWallClock(date, clock string) (time.Time, error) {
	if date == "" || clock == "" {
		return time.Time{}, nil
	}
	t, err := time.Parse(StopWallClockLayout, date+" "+clock)
	if err != nil {
		return time.Time{}, fmt.Errorf("failed to parse technical stop %q %q: %w", date, clock, err)
	}
	return t, nil
}

// DepartureTime devolve a hora de parede da partida, no fuso do aeroporto de
// origem. Ver ParseWallClock: o sufixo Z da API é falso.
func (s *Segment) DepartureTime() (time.Time, error) {
	t, err := ParseWallClock(s.DepartureDate)
	if err != nil {
		return time.Time{}, fmt.Errorf("failed to parse departureDate %q: %w", s.DepartureDate, err)
	}
	return t, nil
}

// ArrivalTime devolve a hora de parede da chegada, no fuso do aeroporto de
// destino.
func (s *Segment) ArrivalTime() (time.Time, error) {
	t, err := ParseWallClock(s.ArrivalDate)
	if err != nil {
		return time.Time{}, fmt.Errorf("failed to parse arrivalDate %q: %w", s.ArrivalDate, err)
	}
	return t, nil
}

// ---------------------------------------------------------------------------
// Ofertas e preços
// ---------------------------------------------------------------------------

// Price é o detalhamento monetário de uma oferta.
//
// A divisão entre float64 e int não é estética: foi medida na sintaxe JSON de
// uma resposta real (462 ocorrências de cada campo). Os valores monetários são
// sempre serializados como decimais — inclusive quando zerados, em que chegam
// como "0.0" e não "0". Declarar ccFee, obFee ou sliderDiscount como int
// produz "json: cannot unmarshal number 0.0 into ... of type int".
//
// Miles e MinFareInPoints são contagens e chegam como inteiros puros.
type Price struct {
	BasePrice       float64 `json:"basePrice"`
	CcFee           float64 `json:"ccFee"`
	Miles           int     `json:"miles"`
	MinFareInPoints int     `json:"minFareInPoints"`
	ObFee           float64 `json:"obFee"`
	Price           float64 `json:"price"`
	SliderDiscount  float64 `json:"sliderDiscount"`
	SuperSaver      bool    `json:"superSaver"`
	Tax             float64 `json:"tax"`
	TypePax         string  `json:"typePax,omitempty"`
}

// Offers agrupa as ofertas de uma busca com a respectiva moeda.
type Offers struct {
	Currency   string          `json:"currency"`
	ListOffers []Offer         `json:"listOffers"`
	Points     json.RawMessage `json:"points"`
}

// Offer é uma combinação vendável de voo + família tarifária.
type Offer struct {
	DiscountedWithPromocode bool            `json:"discountedWithPromocode"`
	EarnedPoints            json.RawMessage `json:"earnedPoints"`
	FareCalculationRate     json.RawMessage `json:"fareCalculationRate"`
	GroupFlights            []GroupFlight   `json:"groupFlights"`
	IDOffer                 int             `json:"idOffer"`
	InCabin                 json.RawMessage `json:"inCabin"`
	InCommercialFareFamily  json.RawMessage `json:"inCommercialFareFamily"`
	InFareFamily            json.RawMessage `json:"inFareFamily"`
	InFareFamilyHierarchy   json.RawMessage `json:"inFareFamilyHierarchy"`
	Inbound                 *BoundDetail    `json:"inbound"`
	ListPaxPoints           json.RawMessage `json:"listPaxPoints"`
	ListPaxPrice            []Price         `json:"listPaxPrice"`
	MinPeriod               json.RawMessage `json:"minPeriod"`
	OutCabin                string          `json:"outCabin"`                // M, W, C
	OutCommercialFareFamily string          `json:"outCommercialFareFamily"` // ex.: NBRND
	OutFareFamily           string          `json:"outFareFamily"`           // ex.: DISCINT, BASINT, CLAINT
	OutFareFamilyHierarchy  int             `json:"outFareFamilyHierarchy"`
	Outbound                *BoundDetail    `json:"outbound"`
	ResponseID              json.RawMessage `json:"responseId"`
	TimeToThink             json.RawMessage `json:"timeToThink"`
	TotalPoints             json.RawMessage `json:"totalPoints"`
	TotalPrice              Price           `json:"totalPrice"`
}

// GroupFlight liga uma oferta aos voos de ida e volta pelo IDFlight.
type GroupFlight struct {
	IDInBound            json.RawMessage `json:"idInBound"`
	IDOutBound           int             `json:"idOutBound"` // == Flight.IDFlight
	LastSeatInBound      json.RawMessage `json:"lastSeatInBound"`
	LastSeatOutBound     json.RawMessage `json:"lastSeatOutBound"`
	MilesGoDiscount      json.RawMessage `json:"milesGoDiscount"`
	OiReference          json.RawMessage `json:"oiReference"`
	SeatInBound          json.RawMessage `json:"seatInBound"`
	SeatOutBound         json.RawMessage `json:"seatOutBound"`
	UniqueOfferReference json.RawMessage `json:"uniqueOfferReference"`
}

// BoundDetail traz os atributos tarifários por perna. Os campos em array têm
// um elemento por segmento da perna.
type BoundDetail struct {
	BagIncluded              json.RawMessage `json:"bagIncluded"`
	BreakPoint               json.RawMessage `json:"breakPoint"`
	Cabin                    []string        `json:"cabin"`
	CommercialfareFamily     []string        `json:"commercialfareFamily"`
	Conditions               json.RawMessage `json:"conditions"`
	EarnedPoints             json.RawMessage `json:"earnedPoints"`
	FareBasis                []string        `json:"fareBasis"`
	FareFamily               []string        `json:"fareFamily"`
	FareFamilyHierarchy      json.RawMessage `json:"fareFamilyHierarchy"`
	FirstClassDisclaimer     json.RawMessage `json:"firstClassDisclaimer"`
	IsMixedCabin             json.RawMessage `json:"isMixedCabin"`
	IsPrimeEconomyMixedCabin json.RawMessage `json:"isPrimeEconomyMixedCabin"`
	ListPaxPoints            json.RawMessage `json:"listPaxPoints"`
	ListPaxPrice             []Price         `json:"listPaxPrice"`
	Rbd                      []string        `json:"rbd"`
	Refundable               json.RawMessage `json:"refundable"`
	SeatsAvail               json.RawMessage `json:"seatsAvail"`
	TotalPoints              json.RawMessage `json:"totalPoints"`
	TotalPrice               Price           `json:"totalPrice"`
}

// ---------------------------------------------------------------------------
// Painéis de calendário
// ---------------------------------------------------------------------------

// DatePanel é o painel de datas vizinhas (outPanel / inPanel).
type DatePanel struct {
	Currency string          `json:"currency"`
	ListTab  []DateTab       `json:"listTab"`
	Points   json.RawMessage `json:"points"`
}

// DateTab é uma data candidata com o menor preço encontrado.
// Date e ReturnDate usam o formato DDMMYYYY.
type DateTab struct {
	Available      string          `json:"available"` // "1" quando há voos
	Date           string          `json:"date"`      // DDMMYYYY
	InBoundPoints  json.RawMessage `json:"inBoundPoints"`
	InBoundPrice   json.RawMessage `json:"inBoundPrice"`
	OutBoundPoints json.RawMessage `json:"outBoundPoints"`
	OutBoundPrice  json.RawMessage `json:"outBoundPrice"`
	ReturnDate     json.RawMessage `json:"returnDate"`
	TotalPoints    json.RawMessage `json:"totalPoints"`
	TotalPrice     Price           `json:"totalPrice"`
}

// IsAvailable informa se a data tem voos comercializáveis.
func (t *DateTab) IsAvailable() bool { return t.Available == "1" }

// OfferMatrix é a matriz ida/volta por combinação de datas.
type OfferMatrix struct {
	Currency string          `json:"currency"`
	ListTab  []MatrixTab     `json:"listTab"`
	Points   json.RawMessage `json:"points"`
}

// MatrixTab é uma célula da matriz de preços.
type MatrixTab struct {
	Available         string          `json:"available"`
	Currency          string          `json:"currency"`
	Date              string          `json:"date"` // DDMMYYYY
	InBound           json.RawMessage `json:"inBound"`
	InBoundPoints     json.RawMessage `json:"inBoundPoints"`
	InBoundPrice      json.RawMessage `json:"inBoundPrice"`
	InboundNoFlights  bool            `json:"inboundNoFlights"`
	InboundSoldOut    bool            `json:"inboundSoldOut"`
	OfferBean         *Offer          `json:"offerBean"`
	OutBound          json.RawMessage `json:"outBound"`
	OutBoundPoints    json.RawMessage `json:"outBoundPoints"`
	OutBoundPrice     json.RawMessage `json:"outBoundPrice"`
	OutboundNoFlights bool            `json:"outboundNoFlights"`
	OutboundSoldOut   bool            `json:"outboundSoldOut"`
	Points            json.RawMessage `json:"points"`
	ReturnDate        json.RawMessage `json:"returnDate"`
	TotalPoints       json.RawMessage `json:"totalPoints"`
	TotalPrice        Price           `json:"totalPrice"`
}

// ---------------------------------------------------------------------------
// Dicionários de tradução
// ---------------------------------------------------------------------------

// Translate resolve códigos IATA e de companhia para nomes legíveis.
type Translate struct {
	Airlines  map[string]Airline  `json:"airlines"`
	Locations map[string]Location `json:"locations"`
}

// Airline é o nome comercial de uma companhia.
type Airline struct {
	Code string `json:"code"`
	Name string `json:"name"`
}

// Location é um aeroporto/cidade.
type Location struct {
	City        string `json:"city"`
	Code        string `json:"code"`
	Country     string `json:"country"`
	CountryCode string `json:"countryCode"`
	Name        string `json:"name"`
}

// wallClockLayouts são os formatos aceitos por ParseWallClock, do mais
// específico para o mais geral.
var wallClockLayouts = []string{
	"2006-01-02T15:04:05.000",
	"2006-01-02T15:04:05",
	"2006-01-02T15:04",
}

// ParseWallClock interpreta os timestamps de voo da API como hora de parede,
// sem conversão de fuso.
//
// O BFM devolve "2026-09-01T23:30:00.000Z", mas o valor é a hora LOCAL do
// aeroporto — o sufixo Z mente. Duas provas independentes, do voo TP87 LIS→GRU:
//
//   - a própria SPA da TAP lê esse campo e envia departureTime "23:30:00" ao
//     endpoint /bfm/rest/booking/flights/info/retrieve;
//   - partida 23:30 e chegada 05:50 dariam 380 minutos se ambos fossem UTC, mas
//     o campo duration informa 620. Tratando como hora local (LIS UTC+1 = 22:30Z,
//     GRU UTC-3 = 08:50Z) dá exatamente 620.
//
// Consequência prática: NUNCA subtraia dois destes valores para obter duração —
// use o campo duration. E as colunas correspondentes são TIMESTAMP sem fuso.
//
// O time.Time devolvido está rotulado como UTC, mas isso é só o portador: o que
// importa é o relógio de parede. Convertê-lo de fuso produziria dado errado.
func ParseWallClock(value string) (time.Time, error) {
	// O designador de zona é descartado de propósito.
	trimmed := strings.TrimSuffix(value, "Z")
	if idx := strings.IndexAny(trimmed, "+"); idx > 10 {
		trimmed = trimmed[:idx]
	}

	for _, layout := range wallClockLayouts {
		if t, err := time.Parse(layout, trimmed); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("%q não corresponde a nenhum formato de hora de parede conhecido", value)
}

// hasContent informa se um json.RawMessage tem valor útil (nem ausente, nem
// null, nem coleção vazia).
func hasContent(raw json.RawMessage) bool {
	switch string(raw) {
	case "", "null", "[]", "{}":
		return false
	default:
		return true
	}
}
