package collect

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"airtravel/internal/models"
)

// ---------------------------------------------------------------------------
// Dublês
// ---------------------------------------------------------------------------

type fakeProvider struct {
	searchErr   error
	calendarErr error
	returnsErr  error
	calls       int
}

func (f *fakeProvider) Search(context.Context, models.SearchParams) (*models.SearchResponse, []byte, error) {
	f.calls++
	if f.searchErr != nil {
		return nil, nil, f.searchErr
	}
	return &models.SearchResponse{
		Status: "200",
		Data: models.SearchData{
			ListOutbound: []models.Flight{{IDFlight: 1, Duration: 595}},
			Offers: models.Offers{Currency: "EUR", ListOffers: []models.Offer{{
				IDOffer:      1,
				GroupFlights: []models.GroupFlight{{IDOutBound: 1}},
				TotalPrice:   models.Price{Price: 615.21},
			}}},
		},
	}, []byte(`{"status":"200"}`), nil
}

func (f *fakeProvider) Calendar(context.Context, models.SearchParams) (*models.CalendarResponse, []byte, error) {
	f.calls++
	if f.calendarErr != nil {
		return nil, nil, f.calendarErr
	}
	return &models.CalendarResponse{Data: models.CalendarData{
		BestPriceForDates: []models.BestPriceForDate{{
			DepartureDate: "2026-09-01T00:00:00", InsertionDate: "2026-08-03T10:00:00",
			BestTotalPrice: 487.21, Currency: "EUR",
		}},
	}}, []byte(`{"data":{}}`), nil
}

func (f *fakeProvider) CalendarReturns(context.Context, models.SearchParams) (*models.CalendarReturnsResponse, []byte, error) {
	f.calls++
	if f.returnsErr != nil {
		return nil, nil, f.returnsErr
	}
	return &models.CalendarReturnsResponse{Data: models.CalendarReturnsData{
		Currency: "EUR",
		Returns:  []models.ReturnPrice{{ReturnDate: "2026-09-20T00:00:00", Price: 445.92}},
	}}, []byte(`{"data":{}}`), nil
}

// fakeStores registra a ordem das gravações — é o que o teste principal verifica.
type fakeStores struct {
	order      []string
	rawKeySeen string

	rawErr     error
	treatedErr error

	exists         bool
	calendarExists bool
	returnsExists  bool
	existsErr      error
	// notBefore é o corte de idade que a última checagem de retomada recebeu.
	notBefore time.Time
}

func (f *fakeStores) SaveRaw(_ context.Context, key string, at time.Time, _ []byte) (string, error) {
	f.order = append(f.order, "raw")
	if f.rawErr != nil {
		return "", f.rawErr
	}
	return "tap:raw:" + key, nil
}

func (f *fakeStores) SaveSearch(_ context.Context, _ models.SearchKey, _ *models.SearchResponse, rawKey string, _ time.Time) error {
	f.order = append(f.order, "treated")
	f.rawKeySeen = rawKey
	return f.treatedErr
}

func (f *fakeStores) SaveCalendar(_ context.Context, _ models.CalendarKey, _ *models.CalendarResponse, rawKey string, _ time.Time) error {
	f.order = append(f.order, "treated")
	f.rawKeySeen = rawKey
	return f.treatedErr
}

func (f *fakeStores) SaveReturns(_ context.Context, _ models.ReturnsKey, _ *models.CalendarReturnsResponse, rawKey string, _ time.Time) error {
	f.order = append(f.order, "treated")
	f.rawKeySeen = rawKey
	return f.treatedErr
}

// As três checagens registram o corte de idade recebido: é o que permite
// verificar que a retomada tem prazo sem precisar de banco.
func (f *fakeStores) Exists(_ context.Context, _ string, notBefore time.Time) (bool, error) {
	f.notBefore = notBefore
	return f.exists, f.existsErr
}
func (f *fakeStores) CalendarExists(_ context.Context, _ string, notBefore time.Time) (bool, error) {
	f.notBefore = notBefore
	return f.calendarExists, f.existsErr
}
func (f *fakeStores) ReturnsExists(_ context.Context, _ string, notBefore time.Time) (bool, error) {
	f.notBefore = notBefore
	return f.returnsExists, f.existsErr
}

