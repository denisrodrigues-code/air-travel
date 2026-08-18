# Medição dos perfis TLS contra a API real da TAP

**Data:** 2026-08-06, 09:15–13:10 (UTC−3) · **Rota de teste:** LIS→RIO, partida
01/09/2026, mercado PT, cabine E, 1 adulto · sem proxy · IP residencial IPv6.

Sessão de medição ao vivo de **todo o `client.profileRegistry`** contra
`booking.flytap.com`. Os números brutos ficam aqui; as conclusões foram propagadas
para o §4 do [CLAUDE.md](CLAUDE.md).

> **O registro mudou no meio da sessão:** começou com **18** perfis e terminou com
> **17** — os dois `Brave_146` da biblioteca saíram e `brave_151` entrou, por causa do
> capture do §4. Cada tabela abaixo diz quantos perfis mediu, então "18" e "17" nas
> seções seguintes não se contradizem: marcam momentos diferentes.
>
> **E mudou de novo depois:** em 07/08/2026 o `brave_151` foi **removido**, deixando
> o registro com **16**. As medições dele abaixo continuam válidas — foram elas que
> justificaram a remoção, ao mostrarem que o perfil passa e falha exatamente junto
> com o `chrome_151`, cujo ClientHello ele compartilha. Este arquivo é o registro
> cru da sessão e não foi reescrito; ver CLAUDE.md §4.

A sessão passou por **três interpretações**, e as duas primeiras estavam erradas.
O §10 registra a trajetória, porque o caminho até a conclusão certa é a parte
reaproveitável.

---

## 1. O resultado que vale

**Sem cookie, 3 dos 18 perfis atravessam a rota `search`.** Reproduzido em duas
passadas, 11:46 e 11:52.

```
firefox_135      gecko    ✅ 34 voos · 103 ofertas · 566,21 EUR · 4673–4809 ms
safari_ios_18_5  webkit   ✅ 34 voos                            · 4746–4866 ms
safari_ios_26_0  webkit   ✅ 34 voos                            · 4747–4857 ms

os 15 restantes           🔴 403 · 1577–1775 ms
```

Bloqueados: os sete `chrome_*` (com `_psk`), `brave_146`(+psk), `opera_90`,
`opera_91`, `chrome_131`, `safari_ios_18_0` e — **a novidade** — `firefox_147` e
`firefox_148`.

As latências separam-se em dois grupos limpos: recusa em ~1585 ms (antes do GDS),
aprovação acima de 4500 ms (inclui o aquecimento). É a mesma assinatura registrada
em 03/08.

### A regra de 03/08 continua valendo

O §4 do CLAUDE.md dizia: **Chromium 403, Gecko e WebKit 200.** Isso **não foi
refutado** — descreve corretamente o caso sem cookie, que é como a aplicação roda.
Os três aprovados são Gecko e WebKit; nenhum Chromium passa.

O que mudou desde 03/08 é **por perfil, não por família**: `firefox_147` e
`firefox_148` saíram, enquanto `firefox_135` e `safari_ios_18_5` continuam
passando.

### O que é novo: um jar válido destrava o Chromium

Medido duas vezes, com jars de navegadores diferentes. A segunda passada cobriu os
**17 perfis do registro** com um `cf_clearance` do Brave, e trouxe `chrome_151` como
suporte nas duas pontas para cercar a medição contra expiração do jar — ele passou
nas duas (4598 ms e 4920 ms), então a tabela é interpretável de ponta a ponta.

