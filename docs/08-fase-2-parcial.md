# VOD Manager — 08. Fase 2, parte 1: normalização e parsers

> Data: 2026-08-18 · Status: **entregue e testado** · Suíte completa verde
> Parte 2 (sincronização real contra as suas fontes) aguarda as amostras anonimizadas.

---

## 1. O que foi entregue

| Item aprovado | Onde | Estado |
|---|---|---|
| `RawItem` | `internal/ingest/contract.go` | entregue |
| `NormalizedItem` | `internal/ingest/normalized.go` | entregue |
| Classificação G/O/V | `internal/ingest/fields.go` | entregue, com teste que a verifica |
| Parser M3U puro | `internal/sources/m3u/parser.go` | entregue |
| Mapper Xtream puro | `internal/sources/xtream/mapper.go` | entregue |
| Normalização + procedência | `internal/ingest/normalize.go`, `title.go`, `episode.go` | entregue |
| Matching inicial | `internal/ingest/match.go` | entregue |
| `unresolved` | `internal/ingest/contract.go` (`Rejection`) | entregue |
| `MatchSignals` | `internal/ingest/normalized.go` | entregue |
| Fixtures sintéticos | `testdata/` | entregue |
| Teste: sync não abre URL de mídia | `test/guards/no_media_access_test.go` | entregue |
| Teste: fixtures sem credencial | `test/guards/fixtures_sem_credencial_test.go` | entregue |

**Não entrou** (por combinação): cliente HTTP das fontes, scheduler, staging, diff,
persistência do catálogo. Tudo isso é a parte 2, depois da validação das amostras.

---

## 2. Procedência: como ficou

Toda decisão da normalização carrega de onde veio e por qual regra. Exemplo real de um
item vindo de um `#EXTINF`:

```
movie.title.declared   = "Interestelar (2014)"
movie.title.display    = "Interestelar"
movie.title.normalized = "interestelar"
movie.title.source     = "m3u:tvg-name"
movie.title.rule       = "title-cleanup-v1"

movie.year.value       = 2014
movie.year.source      = "m3u:tvg-name"
movie.year.rule        = "year-from-title-v1"
```

E de um episódio cujo número foi reconhecido no título:

```
episode.number_provenance.source = "m3u:tvg-name"
episode.number_provenance.rule   = "season-episode-from-title-v1#SxxExx"
```

O sufixo após `#` é o padrão textual que casou. Meses depois dá para responder
exatamente por que um item foi interpretado daquele jeito.

**Regra de versionamento:** mudar o comportamento de uma regra exige criar `v2` e manter
a `v1` enquanto houver dados normalizados por ela. Uma linha antiga do banco continua
explicável mesmo depois de o parser evoluir.

Ano de campo próprio vence ano tirado do título — e a procedência registra qual dos dois
foi usado, o que a Fase 3 aproveita para calibrar confiança.

---

## 3. Decisões de implementação que valem registro

**Credenciais nunca entram nos parsers.** O mapper Xtream não conhece host, usuário nem
senha. Ele emite `StreamRef{Kind, ID, Extension}` e a URL é materializada só na camada de
transporte. Efeito colateral bom: é estruturalmente impossível uma credencial vazar do
parser, porque ele nunca a recebe.

**`direct_source` é sanitizado.** Esse campo do Xtream traz a URL completa com credencial
no path. O payload persistido preserva a chave e substitui o valor. Há teste.

**Payload guardado é o JSON original**, não a nossa struct reserializada — senão os
campos Vendor se perderiam justamente na auditoria que eles existem para permitir.

**Tolerância de tipos.** Painéis mandam o mesmo campo como número numa instalação e como
string em outra. `flexString`/`flexInt`/`flexFloat` aceitam ambos, e um item malformado é
pulado sem derrubar a lista inteira. Objeto onde deveria haver escalar é **ignorado**,
não convertido em texto — converter inventaria um id.

**Ordem determinística nas temporadas.** O mapa de episódios do JSON não tem ordem;
ordenamos numericamente. Ordem instável faria o digest da run oscilar sem nada ter mudado.

**O digest ignora procedência e campos Vendor.** Mudar a versão de uma regra não pode
marcar o catálogo inteiro como alterado; um campo novo do fornecedor também não.

---

## 4. Três coisas que os testes descobriram

**1. Numeral romano de uma letra é ambíguo.** A regra convertia o último token romano em
arábico. "Rocky II" → "rocky 2" está certo; "Filme X" → "filme 10" está errado, e mudaria
a identidade do conteúdo. Numerais de uma letra (`I`, `V`, `X`) saíram da tabela.