func newService(t *testing.T, p Provider, st *fakeStores) *Service {
	t.Helper()

	svc, err := New(p, st, st, slog.New(slog.DiscardHandler), Options{
		Market: "PT",
		Now:    func() time.Time { return time.Unix(1785766450, 0).UTC() },
	})
	if err != nil {
		t.Fatalf("failed to build service: %v", err)
	}
	return svc
}

func params() models.SearchParams {
	return models.SearchParams{
		Origin: "LIS", Destination: "RIO", DepartDate: "01092026",
		Adults: 1, CabinClass: "E",
	}
}

// ---------------------------------------------------------------------------
// A política
// ---------------------------------------------------------------------------

// TestRawIsPersistedBeforeTreated é a razão de este pacote existir.
//
// O registro tratado guarda a chave do bruto (searches.raw_key). Gravar na ordem
// inversa deixaria a coluna apontando para o vazio numa falha.
//
// Antes da refatoração esta ordem estava implementada duas vezes — no
// orquestrador do CLI e nos handlers HTTP — e as duas cópias divergiam no
// tratamento de falha.
func TestRawIsPersistedBeforeTreated(t *testing.T) {
	for _, tc := range []struct {
		name string
		call func(*Service, *fakeStores) error
	}{
		{"Search", func(s *Service, _ *fakeStores) error {
			_, err := s.Search(context.Background(), params(), false)
			return err
		}},
		{"Calendar", func(s *Service, _ *fakeStores) error {
			_, err := s.Calendar(context.Background(), params(), false)
			return err
		}},
		{"Returns", func(s *Service, _ *fakeStores) error {
			_, err := s.Returns(context.Background(), params(), false)
			return err
		}},
	} {
		st := &fakeStores{}
		svc := newService(t, &fakeProvider{}, st)

		if err := tc.call(svc, st); err != nil {
			t.Fatalf("%s: erro inesperado: %v", tc.name, err)
		}
		if len(st.order) != 2 || st.order[0] != "raw" || st.order[1] != "treated" {
			t.Errorf("%s: ordem = %v, esperado [raw treated]", tc.name, st.order)
		}
		if st.rawKeySeen == "" {
			t.Errorf("%s: a chave do bruto não foi propagada ao registro tratado", tc.name)
		}
	}
}

// TestPersistenceFailureBecomesWarning fixa a segunda regra: cada coleta custa
// uma consulta ao GDS, então uma falha de gravação vira aviso, não erro.
func TestPersistenceFailureBecomesWarning(t *testing.T) {
	tests := []struct {
		name         string
		rawErr       error
		treatedErr   error
		wantWarnings int
	}{
		{"tudo ok", nil, nil, 0},
		{"redis fora", errors.New("redis"), nil, 1},
		{"postgres fora", nil, errors.New("postgres"), 1},
		{"ambos fora", errors.New("redis"), errors.New("postgres"), 2},
	}

	for _, tc := range tests {
		st := &fakeStores{rawErr: tc.rawErr, treatedErr: tc.treatedErr}
		svc := newService(t, &fakeProvider{}, st)

		res, err := svc.Search(context.Background(), params(), false)
		if err != nil {
			t.Errorf("%s: devolveu erro %v; a captura não deve ser descartada", tc.name, err)
			continue
		}
		if len(res.Warnings) != tc.wantWarnings {
			t.Errorf("%s: warnings = %v, esperados %d", tc.name, res.Warnings, tc.wantWarnings)
		}
		if res.Response == nil {
			t.Errorf("%s: a resposta capturada foi perdida", tc.name)
		}
	}
}

