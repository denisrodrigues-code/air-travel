package api

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"airtravel/internal/models"
)

// dateLayouts são os formatos aceitos na entrada, do mais canônico ao mais
// tolerante. O DDMMYYYY que a API da TAP exige é detalhe interno; aqui a entrada
// é ISO por padrão.
var dateLayouts = []string{"2006-01-02", "02/01/2006", "02-01-2006", "02012006"}

// parseDate interpreta uma data de entrada em qualquer formato aceito.
func parseDate(field, value string) (time.Time, error) {
	for _, layout := range dateLayouts {
		if t, err := time.Parse(layout, value); err == nil {
			return t, nil
		}
	}
	return time.Time{}, badRequest(
		"%s: %q não é uma data válida (use AAAA-MM-DD)", field, value)
}

// Passengers é a contagem de passageiros de uma busca.
type Passengers struct {
	Adults   int `json:"adults"`
	Youths   int `json:"youths,omitempty"`
	Children int `json:"children,omitempty"`
	Infants  int `json:"infants,omitempty"`
}

// SearchRequest é o corpo de POST /api/v1/searches.
type SearchRequest struct {
	Origin        string     `json:"origin"`
	Destination   string     `json:"destination"`
	DepartureDate string     `json:"departureDate"`
	ReturnDate    string     `json:"returnDate,omitempty"`
	CabinClass    string     `json:"cabinClass,omitempty"`
	Passengers    Passengers `json:"passengers"`
	Limit         int        `json:"limit,omitempty"`
}

// toParams valida e converte para os parâmetros do scraper.
//
// O formato DDMMYYYY exigido pelo BFM é aplicado aqui: a fronteira da API fala
// ISO, o adapter fala o dialeto da TAP.
func (r SearchRequest) toParams() (models.SearchParams, error) {
	var p models.SearchParams

	origin := strings.ToUpper(strings.TrimSpace(r.Origin))
	destination := strings.ToUpper(strings.TrimSpace(r.Destination))

	switch {
	case origin == "":
		return p, badRequest("origin é obrigatório")
	case destination == "":
		return p, badRequest("destination é obrigatório")
	case origin == destination:
		return p, badRequest("origin e destination não podem ser iguais")
	case r.DepartureDate == "":
		return p, badRequest("departureDate é obrigatório")
	}

	departure, err := parseDate("departureDate", r.DepartureDate)
	if err != nil {
		return p, err
	}

	var returnDate string
	if r.ReturnDate != "" {
		back, err := parseDate("returnDate", r.ReturnDate)
		if err != nil {
			return p, err
		}
		if back.Before(departure) {
			return p, badRequest("returnDate é anterior a departureDate")
		}
		returnDate = back.Format(models.DateLayout)
	}

	adults := max(r.Passengers.Adults, 1)
	cabin := strings.ToUpper(strings.TrimSpace(r.CabinClass))
	if cabin == "" {
		cabin = "E"
	}
	if cabin != "E" && cabin != "W" && cabin != "C" {
		return p, badRequest("cabinClass %q inválida (use E, W ou C)", cabin)
	}

	return models.SearchParams{
		Origin:      origin,
		Destination: destination,
		DepartDate:  departure.Format(models.DateLayout),
		ReturnDate:  returnDate,
		Adults:      adults,
		Youths:      max(r.Passengers.Youths, 0),
		Children:    max(r.Passengers.Children, 0),
		Infants:     max(r.Passengers.Infants, 0),
		CabinClass:  cabin,
	}, nil
}

// fromQuery monta uma SearchRequest a partir da query string, para os endpoints
// GET equivalentes.
//
// Recebe o binder, não url.Values: os inteiros precisam acumular erro de
// validação como em qualquer outro parâmetro. O chamador confere q.Err().
func fromQuery(q *query) SearchRequest {
	return SearchRequest{
		Origin:        q.raw("origin"),
		Destination:   q.raw("destination"),
		DepartureDate: q.raw("departureDate"),
		ReturnDate:    q.raw("returnDate"),
		CabinClass:    q.raw("cabinClass"),
		Passengers:    Passengers{Adults: q.positive("adults", 1)},
		Limit:         q.int("limit"),
	}
}

// Capture descreve como a coleta foi feita — útil para diagnóstico e para
// responder "qual combinação capturou este preço".
type Capture struct {
	TLSProfile string `json:"tlsProfile"`
	Engine     string `json:"engine"`
	Market     string `json:"market"`
	LatencyMs  int64  `json:"latencyMs"`
	OfficeID   string `json:"officeId,omitempty"`
	Flights    int    `json:"flights"`
	Offers     int    `json:"offers"`
	RawKey     string `json:"rawKey,omitempty"`
	// Warnings descreve falhas de persistência que não invalidaram a captura.
	Warnings []string `json:"warnings,omitempty"`
}

// methodNotAllowed responde a métodos não suportados numa rota conhecida.
func methodNotAllowed(w http.ResponseWriter, allowed ...string) {
	w.Header().Set("allow", strings.Join(allowed, ", "))
	w.WriteHeader(http.StatusMethodNotAllowed)
	fmt.Fprintf(w, `{"status":405,"code":"method_not_allowed","detail":"use %s"}`,
		strings.Join(allowed, ", "))
}