```
brave_151        chromium   ✅ 34 voos · 106 ofertas · 566,21 EUR · 4566 ms
chrome_133       chromium   ✅ 34 voos                           · 4580 ms
chrome_133_psk   chromium   ✅ 34 voos                           · 4524 ms
chrome_144       chromium   ✅ 34 voos                           · 4497 ms
chrome_144_psk   chromium   ✅ 34 voos                           · 4600 ms
chrome_146       chromium   ✅ 34 voos                           · 4531 ms
chrome_146_psk   chromium   ✅ 34 voos                           · 4625 ms
chrome_151       chromium   ✅ 34 voos                           · 4598 ms
safari_ios_18_5  webkit     ✅ 34 voos                           · 4849 ms
safari_ios_26_0  webkit     ✅ 34 voos                           · 4552 ms
chrome_131       gecko      🔴 403 · 1673 ms
firefox_135      gecko      🔴 403 · 1586 ms
firefox_147      gecko      🔴 403 · 1589 ms
firefox_148      gecko      🔴 403 · 1590 ms
opera_90         chromium   🔴 403 · 1591 ms
opera_91         chromium   🔴 403 · 1616 ms
safari_ios_18_0  webkit     🔴 403 · 1593 ms
```

**10 dos 17 passam** — os oito Chromium (incluindo `brave_151`) mais os dois Safari
que já passavam sem cookie.

Três conclusões que esta passada fecha, e que a de 10:11 (com jar de Chrome) tinha
deixado abertas:

1. **O clearance atravessa variantes de ClientHello dentro da família.** Um jar
   emitido a um Brave/Chromium 151 destrava `chrome_133`, `_144`, `_146` e as
   variantes `_psk` — ClientHellos diferentes entre si. Não é preciso o ClientHello
   exato de quem obteve o token.
2. **O Opera falha COM jar coerente.** Antes se dizia "nunca foi testado numa
   condição em que pudesse passar"; agora foi. Chromium com clearance Chromium
   válido, e ainda 403. O problema é o ClientHello de 2022 — o §5 deixa de ser
   hipótese e passa a ser medição.
3. **`safari_ios_18_0` falha COM jar.** Mesma leitura: o ClientHello do Safari 16 sob
   UA de Safari 18 é recusado mesmo quando o token está válido.

### Síntese

> Sem cookie, quem decide é a combinação perfil↔motor, e só três passam.
> Com um `cf_clearance` válido **e coerente com a identidade**, os Chromium
> passam também. As duas coisas são verdadeiras ao mesmo tempo.

---

## 2. Validade do jar

Dois jars foram acompanhados envelhecendo, sempre com o perfil `chrome_151`:

| jar | idade | resultado |
|---|---|---|
| Chrome | 3 min | ✅ 34 voos |
| Chrome | 17 min | ✅ 34 voos |
| Chrome | **87 min** | 🔴 403 · 1585 ms |
| Brave | 83 s | ✅ 34 voos |
| Brave | 2 min | ✅ 34 voos |
| Brave | **~16 min** | 🔴 403 · 1584 ms |
| Brave | **~24 min** | 🔴 403 · 1586 ms |

O `__cf_bm` que acompanha o `cf_clearance` tem TTL de ~30 min, mas os jars morreram
**antes** disso — o de Brave aos ~16 min. Daí `config.ClearanceTTL = 15 min`, ancorado em
`TestClearanceTTLErrsOnTheShortSide` — o teste fixa o LADO para o qual errar, não um
valor, porque as duas medições se contradizem.

O limite exato não foi medido, e os dois jars **não concordam**: o de Chrome valia aos
17 min, o de Brave já não valia aos ~16. Isso descarta uma leitura puramente de TTL —
o volume de requisições da sessão, ou o número de usos do próprio clearance, entra na
conta. Por isso `ClearanceTTL` é escolha calibrada, não constante descoberta: ele
serve para o diagnóstico dizer "talvez o jar esteja velho", não para prometer
validade.

---

## 3. Rotas de calendário — 18/18, e o bloqueio não as atinge

Medido com `cmd/routeprobe`, ferramenta escrita nesta sessão porque o `wafprobe`
só cobre a `search`.

- **`calendar`**: os 18 perfis, **363 datas · 360 com voo · 566,21 EUR · 118 KB**,
  731–813 ms.
- **`calendarReturns`**: os 18 perfis, **340 voltas · 335 com voo · 419,82 EUR ·
  47 KB**, 755–941 ms.