// TestProviderFailureIsError distingue os dois casos: falha do provedor é erro,
// falha de persistência é aviso.
func TestProviderFailureIsError(t *testing.T) {
	boom := errors.New("WAF bloqueou")
	st := &fakeStores{}
	svc := newService(t, &fakeProvider{searchErr: boom}, st)

	_, err := svc.Search(context.Background(), params(), false)
	if !errors.Is(err, boom) {
		t.Errorf("err = %v, esperado embrulhar %v", err, boom)
	}
	if len(st.order) != 0 {
		t.Errorf("gravou %v apesar da falha do provedor", st.order)
	}
}

// TestResumeSkipsWithoutTouchingProvider confirma que a retomada evita a rede.
func TestResumeSkipsWithoutTouchingProvider(t *testing.T) {
	for _, tc := range []struct {
		name  string
		store *fakeStores
		call  func(*Service) (bool, error)
	}{
		{"Search", &fakeStores{exists: true}, func(s *Service) (bool, error) {
			r, err := s.Search(context.Background(), params(), true)
			return r.Skipped, err
		}},
		{"Calendar", &fakeStores{calendarExists: true}, func(s *Service) (bool, error) {
			r, err := s.Calendar(context.Background(), params(), true)
			return r.Skipped, err
		}},
		{"Returns", &fakeStores{returnsExists: true}, func(s *Service) (bool, error) {
			r, err := s.Returns(context.Background(), params(), true)
			return r.Skipped, err
		}},
	} {
		p := &fakeProvider{}
		svc := newService(t, p, tc.store)

		skipped, err := tc.call(svc)
		if err != nil {
			t.Errorf("%s: erro inesperado: %v", tc.name, err)
		}
		if !skipped {
			t.Errorf("%s: deveria ter sido ignorado", tc.name)
		}
		if p.calls != 0 {
			t.Errorf("%s: chamou o provedor %d vez(es) apesar da retomada", tc.name, p.calls)
		}
		if len(tc.store.order) != 0 {
			t.Errorf("%s: gravou %v apesar da retomada", tc.name, tc.store.order)
		}
	}
}

// TestResumeHasADeadline: a retomada aproveita o que já foi coletado, mas só
// enquanto for recente.
//
// Sem o corte, a retomada do calendário era permanente: a calendar_key não inclui
// data, então uma rota coletada uma vez ficava marcada como pronta para sempre e a
// segunda execução de `./run.sh calendar` virava um no-op — com preços de meses
// atrás no banco e nenhum sinal disso.
func TestResumeHasADeadline(t *testing.T) {
	const agora = 1785766450
	now := func() time.Time { return time.Unix(agora, 0).UTC() }

	newSvc := func(t *testing.T, st *fakeStores, maxAge time.Duration) *Service {
		t.Helper()
		svc, err := New(&fakeProvider{}, st, st, slog.New(slog.DiscardHandler),
			Options{Market: "PT", ResumeMaxAge: maxAge, Now: now})
		if err != nil {
			t.Fatalf("failed to build service: %v", err)
		}
		return svc
	}

	t.Run("o corte é agora menos a idade máxima", func(t *testing.T) {
		st := &fakeStores{calendarExists: true}
		if _, err := newSvc(t, st, 24*time.Hour).
			Calendar(context.Background(), params(), true); err != nil {
			t.Fatalf("erro inesperado: %v", err)
		}

		want := now().Add(-24 * time.Hour)
		if !st.notBefore.Equal(want) {
			t.Errorf("notBefore = %s, esperado %s", st.notBefore, want)
		}
	})

	t.Run("idade zero desliga o corte", func(t *testing.T) {
		st := &fakeStores{calendarExists: true}
		if _, err := newSvc(t, st, 0).
			Calendar(context.Background(), params(), true); err != nil {
			t.Fatalf("erro inesperado: %v", err)
		}
		if !st.notBefore.IsZero() {
			t.Errorf("notBefore = %s, esperado o zero (sem corte)", st.notBefore)
		}
	})

	t.Run("sem retomada não há checagem", func(t *testing.T) {
		st := &fakeStores{}
		if _, err := newSvc(t, st, 24*time.Hour).
			Calendar(context.Background(), params(), false); err != nil {
			t.Fatalf("erro inesperado: %v", err)
		}
		if !st.notBefore.IsZero() {
			t.Error("consultou a retomada com resume=false")
		}
	})

	t.Run("idade negativa é erro de montagem", func(t *testing.T) {
		st := &fakeStores{}
		if _, err := New(&fakeProvider{}, st, st, slog.New(slog.DiscardHandler),
			Options{Market: "PT", ResumeMaxAge: -time.Hour}); err == nil {
			t.Error("ResumeMaxAge negativa foi aceita")
		}
	})
}

