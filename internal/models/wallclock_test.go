package models

import (
	"encoding/json"
	"testing"
	"time"
)

// TestParseWallClockKeepsLocalTime protege a decisão de ignorar o sufixo Z.
//
// Regressão: usar time.RFC3339 aqui rotula 23:30 como UTC. Como o valor é hora
// local de Lisboa (UTC+1), o instante gravado ficava 1 hora errado — e 3 horas
// no caso de São Paulo.
func TestParseWallClockKeepsLocalTime(t *testing.T) {
	tests := []struct {
		in   string
		want time.Time
	}{
		{"2026-09-01T23:30:00.000Z", time.Date(2026, 9, 1, 23, 30, 0, 0, time.UTC)},
		{"2026-09-02T05:50:00.000Z", time.Date(2026, 9, 2, 5, 50, 0, 0, time.UTC)},
		{"2026-09-01T23:30:00", time.Date(2026, 9, 1, 23, 30, 0, 0, time.UTC)},
		{"2026-09-01T23:30:00+01:00", time.Date(2026, 9, 1, 23, 30, 0, 0, time.UTC)},
	}

	for _, tc := range tests {
		got, err := ParseWallClock(tc.in)
		if err != nil {
			t.Errorf("ParseWallClock(%q) erro: %v", tc.in, err)
			continue
		}
		if !got.Equal(tc.want) {
			t.Errorf("ParseWallClock(%q) = %v, esperado %v", tc.in, got, tc.want)
		}
	}

	if _, err := ParseWallClock("nao-e-data"); err == nil {
		t.Error("ParseWallClock aceitou entrada inválida")
	}
}

// TestDurationComesFromAPI demonstra por que a duração não pode ser calculada
// subtraindo os timestamps: eles estão em fusos diferentes.
//
// TP87 LIS→GRU: 23:30 → 05:50 dá 380 minutos de diferença de parede, mas a API
// informa 620 — que é o valor correto (LIS UTC+1 = 22:30Z, GRU UTC-3 = 08:50Z).
func TestDurationComesFromAPI(t *testing.T) {
	var resp SearchResponse
	if err := json.Unmarshal(loadFixture(t), &resp); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	var checked int
	for _, flight := range resp.Data.ListOutbound {
		for i := range flight.ListSegment {
			seg := &flight.ListSegment[i]
			if seg.FlightNumber != "87" || seg.DepartureAirport != "LIS" {
				continue
			}

			departure, err := seg.DepartureTime()
			if err != nil {
				t.Fatalf("partida inválida: %v", err)
			}
			arrival, err := seg.ArrivalTime()
			if err != nil {
				t.Fatalf("chegada inválida: %v", err)
			}

			wallDiff := int(arrival.Sub(departure).Minutes())
			if wallDiff == seg.Duration {
				t.Errorf("diferença de parede (%d) igual ao duration (%d): "+
					"os timestamps deixaram de estar em fusos distintos, "+
					"reveja se ParseWallClock ainda é necessário", wallDiff, seg.Duration)
			}
			if seg.Duration != 620 {
				t.Errorf("duration = %d, esperado 620 para TP87 LIS-GRU", seg.Duration)
			}
			if departure.Hour() != 23 || departure.Minute() != 30 {
				t.Errorf("partida = %02d:%02d, esperado 23:30 (hora local de Lisboa)",
					departure.Hour(), departure.Minute())
			}
			checked++
		}
	}

	if checked == 0 {
		t.Fatal("voo TP87 LIS-GRU não encontrado na fixture")
	}
}