Payload idêntico em todos os 18 nas duas rotas. **As 72 requisições passaram sem
cookie algum, durante a janela em que a `search` recusava tudo.** Consequência
prática: um 403 na `search` não é razão para parar os modos `calendar` e
`returns`.

---

## 4. Brave: o capture desfez os perfis da biblioteca

Capturado com o powhttp em 2026-08-06, navegando de verdade no Brave 151.

**O ClientHello do Brave é o do Chrome 151.** JA4
`t13d1516h2_8daaf6152771_806a8c22fdea` — idêntico ao Chrome real e ao nosso
`chrome_151`, e idêntico também na lista crua: mesmos 15 cifradores, mesmas 16
extensões (`44cd`, `fe0d`), mesmos ML-DSA. O JA3 difere (`412e…` vs `93c3…`), mas
JA3 inclui a **ordem** das extensões, que o Chromium embaralha por conexão.

Confirmado na rede com o `cf_clearance` legítimo do próprio Brave:

| perfil | jar | resultado |
|---|---|---|
| `brave_146` | Brave | 🔴 403 · 1602 ms |
| `brave_146_psk` | Brave | 🔴 403 · 1594 ms |
| **`chrome_151`** | Brave | ✅ **34 voos · 103 ofertas · 4609 ms** |

Os perfis `Brave_146`/`Brave_146_PSK` da biblioteca reproduzem um navegador que não
existe nessa forma. **Removidos do registro**, substituídos por `brave_151`, que usa
o spec do `chrome_151` e difere só na identidade HTTP.

### Meus chutes de identidade, conferidos

| campo | eu chutei | real |
|---|---|---|
| token no UA | sem `Brave/` ✅ | `…Chrome/151.0.0.0 Safari/537.36` — idêntico ao Chrome |
| versão no UA | `146` ❌ | **`151`** |
| `Brave` no `sec-ch-ua` | sim ✅ | `"Not=A?Brand";v="99", "Brave";v="151", "Chromium";v="151"` |
| ordem das marcas | GREASE por último ❌ | **GREASE primeiro** |
| `sec-ch-ua-platform` / `-mobile` | `"Windows"` / `?0` ✅ | iguais |

O motor `Chromium`, escrito à mão do capture do Chrome, já punha o GREASE primeiro —
então a divergência ficava só nos motores de marca, exatamente onde não havia
medição. Fixado em `TestGreaseBrandComesFirst`.

### Duas coisas que eu não sabia

**O Brave não envia os cabeçalhos do Dynatrace** — sem `x-dtreferer`, `x-dtpc`,
`traceparent`, `tracestate`. Ele bloqueia o script de RUM como rastreador. Daí o
campo `Engine.BlocksTrackers`: a condição em `applyHeaders` passou a ser dupla, a
rota disparar a telemetria **e** o navegador deixá-la rodar.

**`sec-gpc: 1`** — Global Privacy Control, ligado por padrão no Brave.

E o `accept-language` veio reduzido (`pt-BR,pt;q=0.7` contra
`pt-PT,pt;q=0.9,en-US;q=0.8,en;q=0.7`). Consistente com a redução de entropia do
Brave, mas com um capture de cada navegador **não se separa** isso da hipótese de os
dois terem locales diferentes. Codificado em `Engine.AcceptLang` por fidelidade ao
capture, não como conclusão.

### O resultado NEGATIVO, que é o mais útil

O `chrome_151` passou com o jar do Brave **anunciando `"Google Chrome";v="151"`** e
**enviando** os quatro cabeçalhos de telemetria que o Brave real não envia.

Logo o WAF **não confere a marca do `sec-ch-ua` nem a presença dos cabeçalhos de
RUM** contra o clearance. As quatro divergências acima existem por fidelidade ao
navegador, não porque a rota as exija — e o espaço de hipóteses do enigma
`firefox_135` vs `147`/`148` fica bem menor.

### `brave_151` com jar fresco: passa

Remedido com um `cf_clearance` de **83 s**, controle nas duas pontas:

