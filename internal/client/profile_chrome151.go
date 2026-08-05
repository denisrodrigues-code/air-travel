package client

import (
	"github.com/bogdanfinn/fhttp/http2"
	tls_client "github.com/bogdanfinn/tls-client/profiles"
	tls "github.com/bogdanfinn/utls"
)

// Algoritmos de assinatura pós-quânticos anunciados pelo Chrome 151 e ausentes
// de qualquer perfil do tls-client v1.15.1 (o mais recente é o Chrome 144).
//
// Foram lidos do JA4 do navegador real, cujo terceiro componente lista
// 0904,0905,0906 antes dos oito algoritmos clássicos. É a última divergência
// medida entre o nosso ClientHello e o do Chrome: sem eles o JA4 fica
// t13d1516h2_8daaf6152771_d8a2da3f94cd em vez de ..._806a8c22fdea.
const (
	mlDSA44 tls.SignatureScheme = 0x0904
	mlDSA65 tls.SignatureScheme = 0x0905
	mlDSA87 tls.SignatureScheme = 0x0906
)

// chrome151 replica o perfil Chrome_144 do tls-client acrescentando os três
// algoritmos acima, com o objetivo de reproduzir exatamente o JA4 do Chrome 151
// observado em booking.flytap.com:
//
//	t13d1516h2_8daaf6152771_806a8c22fdea
//
// Todo o restante — cifradores, extensões e sua ordem, SETTINGS do HTTP/2,
// connectionFlow — é idêntico ao Chrome_144, que já batia nos dois primeiros
// componentes do JA4 e no fingerprint HTTP/2.
var chrome151 = tls_client.NewClientProfile(
	tls.ClientHelloID{
		Client:               "Chrome",
		RandomExtensionOrder: false,
		Version:              "151",
		Seed:                 nil,
		SpecFactory:          chrome151Spec,
	},
	map[http2.SettingID]uint32{
		http2.SettingHeaderTableSize:   65536,
		http2.SettingEnablePush:        0,
		http2.SettingInitialWindowSize: 6291456,
		http2.SettingMaxHeaderListSize: 262144,
	},
	[]http2.SettingID{
		http2.SettingHeaderTableSize,
		http2.SettingEnablePush,
		http2.SettingInitialWindowSize,
		http2.SettingMaxHeaderListSize,
	},
	[]string{":method", ":authority", ":scheme", ":path"},
	15663105, // connectionFlow: o WINDOW_UPDATE inicial do Chrome
	nil,      // priorities
	nil,      // headerPriority
	0,        // streamID
	false,    // allowHTTP
	map[uint64]uint64{
		1: 65536, // SETTINGS_QPACK_MAX_TABLE_CAPACITY
		7: 100,   // SETTINGS_QPACK_BLOCKED_STREAMS
	},
	[]uint64{
		1,    // SETTINGS_QPACK_MAX_TABLE_CAPACITY
		0x6,  // SETTINGS_MAX_FIELD_SECTION_SIZE
		7,    // SETTINGS_QPACK_BLOCKED_STREAMS
		0x33, // SETTINGS_H3_DATAGRAM
	},
	984832, // http3PriorityParam
	[]string{":method", ":authority", ":scheme", ":path"},
	true, // http3SendGreaseFrames
)

// chrome151Spec devolve o ClientHello. A ordem das extensões reproduz a do
// Chrome_144 e não deve ser alterada: ela define o JA3 e alimenta o JA4.
func chrome151Spec() (tls.ClientHelloSpec, error) {
	return tls.ClientHelloSpec{
		CipherSuites: []uint16{
			tls.GREASE_PLACEHOLDER,
			tls.TLS_AES_128_GCM_SHA256,
			tls.TLS_AES_256_GCM_SHA384,
			tls.TLS_CHACHA20_POLY1305_SHA256,
			tls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256,
			tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256,
			tls.TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384,
			tls.TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384,
			tls.TLS_ECDHE_ECDSA_WITH_CHACHA20_POLY1305_SHA256,
			tls.TLS_ECDHE_RSA_WITH_CHACHA20_POLY1305_SHA256,
			tls.TLS_ECDHE_RSA_WITH_AES_128_CBC_SHA,
			tls.TLS_ECDHE_RSA_WITH_AES_256_CBC_SHA,
			tls.TLS_RSA_WITH_AES_128_GCM_SHA256,
			tls.TLS_RSA_WITH_AES_256_GCM_SHA384,
			tls.TLS_RSA_WITH_AES_128_CBC_SHA,
			tls.TLS_RSA_WITH_AES_256_CBC_SHA,
		},
		CompressionMethods: []byte{tls.CompressionNone},
		Extensions: []tls.TLSExtension{
			&tls.UtlsGREASEExtension{},
			&tls.KeyShareExtension{KeyShares: []tls.KeyShare{
				{Group: tls.CurveID(tls.GREASE_PLACEHOLDER), Data: []byte{0}},
				{Group: tls.X25519MLKEM768},
				{Group: tls.X25519},
			}},
			&tls.SNIExtension{},
			&tls.ApplicationSettingsExtensionNew{
				SupportedProtocols: []string{"h2"},
			},
			&tls.RenegotiationInfoExtension{
				Renegotiation: tls.RenegotiateOnceAsClient,
			},
			&tls.SupportedCurvesExtension{Curves: []tls.CurveID{
				tls.GREASE_PLACEHOLDER,
				tls.X25519MLKEM768,
				tls.X25519,
				tls.CurveP256,
				tls.CurveP384,
			}},
			&tls.UtlsCompressCertExtension{Algorithms: []tls.CertCompressionAlgo{
				tls.CertCompressionBrotli,
			}},
			&tls.SessionTicketExtension{},
			&tls.StatusRequestExtension{},
			&tls.ExtendedMasterSecretExtension{},
			&tls.SupportedVersionsExtension{Versions: []uint16{
				tls.GREASE_PLACEHOLDER,
				tls.VersionTLS13,
				tls.VersionTLS12,
			}},
			// A única alteração face ao Chrome_144: os três ML-DSA na frente.
			&tls.SignatureAlgorithmsExtension{SupportedSignatureAlgorithms: []tls.SignatureScheme{
				mlDSA44,
				mlDSA65,
				mlDSA87,
				tls.ECDSAWithP256AndSHA256,
				tls.PSSWithSHA256,
				tls.PKCS1WithSHA256,
				tls.ECDSAWithP384AndSHA384,
				tls.PSSWithSHA384,
				tls.PKCS1WithSHA384,
				tls.PSSWithSHA512,
				tls.PKCS1WithSHA512,
			}},
			&tls.SCTExtension{},
			&tls.SupportedPointsExtension{SupportedPoints: []byte{
				tls.PointFormatUncompressed,
			}},
			tls.BoringGREASEECH(),
			&tls.ALPNExtension{AlpnProtocols: []string{"h2", "http/1.1"}},
			&tls.PSKKeyExchangeModesExtension{Modes: []uint8{tls.PskModeDHE}},
			&tls.UtlsGREASEExtension{},
		},
	}, nil
}