**2. Os pesos do matching não funcionavam.** Os valores propostos em docs/03 §8 davam 60
para "título idêntico + ano idêntico" — abaixo do limiar de 95. O caso mais comum que
existe, duas fontes com o mesmo filme, cairia em revisão manual para sempre. Recalibrado a
partir de três âncoras, agora documentadas na tabela de docs/03 §8 e travadas por teste.

**3. O artigo inicial só era movido em títulos com 3+ palavras.** "A Origem" e "Origem, A"
não convergiam. Corrigido.

Nenhuma das três apareceria sem tabela de casos. É o argumento para a normalização ser
função pura: dá para exercitar 40 títulos em milissegundos.

---

## 5. Resultado dos testes

`gofmt` limpo · `go vet ./...` limpo · `go test -count=1 ./...` **verde**.

| Pacote | Cobertura de casos |
|---|---|
| `internal/ingest` | limpeza de título (14 casos), temporada/episódio (15 padrões + 6 negativos), normalização, rejeições (6 motivos), digest, campos garantidos, matching (10 casos + simetria), sanitização, chave de URL |
| `internal/sources/m3u` | filmes, séries, problemáticos, vírgula em título, `#EXTGRP`, atributo sem aspas, lista vazia/truncada, streaming com 20 mil itens |
| `internal/sources/xtream` | categorias, VOD, séries, episódios, tipos alternativos, itens malformados, preservação de campos Vendor, sanitização de `direct_source` |
| `test/guards` | sync não abre URL de mídia; fixtures sem credencial |

### Os dois testes de guarda

`TestSincronizacaoNaoAbreURLDeMidia` sobe um servidor HTTP real, monta uma lista M3U cujas
URLs apontam para ele, roda o pipeline inteiro (parse → normalização → matching) e exige
**zero requisições**. Se alguém adicionar um `HEAD` "só para saber o tamanho", um probe de
qualidade ou um seguimento de redirect no caminho de sincronização, o teste falha.

`TestFixturesNaoContemCredenciais` varre `testdata/` inteiro procurando usuário/senha em
query string, credencial no userinfo ou no path, token longo, IP público e domínio não
convencionado. E — importante — há um **teste do próprio teste**: `TestVarreduraDetecta`
planta seis credenciais falsas e exige que a varredura pegue todas. Sem isso, um erro de
regex tornaria a guarda um placebo silencioso.

---

## 6. Limitações conhecidas, pendentes das suas amostras

**Token embutido no PATH não é tratado.** `NormalizeURLForKey` remove query string e
fragmento, que é onde tokens voláteis normalmente ficam. Mas o padrão
`/movie/usuario/senha/123.mp4` tem credencial no path: se a senha da fonte rotacionar,
o hash muda e o item vira uma variante nova. Sem ver as URLs reais das suas fontes,
qualquer recorte de path seria adivinhação — e adivinhar errado significa ou inflar o
catálogo, ou colidir variantes distintas.

Mitigação atual: quando a fonte fornece `tvg-id` ou `stream_id`, a identidade vem dele e
o problema não existe (decisão aprovada #1). O risco fica restrito a fontes M3U sem
`tvg-id`.

**`last_modified` do Xtream não é usado.** O campo é lido e exposto em `Series`, mas
permanece **nível Vendor** e não governa nenhuma decisão. Conforme sua instrução, ele só
vira lógica depois de as amostras confirmarem que suas fontes o fornecem — e depois de
promoção documentada V→O em docs/07.

**ID numérico no path da URL não é usado como `ExternalID`.** Seria útil (protegeria
contra rotação de token), mas é uma inferência sobre estrutura de URL específica de
fornecedor. Fica como candidata a promoção, pendente das amostras.

**Nenhum fixture é real.** `testdata/m3u/` e `testdata/xtream/` foram escritos a partir do
formato público do protocolo. A compatibilidade com as suas fontes **não está
confirmada** — é o que a comparação em `docs/09-campos-das-fontes.md` vai estabelecer.

---

## 7. Pendências herdadas da Fase 1

Continuam abertas, conforme sua decisão de não bloquear: `-race` (falta compilador C
nesta máquina; roda no CI Linux), `docker compose up` não executado, CI nunca executado,
imagem Docker não construída.

---

## 8. Quando as amostras chegarem

Ordem de trabalho combinada:

1. Colocar os arquivos em `testdata/amostras/` (o teste de credenciais roda sobre eles).
2. Produzir `docs/09-campos-das-fontes.md`: tabela campo a campo confrontando amostra real
   × contrato, com o nível final (G/O/V) de cada um.
3. Registrar as promoções V→O necessárias — **primeiro no documento, depois no código**.
4. Só então ajustar parsers e implementar a parte 2 (cliente HTTP, scheduler, staging,
   diff, persistência).

As amostras servem para **validar e ajustar o contrato**, não para alterar a arquitetura
em silêncio.