| # | perfil | resultado |
|---|---|---|
| 1 | `chrome_151` (controle) | ✅ 34 voos · 106 ofertas · 4712 ms |
| 2 | **`brave_151`** | ✅ **34 voos · 106 ofertas · 4683 ms** |
| 3 | `chrome_151` (controle remedido) | ✅ 34 voos · 4542 ms |
| 4 | `brave_151` (2ª passada) | ✅ 34 voos · 4802 ms |
| 5 | `brave_151` (3ª passada) | ✅ 34 voos · 4761 ms |

**As quatro divergências de fidelidade ao Brave não quebram nada.** E a conclusão
fecha nos dois sentidos: identidade Brave com jar do Brave passa, identidade Chrome
com jar do Brave também. O clearance não é sensível à marca do `sec-ch-ua` nem à
telemetria em nenhuma direção.

Sem cookie, `brave_151` é recusado — como todo Chromium. Medido com janela limpa. E
passa na rota `calendar`, o que confirma que o motor está bem formado.

### A primeira tentativa foi inconclusiva, e o controle é o que salvou

Antes desta, uma medição deu `brave_151` 🔴 403 (1613 ms). Mas o `chrome_151`
remedido em seguida **também** caiu (1593 ms): o jar havia morrido, não a identidade.

Sem aquele controle ao lado, "o `brave_151` falha e o `chrome_151` passa, então
minhas quatro divergências quebraram algo" seria a leitura óbvia — e estaria errada.
É a mesma disciplina do §10, funcionando a favor pela primeira vez em vez de contra.

**O jar dura pouco**, e menos do que o TTL nominal sugere: este morreu antes dos 30
min do `__cf_bm`, provavelmente por uso ou pelo volume acumulado da sessão.

Nota de método para recapturar: os **dois 403 que o navegador toma** antes de o
clearance aparecer são o desafio da Cloudflare, **não** bloqueio — foi o que se viu
no capture, com o `cf_clearance` emitido 2 s depois deles. E o timestamp embutido no
valor do cookie diz a idade: confira antes de medir, não depois.

## 5. Opera: inviável, e agora medido — não mais hipótese

A biblioteca para no **Opera 91 = Chromium 105, de 2022**, e `Opera_89/90/91` são o
mesmo ClientHello. Um Opera real hoje roda sobre um Chromium que a biblioteca não
tem, então qualquer combinação é incoerente por construção: ClientHello de 2022 sob
UA de 2026, ou UA de 2022 declarando um navegador que ninguém usa.

**Isso deixou de ser dedução.** Na passada dos 17 perfis com jar do Brave (§1),
`opera_90` e `opera_91` tomaram 403 enquanto os oito Chromium modernos passavam com
o MESMO token. Família certa, clearance válido, e ainda recusados: sobra o
ClientHello. A ressalva anterior — "nunca foi testado numa condição em que pudesse
passar" — está respondida.

Os dois ficam no registro e fora de `PassingProfiles`. Passaram 18/18 nas rotas de
calendário, onde valem como identidades extras.

## 6. Degradação por perfil, e por que ela limita esta sessão

Ao fim da sessão, com **bem mais de cem requisições** acumuladas do mesmo IP, o
comportamento sem cookie deixou de ser estável. Medido às ~14:05, com suporte nas
duas pontas:

```
safari_ios_26_0  🔴 403        ← suporte
firefox_135      🔴 403
firefox_147      🔴 403
safari_ios_18_5  ✅ 34 voos
safari_ios_26_0  🔴 403        ← suporte
```

O `safari_ios_26_0` falhou **nos dois suportes** e o `safari_ios_18_5` passou no
meio. Então **não é janela global fechada** — é penalidade por perfil, e ela atinge
justamente os fingerprints que esta sessão usou mais.

Compare com os mesmos perfis mais cedo:

| perfil | 11:46 sem cookie | ~13:50 com jar | ~14:05 sem cookie |
|---|---|---|---|
| `firefox_135` | ✅ | 🔴 | 🔴 |
| `safari_ios_18_5` | ✅ | ✅ | ✅ |
| `safari_ios_26_0` | ✅ | ✅ | 🔴 |

