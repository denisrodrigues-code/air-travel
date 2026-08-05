package models

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// fixturePath aponta para uma resposta real de /bfm/rest/booking/availability/search
// capturada do navegador (LIS->RIO, 01/09/2026, HTTP 200, 279.287 bytes).
const fixturePath = "testdata/availability_search_response.json"

func loadFixture(t *testing.T) []byte {
	t.Helper()

	raw, err := os.ReadFile(filepath.Clean(fixturePath))
	if err != nil {
		t.Fatalf("failed to read fixture: %v", err)
	}
	return raw
}

// TestSearchResponseUnmarshal garante que os structs aceitam a resposta real.
// É a validação de tipos que evita erros "json: cannot unmarshal" em produção.
func TestSearchResponseUnmarshal(t *testing.T) {
	var resp SearchResponse
	if err := json.Unmarshal(loadFixture(t), &resp); err != nil {
		t.Fatalf("failed to unmarshal real response: %v", err)
	}

	if !resp.OK() {
		t.Errorf("OK() = false, esperado true (status = %q)", resp.Status)
	}
	if resp.HasErrors() {
		t.Errorf("HasErrors() = true, esperado false (errors = %s)", resp.Errors)
	}

	// Contagens conferidas na captura.
	if got, want := len(resp.Data.ListOutbound), 34; got != want {
		t.Errorf("len(ListOutbound) = %d, esperado %d", got, want)
	}
	if got, want := len(resp.Data.Offers.ListOffers), 105; got != want {
		t.Errorf("len(ListOffers) = %d, esperado %d", got, want)
	}
	if got, want := resp.Data.Offers.Currency, "EUR"; got != want {
		t.Errorf("Currency = %q, esperado %q", got, want)
	}
	if resp.Data.OfficeID == "" {
		t.Error("OfficeID vazio")
	}

	// Somente ida: a perna de volta não vem preenchida.
	if resp.Data.ListInbound != nil {
		t.Errorf("ListInbound = %v, esperado nil numa busca só de ida", resp.Data.ListInbound)
	}
}

// TestPriceIsFloat protege a decisão de tipar os preços como float64: a API
// devolve basePrice tanto inteiro quanto decimal.
func TestPriceIsFloat(t *testing.T) {
	var resp SearchResponse
	if err := json.Unmarshal(loadFixture(t), &resp); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	var fractional int
	for _, offer := range resp.Data.Offers.ListOffers {
		if offer.TotalPrice.Price <= 0 {
			t.Fatalf("oferta %d com preço não positivo: %v", offer.IDOffer, offer.TotalPrice.Price)
		}
		if offer.TotalPrice.BasePrice != float64(int64(offer.TotalPrice.BasePrice)) {
			fractional++
		}
	}
	if fractional == 0 {
		t.Error("nenhum basePrice fracionário na amostra: float64 deixaria de ser justificado")
	}
}

// TestSegmentTimestamps confirma que as datas dos segmentos são RFC3339.
func TestSegmentTimestamps(t *testing.T) {
	var resp SearchResponse
	if err := json.Unmarshal(loadFixture(t), &resp); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	for _, flight := range resp.Data.ListOutbound {
		if len(flight.ListSegment) == 0 {
			t.Errorf("voo %d sem segmentos", flight.IDFlight)
			continue
		}
		for i := range flight.ListSegment {
			seg := &flight.ListSegment[i]
			if _, err := seg.DepartureTime(); err != nil {
				t.Errorf("voo %d segmento %d: %v", flight.IDFlight, i, err)
			}
			if _, err := seg.ArrivalTime(); err != nil {
				t.Errorf("voo %d segmento %d: %v", flight.IDFlight, i, err)
			}
		}
	}
}

// TestFlattenOffers verifica o cruzamento entre ofertas e voos por idOutBound.
func TestFlattenOffers(t *testing.T) {
	var resp SearchResponse
	if err := json.Unmarshal(loadFixture(t), &resp); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	key := SearchKey{
		Origin: "LIS", Destination: "RIO", DepartDate: "01092026",
		CabinClass: "E", Market: "PT", Adults: 1,
	}
	records := FlattenOffers(key, "2026-09-01T00:00:00Z", &resp)

	if len(records) == 0 {
		t.Fatal("FlattenOffers não produziu registros")
	}
	if len(records) < len(resp.Data.Offers.ListOffers) {
		t.Errorf("len(records) = %d, esperado >= %d ofertas",
			len(records), len(resp.Data.Offers.ListOffers))
	}

	for _, rec := range records {
		if rec.TotalPrice <= 0 {
			t.Errorf("oferta %d sem preço", rec.OfferID)
		}
		if len(rec.Route) < 2 {
			t.Errorf("oferta %d com rota incompleta: %v", rec.OfferID, rec.Route)
		}
		if rec.Route[0] != "LIS" {
			t.Errorf("oferta %d parte de %q, esperado LIS", rec.OfferID, rec.Route[0])
		}
		if len(rec.FlightNumbers) != len(rec.Carriers) {
			t.Errorf("oferta %d: %d números de voo para %d transportadoras",
				rec.OfferID, len(rec.FlightNumbers), len(rec.Carriers))
		}
		if rec.DepartureTime == "" || rec.ArrivalTime == "" {
			t.Errorf("oferta %d sem horários", rec.OfferID)
		}
	}
}

// TestTranslateDictionaries confirma os dicionários usados para resolver códigos.
func TestTranslateDictionaries(t *testing.T) {
	var resp SearchResponse
	if err := json.Unmarshal(loadFixture(t), &resp); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	lis, ok := resp.Translate.Locations["LIS"]
	if !ok {
		t.Fatal("aeroporto LIS ausente do dicionário")
	}
	if lis.City != "Lisboa" || lis.CountryCode != "PT" {
		t.Errorf("LIS = %+v, esperado Lisboa/PT", lis)
	}

	tp, ok := resp.Translate.Airlines["TP"]
	if !ok {
		t.Fatal("companhia TP ausente do dicionário")
	}
	if tp.Name != "TAP Air Portugal" {
		t.Errorf("TP.Name = %q, esperado \"TAP Air Portugal\"", tp.Name)
	}
}

// TestNoUnknownFields usa DisallowUnknownFields para revelar campos que a API
// devolve e que o modelo ainda não representa.
//
// Falhar aqui não indica bug de desserialização — o decodificador tolerante já
// é validado nos testes acima. Serve como alerta de que a API tem informação
// não aproveitada, ou mudou.
func TestNoUnknownFields(t *testing.T) {
	dec := json.NewDecoder(bytes.NewReader(loadFixture(t)))
	dec.DisallowUnknownFields()

	var resp SearchResponse
	if err := dec.Decode(&resp); err != nil {
		t.Errorf("campo não modelado na resposta da API: %v", err)
	}
}