// TestValidationRejectsBeforeProvider garante que entrada inválida não gasta uma
// requisição.
func TestValidationRejectsBeforeProvider(t *testing.T) {
	bad := []models.SearchParams{
		{Destination: "RIO", DepartDate: "01092026", Adults: 1, CabinClass: "E"},
		{Origin: "LIS", DepartDate: "01092026", Adults: 1, CabinClass: "E"},
		{Origin: "LIS", Destination: "LIS", DepartDate: "01092026", Adults: 1, CabinClass: "E"},
		{Origin: "LIS", Destination: "RIO", DepartDate: "2026-09-01", Adults: 1, CabinClass: "E"},
		{Origin: "LIS", Destination: "RIO", DepartDate: "01092026", Adults: 0, CabinClass: "E"},
		{Origin: "LIS", Destination: "RIO", DepartDate: "01092026", Adults: 1},
		{Origin: "LIS", Destination: "RIO", DepartDate: "01092026", Adults: 1, CabinClass: "E", TripType: "X"},
	}

	for i, p := range bad {
		prov := &fakeProvider{}
		svc := newService(t, prov, &fakeStores{})

		if _, err := svc.Search(context.Background(), p, false); err == nil {
			t.Errorf("caso %d: params inválidos aceitos: %+v", i, p)
		}
		if prov.calls != 0 {
			t.Errorf("caso %d: chamou o provedor com entrada inválida", i)
		}
	}
}

// TestKeysComeFromService confirma que o mercado é do serviço, não do chamador.
func TestKeysComeFromService(t *testing.T) {
	svc := newService(t, &fakeProvider{}, &fakeStores{})

	if svc.Market() != "PT" {
		t.Errorf("Market() = %q, esperado PT", svc.Market())
	}

	res, err := svc.Search(context.Background(), params(), false)
	if err != nil {
		t.Fatalf("erro inesperado: %v", err)
	}
	if want := "search:LIS:RIO:01092026:OW:E:PT:1"; res.Key.String() != want {
		t.Errorf("Key = %q, esperado %q", res.Key.String(), want)
	}

	cal, err := svc.Calendar(context.Background(), params(), false)
	if err != nil {
		t.Fatalf("erro inesperado: %v", err)
	}
	if want := "calendar:LIS:RIO:E:O:PT:1"; cal.Key.String() != want {
		t.Errorf("CalendarKey = %q, esperado %q", cal.Key.String(), want)
	}
}

// TestNewRejectsMissingDependencies cobre a validação do construtor.
func TestNewRejectsMissingDependencies(t *testing.T) {
	st := &fakeStores{}
	log := slog.New(slog.DiscardHandler)

	if _, err := New(nil, st, st, log, Options{Market: "PT"}); err == nil {
		t.Error("aceitou provider nil")
	}
	if _, err := New(&fakeProvider{}, nil, st, log, Options{Market: "PT"}); err == nil {
		t.Error("aceitou treated nil")
	}
	if _, err := New(&fakeProvider{}, st, st, log, Options{}); err == nil {
		t.Error("aceitou Market vazio")
	}
}