**Duas consequências de método, e as duas importam mais que os números:**

1. **A tabela com jar (§1) é válida; a leitura sem cookie do fim da sessão não é.**
   A primeira foi cercada por suporte que passou nas duas pontas. A segunda tem
   suporte falhando, então só se pode afirmar o que passou (`safari_ios_18_5`), não o
   que falhou.
2. **Não se deve reescrever `PassingProfiles` nem `DefaultTLSProfile` com este dado.**
   `firefox_135` bloqueado aqui é indistinguível de "esgotei aquele fingerprint hoje"
   e de "ele degradou como o `147` e o `148`". Separar exige **remedir depois de um
   período de silêncio**, não mais requisições agora.

Isto refina o "bloqueio por volume" descrito no §4 do `CLAUDE.md`: ele **não é
all-or-nothing global**. Na medição de 2026-08-04 ele parecia atingir todos os
perfis ao mesmo tempo; aqui atinge alguns e poupa outros, na mesma janela e no mesmo
IP.

## 7. Matriz JA4 completa — a lacuna que estava declarada

O §4 do `CLAUDE.md` registrava "**não medido:** o JA4 de `firefox_148`, `_147`,
`_135` e `safari_ios_18_5`", porque a captura havia parado antes daquela leitura.
Medida agora, com `cmd/tlsprobe` sondando um SVG estático — **sem passar pelo WAF**,
então custa zero tentativa:

| perfil | JA4 | `search` sem cookie |
|---|---|---|
| `chrome_131` | `t13d1516h2_8daaf6152771_02713d6af862` | 🔴 |
| `chrome_133` · `_psk` | `t13d1516h2_8daaf6152771_d8a2da3f94cd` | 🔴 |
| `chrome_144` · `_psk` | `t13d1516h2_8daaf6152771_d8a2da3f94cd` | 🔴 |
| `chrome_146` · `_psk` | `t13d1517h2_8daaf6152771_dcad5a053991` | 🔴 |
| `chrome_151` | `t13d1516h2_8daaf6152771_806a8c22fdea` | 🔴 |
| `brave_151` | `t13d1516h2_8daaf6152771_806a8c22fdea` | 🔴 |
| `opera_90` · `opera_91` | `t13d1516h2_8daaf6152771_e5627efa2ab1` | 🔴 |
| **`firefox_135`** | `t13d1715h2_5b57614c22b0_a54fffd0eb61` | **✅** |
| `firefox_147` | `t13d1717h2_5b57614c22b0_68c5a8c2958d` | 🔴 |
| `firefox_148` | `t13d1917h2_4d8ed5baf28e_3cbfd9057e0d` | 🔴 |
| `safari_ios_18_0` | `t13d2014h2_a09f3c656075_14788d8d241b` | 🔴 |
| **`safari_ios_18_5`** | `t13d2014h2_a09f3c656075_e42f34c56612` | **✅** |
| **`safari_ios_26_0`** | `t13d2013h2_a09f3c656075_7f0f34a4126d` | **✅** |

### Quatro leituras

**1. `chrome_151` e `brave_151` reproduzem o JA4 do navegador real, verificado na
rede.** Os dois compartilham o spec e produzem
`t13d1516h2_8daaf6152771_806a8c22fdea`, idêntico ao Chrome 151 e ao Brave 151 dos
captures. É a única paridade completa do projeto, e agora está confirmada de fora.

**2. As variantes `_psk` têm o mesmo JA4 da não-PSK**, nos três pares — o JA4 não
distingue `pre_shared_key`. Os JA3 diferem (`d73b59db…` vs `a19ab9f0…` etc.), então
quem quiser separá-las precisa do JA3.

