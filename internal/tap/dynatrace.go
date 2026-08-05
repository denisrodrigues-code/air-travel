// Identificadores de telemetria (Dynatrace) que o navegador envia.

package tap

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"
)

// ---------------------------------------------------------------------------
// Identificadores de RUM (Dynatrace)
// ---------------------------------------------------------------------------

// dynatraceSession guarda os identificadores de telemetria que o Chrome envia.
// Omiti-los alteraria a lista e a ordem de cabeçalhos e, com isso, a impressão
// digital HTTP/2.
type dynatraceSession struct {
	dtpc      string
	rxVisitor string
	serverID  string
	appID     string
	traceID   string
}

func newDynatraceSession() (dynatraceSession, error) {
	traceID, err := randomHex(16)
	if err != nil {
		return dynatraceSession{}, err
	}
	visitor, err := randomUpper(32)
	if err != nil {
		return dynatraceSession{}, err
	}
	serverID, err := randomUpper(32)
	if err != nil {
		return dynatraceSession{}, err
	}

	return dynatraceSession{
		dtpc:      fmt.Sprintf("2$156801430_708h16v%s-0e0", serverID),
		rxVisitor: fmt.Sprintf("%d%s", time.Now().UnixMilli(), visitor),
		serverID:  serverID,
		appID:     "2c7fc65210728a28",
		traceID:   traceID,
	}, nil
}

// trace devolve um par traceparent/tracestate coerente para uma requisição.
func (d dynatraceSession) trace() (string, string) {
	spanID, err := randomHex(8)
	if err != nil {
		// Um span degradado é preferível a abortar a requisição.
		spanID = "0000000000000001"
	}
	traceparent := fmt.Sprintf("00-%s-%s-01", d.traceID, spanID)
	tracestate := fmt.Sprintf("c68a6847-ce3ba1e0@dtr=1;%s;1;%s;%s;%s-0",
		spanID, d.appID, d.rxVisitor, d.serverID)
	return traceparent, tracestate
}

func randomHex(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("failed to read random bytes: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

// randomUpper gera um identificador alfabético maiúsculo no estilo Dynatrace.
func randomUpper(n int) (string, error) {
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZ"
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("failed to read random bytes: %w", err)
	}
	out := make([]byte, n)
	for i, b := range buf {
		out[i] = alphabet[int(b)%len(alphabet)]
	}
	return string(out), nil
}

func boolString(v bool) string {
	if v {
		return "true"
	}
	return "false"
}

func truncate(b []byte, limit int) string {
	if len(b) <= limit {
		return string(b)
	}
	return string(b[:limit]) + "..."
}
