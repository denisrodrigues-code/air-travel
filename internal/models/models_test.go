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

// TestTechnicalStopsAreTyped fixa a parada técnica do TP67 LIS→GIG, que é o
// caso que expôs a lacuna: a TAP anuncia "1 escala" para esse voo, mas
// numberOfStops é 0 porque conta apenas conexões.
//
// O campo era json.RawMessage e nunca chegava ao banco, então a coluna PARADAS
// contradizia a interface da TAP sem estar errada.
func TestTechnicalStopsAreTyped(t *testing.T) {
	var resp SearchResponse
	if err := json.Unmarshal(loadFixture(t), &resp); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	var found *Segment
	total := 0
	for i := range resp.Data.ListOutbound {
		flight := &resp.Data.ListOutbound[i]
		for j := range flight.ListSegment {
			seg := &flight.ListSegment[j]
			total += len(seg.TechnicalStops)
			if seg.FlightNumber == "67" && len(seg.TechnicalStops) > 0 {
				found = seg
				if flight.NumberOfStops != 0 {
					t.Errorf("TP67 numberOfStops = %d, esperado 0: a parada técnica "+
						"não é conexão e não deve aparecer ali", flight.NumberOfStops)
				}
			}
		}
	}

	if total != 1 {
		t.Errorf("a fixture tem %d paradas técnicas, esperado 1", total)
	}
	if found == nil {
		t.Fatal("parada técnica do TP67 não encontrada")
	}

	stop := found.TechnicalStops[0]
	if stop.Location != "CWB" {
		t.Errorf("Location = %q, esperado CWB", stop.Location)
	}
	if stop.Duration != 105 {
		t.Errorf("Duration = %d, esperado 105", stop.Duration)
	}

	// Data e hora vêm em campos separados e num terceiro formato: DD/MM/AAAA e
	// HH:MM, nem RFC3339 dos segmentos nem o layout do calendário.
	arrival, err := stop.ArrivalAt()
	if err != nil {
		t.Fatalf("ArrivalAt: %v", err)
	}
	if got := arrival.Format("2006-01-02 15:04"); got != "2026-09-01 19:55" {
		t.Errorf("ArrivalAt = %q, esperado 2026-09-01 19:55", got)
	}

	departure, err := stop.DepartureAt()
	if err != nil {
		t.Fatalf("DepartureAt: %v", err)
	}
	if got := departure.Format("2006-01-02 15:04"); got != "2026-09-01 21:40" {
		t.Errorf("DepartureAt = %q, esperado 2026-09-01 21:40", got)
	}
}

// TestTechnicalStopWithoutClockIsNotAnError separa ausência de informação de
// resposta malformada: uma escala sem horário anunciado devolve o instante zero
// e nenhum erro, porque derrubar a gravação da busca inteira por isso seria
// desproporcional. Já um valor presente e ilegível continua erro.
func TestTechnicalStopWithoutClockIsNotAnError(t *testing.T) {
	empty := TechnicalStop{Location: "CWB", Duration: 105}
	at, err := empty.ArrivalAt()
	if err != nil {
		t.Errorf("escala sem horário devolveu erro: %v", err)
	}
	if !at.IsZero() {
		t.Errorf("ArrivalAt = %v, esperado o instante zero", at)
	}

	broken := TechnicalStop{ArrivalDate: "2026-09-01", ArrivalTime: "19:55"}
	if _, err := broken.ArrivalAt(); err == nil {
		t.Error("data em ISO foi aceita; o formato da parada técnica é DD/MM/AAAA")
	}
}

// TestFlattenKeepsOutboundPrice fixa a distinção que a comparação com o site
// revelou: a TAP exibe ao usuário o preço da PERNA DE IDA, e só o total da
// viagem estava sendo guardado.
//
// O ponteiro existe para separar "a API não trouxe a perna" de "custa zero".
func TestFlattenKeepsOutboundPrice(t *testing.T) {
	var resp SearchResponse
	if err := json.Unmarshal(loadFixture(t), &resp); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	key := SearchKey{
		Origin: "LIS", Destination: "RIO", DepartDate: "01092026",
		CabinClass: "E", Market: "PT", Adults: 1,
	}
	records := FlattenOffers(key, "2026-09-01T00:00:00Z", &resp)

	withPrice := 0
	for _, rec := range records {
		if rec.OutboundPrice == nil {
			continue
		}
		withPrice++
		if *rec.OutboundPrice > rec.TotalPrice {
			t.Errorf("oferta %d: ida %.2f maior que o total %.2f",
				rec.OfferID, *rec.OutboundPrice, rec.TotalPrice)
		}
	}

	if withPrice == 0 {
		t.Fatal("nenhum registro trouxe o preço da ida")
	}

	// A contagem de paradas técnicas acompanha o registro achatado, senão o
	// relatório não teria como não contradizer o site.
	stops := 0
	for _, rec := range records {
		stops += rec.TechnicalStops
	}
	if stops == 0 {
		t.Error("nenhum registro contabilizou a parada técnica do TP67")
	}
}