**3. `chrome_133` mudou de valor, e a tabela antiga estava errada.** Ela dava
`t13d1516h**3**_…` (ALPN com h3); remedido dá **h2**, porque o `WithDisableHttp3()`
está aplicado — a medição antiga é de quando aquela opção estava removida por engano.
Consequência: **`chrome_133` e `chrome_144` são hoje indistinguíveis por JA4**, ainda
que seus ClientHellos difiram na ordem das extensões. O JA4 as ordena antes de
hashear, o que é exatamente a ressalva do §10 do `CLAUDE.md`.

**4. Os dois pares que estreitam o enigma da rota.** É o achado que vale mais aqui:

| par | o que é IGUAL | o que difere | resultado |
|---|---|---|---|
| `firefox_135` vs `_147` | motor inteiro + **hash de cifradores** `5b57614c22b0` | extensões: **15 vs 17** | ✅ vs 🔴 |
| `safari_ios_18_5` vs `18_0` | **os dois primeiros componentes** do JA4 | só o terceiro hash | ✅ vs 🔴 |

No par Gecko, as duas extensões a mais no `147` são `session_ticket` (`0x0023`) e
`psk_key_exchange_modes` (`0x002d`). No par WebKit, offline a única diferença entre os
specs é `signature_algorithms`.

**São os dois pares mais limpos que este projeto produziu** — quase tudo igual, e o
WAF decidindo diferente. É o melhor handle que existe para o critério da rota.

> **E a hipótese fácil já morreu.** Não é o `0x0203` (ECDSA-SHA1): o
> `safari_ios_18_0` o anuncia e falha, mas o `firefox_135` **também o anuncia e
> passa**, enquanto o `firefox_147` **não** o anuncia e falha. Nenhuma das duas
> direções se sustenta.
>
> O experimento que isolaria de verdade: derivar `Firefox_147` **sem** as duas
> extensões, e `Safari_IOS_18_0` **com os sigalgs do 18_5**. Cada um muda uma coisa
> só.

## 8. Configuração que resultou

Duas mudanças, ambas ancoradas na medição do §1:

```go
// internal/config/config.go
DefaultTLSProfile = "firefox_135"        // era firefox_148, que deixou de passar

// internal/client/pool.go
var PassingProfiles = []string{
	"firefox_135", "safari_ios_18_5", "safari_ios_26_0",
}
```

Verificado ao vivo às 13:08, **sem uma única rotação**:

```
INFO busca persistido origem=LIS destino=RIO ofertas=106 voos=34
Buscas: 1 total | 1 concluídas | 0 falhas | 106 ofertas coletadas
```

### Dois nomes que parecem certos e não são

Ambos foram cometidos ao editar `PassingProfiles` à mão, e agora há teste para os
dois (`TestPassingProfilesMatchTheMeasurement`).

**`"Firefox_135"` com maiúscula derruba a aplicação.** A chave do registro é
minúscula; `Firefox_135` é o nome da *variável Go* na biblioteca. Como `Rotate` é
ligado por padrão, o pool falha ao montar e nem o `scraper` nem a `api` sobem:

```
ERROR execução falhou err="failed to build fingerprint pool: perfil \"Firefox_135\" sem motor associado"
```

**`safari_ios_18_0` no lugar do `18_5` é o erro silencioso, e o pior.** Um
caractere de diferença, e o `18_0` é justamente o perfil medido bloqueado nas duas
condições. A rotação gastaria uma requisição e um 403 nele a cada volta, sem nada
indicando o motivo.

---

## 9. O erro de teste que a rede expôs

A primeira versão deste documento afirmava:

> "`Safari_IOS_18_0`, `Safari_16_0` e `Safari_15_6_1` são byte a byte iguais ao
> `Safari_IOS_18_5`."

**Falso**, e havia um teste verde afirmando isso. O helper de comparação olhava os
**tipos** das extensões, não o conteúdo — então não via a diferença que o servidor
vê. Refeita a comparação incluindo os bytes:

| relação real | |
|---|---|
| `Safari_IOS_18_0` ≡ `Safari_16_0` ≡ `Safari_15_6_1` | o fingerprint do Safari de **2022** |
| `Safari_IOS_18_5` | distinto de todos (não anuncia `0203`, ECDSA-SHA1) |
| `Safari_IOS_26_0` | distinto de todos (não envia padding) |
| `Opera_89` ≡ `Opera_90` ≡ `Opera_91` ≡ `Chrome_103` ≡ `Chrome_105` | confirmado por conteúdo |

Consequência concreta: **o motor `WebKitIOS18` anuncia Safari 18.0 sobre um
ClientHello de Safari 16.** É a incoerência que o `engine.go` existe para impedir,
introduzida por confiar no nome do perfil da biblioteca — e o WAF a recusou nas
duas passadas, enquanto os outros dois Safari passaram.

`fingerprintOfProfile` em `internal/client/profiles_added_test.go` agora compara os
bytes. A lição: **uma comparação mais barata que a do adversário não mede nada.**

---

## 10. As três interpretações, e por que as duas primeiras estavam erradas

Esta seção existe porque o caminho é mais reaproveitável que o destino.

### 10.1 "É bloqueio por volume" — errado

Das 09:15 às 09:53, cinco checagens de controle do `wafprobe` foram recusadas. A
ferramenta se recusou a medir e recomendou esperar. **Esperei 38 minutos por uma
janela que não existia.**

A causa real: o `-control` do `wafprobe` tem `config.DefaultTLSProfile` como
padrão, que era `firefox_148` — um perfil que **deixou de passar**. Controle morto,
recusa permanente, diagnóstico de "espere".

### 10.2 "O cookie é O fator, a regra do motor caiu" — exagerado

A captura do navegador e o experimento do §1 mostraram o jar destravando o
`chrome_151`. Concluí que a regra "Chromium 403 / Gecko 200" estava **refutada**.

Não estava. Eu havia medido os 18 **apenas com jar**, e naquela condição sete
Chromium passam. Faltava a medição sem jar — que é a condição em que a aplicação
roda, e na qual a regra continua descrevendo o comportamento exatamente.

Confundi "o cookie destrava o Chromium" com "o cookie é a única variável".

### 10.3 A leitura que fecha tudo sem contradição

| observação | explicação |
|---|---|
| `chrome_151` + jar fresco, 10:11 → ✅ | jar coerente destrava Chromium |
| `chrome_151` + jar de 87 min, 11:36 → 🔴 | jar expirado ⇒ vale a regra sem cookie |
| `safari_ios_26_0` + jar velho, 11:43 → ✅ | WebKit passa sem cookie de qualquer forma |
| `firefox_148` recusado 09:15–09:53 | **não era volume** — é esse perfil que caiu |
| 15 dos 18 recusados sem cookie, 11:46 | a regra de 03/08, intacta |

### O que quebrou o impasse

**Não foi outra medição minha — foi um oráculo externo.** A observação de que a
busca funcionava normalmente no navegador real, na mesma janela em que a ferramenta
reportava tudo bloqueado, custou uma pergunta e valeu mais que cinco execuções do
`wafprobe`.

A lição de método, e é a terceira vez que ela aparece neste projeto — as outras duas
estão no §4 do [CLAUDE.md](CLAUDE.md), sobre generalizar de uma família de motores
para todas e de uma medição pontual para uma regra: **generalizar de uma condição de
medição para uma propriedade do sistema.** Desta vez o agravante é que a
generalização foi *codificada numa ferramenta*, onde passou a dar conselho errado com
autoridade.

---

## 11. Pendências

- [x] ~~O `-control` do `wafprobe` não deve herdar `DefaultTLSProfile`.~~
      **RESOLVIDO.** O controle virou uma cadeia: `-control auto` (novo padrão)
      percorre `client.PassingProfiles` até uma referência passar. Se alguma passa, a
      janela está aberta e as que falharam antes dela são apontadas como perfis
      envelhecidos; só quando **nenhuma** passa é que a hipótese global se sustenta e
      a ferramenta se recusa a medir. Verificado nos três caminhos, incluindo com uma
      referência morta introduzida de propósito no topo da cadeia. O remedir do fim
      usa a referência que passou, não o primeiro nome. Ver `CLAUDE.md` §4.
