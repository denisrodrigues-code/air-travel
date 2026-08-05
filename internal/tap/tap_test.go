package tap

import (
	"os"
	"strings"
	"testing"
	"time"

	"airtravel/internal/config"
	"airtravel/internal/models"
)

// accessDeniedFixture é a página HTTP 403 real servida pelo WAF da TAP em
// /bfm/rest/booking/availability/search (92.229 bytes).
const accessDeniedFixture = "testdata/access_denied.html"

// testConfig devolve o mínimo que buildRequest consome.
func testConfig() *config.Config {
	cfg := config.Default()
	cfg.Market = "PT"
	cfg.Language = "pt"
	return cfg
}

func loadAccessDenied(t *testing.T) []byte {
	t.Helper()

	raw, err := os.ReadFile(accessDeniedFixture)
	if err != nil {
		t.Fatalf("failed to read fixture: %v", err)
	}
	return raw
}

// TestIsAccessDenied confirma o reconhecimento da página de bloqueio e que ela
// não é confundida com uma resposta legítima.
func TestIsAccessDenied(t *testing.T) {
	if !isAccessDenied(loadAccessDenied(t)) {
		t.Error("isAccessDenied() = false para a página real de bloqueio")
	}

	for name, payload := range map[string]string{
		"json de sucesso": `{"status":"200","data":{},"errors":[]}`,
		"html comum":      `<html><head><meta name="viewport" content="width=device-width"></head></html>`,
		"vazio":           ``,
	} {
		if isAccessDenied([]byte(payload)) {
			t.Errorf("isAccessDenied() = true para %s", name)
		}
	}
}

// TestBlockDetails garante que os três campos do bloqueio são extraídos, sem
// contaminação pelo HTML em volta.
//
// Regressão: uma versão case-insensitive do padrão casava o "id" de
// `width=device-width` na meta viewport e devolvia lixo no lugar do ID.
func TestBlockDetails(t *testing.T) {
	details := blockDetails(loadAccessDenied(t))

	for _, want := range []string{"Geolocation: BR", "Your IP: 2804:", "ID: a2555922af8eb7f1"} {
		if !strings.Contains(details, want) {
			t.Errorf("blockDetails() = %q, esperado conter %q", details, want)
		}
	}
	if strings.Contains(details, "device-width") || strings.Contains(details, ">") {
		t.Errorf("blockDetails() contaminado por HTML: %q", details)
	}
}

// TestHeaderProfiles fixa a divergência de cabeçalhos entre endpoints: os
// cabeçalhos de RUM do Dynatrace não são enviados em session/create.
func TestHeaderProfiles(t *testing.T) {
	tests := []struct {
		path          string
		wantDynatrace bool
		wantReferer   string
	}{
		{pathSessionCreate, false, "/booking"},
		{pathSessionReset, false, "/booking"},
		{pathAvailability, true, "/booking/flights"},
		{pathPaxTypes, true, "/booking/flights"},
		// Cada calendário tem o seu gatilho na SPA: o de partidas vem do
		// formulário, o de retornos do painel de datas.
		{pathCalendar, true, "/booking"},
		{pathCalendarReturns, true, "/booking/dates"},
		{"/bfm/rest/rota/desconhecida", true, "/booking/flights"},
	}

	for _, tc := range tests {
		got := profileFor(tc.path)
		if got.dynatrace != tc.wantDynatrace {
			t.Errorf("profileFor(%q).dynatrace = %v, esperado %v",
				tc.path, got.dynatrace, tc.wantDynatrace)
		}
		if got.refererPath != tc.wantReferer {
			t.Errorf("profileFor(%q).refererPath = %q, esperado %q",
				tc.path, got.refererPath, tc.wantReferer)
		}
	}
}

// TestJWTExpiry usa o JWT anônimo real capturado do navegador.
func TestJWTExpiry(t *testing.T) {
	const token = "eyJhbGciOiJIUzI1NiJ9." +
		"eyJzdWIiOiItYnFCaW5CaUh6NFlnKzg3Qk4rUFUzVGFYVVd5UnJuMVQvaVYvTGp4Z2VTQT0iLCJzY29" +
		"wZXMiOlsiUk9MRV9BTk9OWU1PVVNfVVNFUiJdLCJob3N0IjoidHBwcm8td2ZpYmUtdm1zczAwMDBGMC" +
		"IsInJhbmRvbSI6IlYxRUNJIiwiaWF0IjoxNzg1NzU3OTY2LCJleHAiOjE3ODU3NzU5NjZ9." +
		"UDtdgDcZ5cgGlAsZo9n8cPRNe59wFhvh8RPqJFIJJYA"

	got, err := jwtExpiry(token)
	if err != nil {
		t.Fatalf("jwtExpiry() erro inesperado: %v", err)
	}
	if want := time.Unix(1785775966, 0); !got.Equal(want) {
		t.Errorf("jwtExpiry() = %v, esperado %v", got, want)
	}

	for name, bad := range map[string]string{
		"vazio":            "",
		"dois segmentos":   "aaa.bbb",
		"payload inválido": "aaa.!!!nao-base64!!!.ccc",
	} {
		if _, err := jwtExpiry(bad); err == nil {
			t.Errorf("jwtExpiry(%s) não devolveu erro", name)
		}
	}
}

// TestBuildRequestOneWay confirma as particularidades do payload observadas na
// captura do navegador.
func TestBuildRequestOneWay(t *testing.T) {
	s := &Scraper{cfg: testConfig()}

	req := s.buildRequest(models.SearchParams{
		Origin: "LIS", Destination: "RIO", DepartDate: "01092026",
		Adults: 1, CabinClass: "E",
	})

	if !req.OneWay || req.TripType != "O" || req.RoundTripType {
		t.Errorf("só ida malformado: OneWay=%v TripType=%q RoundTripType=%v",
			req.OneWay, req.TripType, req.RoundTripType)
	}
	// O frontend repete a data de partida no returnDate quando é só ida.
	if req.ReturnDate != req.DepartureDate[0] {
		t.Errorf("ReturnDate = %q, esperado igual a DepartureDate %q",
			req.ReturnDate, req.DepartureDate[0])
	}
	if req.CmsID != "string" || req.Session != "string" {
		t.Errorf("cmsId/session = %q/%q, esperado os literais \"string\"", req.CmsID, req.Session)
	}
	if req.NumSeat != 1 || req.NumSeats != 1 {
		t.Errorf("numSeat/numSeats = %d/%d, esperado 1/1", req.NumSeat, req.NumSeats)
	}
	if req.Passengers.ADT != 1 || req.PaxSearch.ADT != 1 {
		t.Error("passengers/paxSearch não refletem 1 adulto")
	}
}