- [x] ~~Testar o Brave com jar do próprio Brave.~~ **FEITO** (§4): o ClientHello do
      Brave é o do Chrome 151, os perfis `Brave_146` da biblioteca não o reproduzem e
      foram removidos, e `brave_151` os substitui com identidade tirada de capture.
- [x] ~~Remedir `brave_151` com jar FRESCO.~~ **FEITO** (§4): passa, três passadas,
      com controle nas duas pontas. As quatro divergências de fidelidade ao Brave não
      quebram nada.
- [x] ~~Não investir no Opera até a biblioteca ganhar um perfil moderno.~~
      **CONFIRMADO por medição** (§5): `opera_90`/`opera_91` tomaram 403 com jar
      Chromium válido, enquanto os oito Chromium modernos passavam com o mesmo token.
      Não é falta de cookie nem condição não testada — é o ClientHello de 2022.
- [ ] **REMEDIR `firefox_135` e `safari_ios_26_0` depois de um período de silêncio.**
      Os dois foram recusados sem cookie ao fim da sessão (§6), com `safari_ios_18_5`
      passando entre eles — então é penalidade por perfil, não janela fechada. Como
      `firefox_135` é `DefaultTLSProfile` e ambos estão em `PassingProfiles`, importa
      saber se degradaram como o `147`/`148` ou se apenas se esgotaram hoje. **Não
      mexer nas duas listas com o dado atual**: ele não separa as duas hipóteses, e
      mais requisições agora só pioram a medição.
- [ ] **`safari_ios_18_0` é candidato a remoção** (§6): ClientHello de 2022 sob UA
      de 2024, medido bloqueado duas vezes.
- [ ] **Por que `firefox_135` passa e `firefox_147`/`_148` não**, tendo motor
      `Gecko` idêntico — mesmo User-Agent, mesmos cabeçalhos, mesma ordem. A única
      diferença é o ClientHello: o 135 não traz os cifradores DHE (`0x0033`,
      `0x0039`) nem as extensões `session_ticket` e `psk_key_exchange_modes` que
      147 e 148 trazem. Reproduzido em duas passadas, sem explicação.
- [ ] **A identidade HTTP do Opera segue não conferida** contra captura: o
      mapeamento Opera 91 → Chromium 105 e os builds (`OPR/91.0.4516.20`) são
      escolha, não medição.
- [ ] **Quanto tempo o jar vale exatamente** (§2): vale aos 17 min, não vale aos 87.

---

## 12. Ressalvas

- **Preços mudam todo dia.** Os 566,21 · 419,82 EUR atestam que as respostas eram
  iguais entre perfis na mesma janela; não servem como referência futura.
- **Nenhuma medição isola o que exatamente o WAF compara.** Sabe-se que a decisão
  depende da combinação ClientHello + identidade HTTP, que é reproduzível por
  perfil, e que um jar coerente muda o resultado. Sabe-se também, agora, o que ele
  **não** compara: a marca do `sec-ch-ua` e a presença dos cabeçalhos de RUM (§4). O
  critério interno continua desconhecido.
- **Dois jars foram testados**, um de Chrome 151 e um de Brave 151. O de Firefox,
  que é a hipótese natural para fazer os Gecko passarem com jar, continua não medido —
  e ganhou relevância: com jar de Chromium os três Gecko são recusados (§1).
- **A degradação por perfil (§6) contamina qualquer leitura sem cookie do fim da
  sessão.** As conclusões sem cookie deste documento vêm da medição de 11:46, cercada
  por suporte; as do fim valem só para o que passou.
- **`cookies.txt` é gitignored** e contém `cf_clearance` atrelado ao IP. Aqui estão
  resultados, não o jar.
- **`go test ./...`: 210 testes, todos passando.** A falha de
  `TestPoolRepeatedReportsDoNotLookGlobal`, presente no início da sessão, era
  acoplamento à ordem de `PassingProfiles` e foi corrigida.
