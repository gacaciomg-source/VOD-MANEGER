# VOD Manager — 06. Relatório da Fase 1

> Data: 2026-08-18 · Ambiente: Windows 10, Go 1.26.5, Postgres 16 (embutido nos testes)

---

## 1. O que foi entregue

| Item da Fase 1 (doc 05) | Estado |
|---|---|
| Repositório Go, Makefile, Dockerfile, docker-compose | entregue |
| `internal/config` com validação no boot | entregue e testado |
| Registro de módulos com gate por `ROLE` | entregue e testado |
| Migrations iniciais (7 tabelas) + migrador com advisory lock | entregue e testado |
| `auth`: Argon2id, sessão em cookie httpOnly, token de API, papéis, rate limit | entregue e testado |
| `store`: pgx/v5 com SQL à mão | entregue e testado |
| `api`: healthz, readyz, auth, CRUD de fontes, credenciais cifradas, eventos | entregue e testado |
| Logging estruturado + tabela `events` + `/metrics` | entregue e testado |
| Testes unitários e de integração | entregue, **verdes** |
| README / documentação | entregue |

**Não entrou** (conforme combinado): sync, cache, streaming, painel web.

---

## 2. Resultado dos testes

`go vet ./...` limpo · `gofmt -l .` vazio · `go test -count=1 ./...` **verde**.

**Unitários** — `internal/roles` (7), `internal/cryptobox` (9), `internal/auth` (11),
`internal/config` (7), `internal/db` (2).

**Integração contra Postgres real** — 19 testes, todos passando:

```
TestBootDoBinario                              14.09s   ← critério de aceite da fase
TestHealthzEReadyz / TestMetricsExpostas
TestLoginFluxoCompleto / TestLoginRateLimit
TestFontesCRUDViaAPI / TestPapelViewerNaoEscreve
TestCredencialDaFonteNuncaVoltaEmRespostaDaAPI ← promessa da D7
TestTokenDeAPIAutentica / TestEventosSaoRegistradosPelaAPI
TestRotasInexistentesEMetodoErrado
TestUsuarioCicloDeVida / TestSessaoSoRetornaQuandoValida
TestSessaoDeUsuarioDesabilitadoNaoAutentica
TestTokenDeAPIRespeitaExpiracaoERevogacao
TestFonteCRUDEValidacoesDoSchema / TestReordenarFontesReescreveAPrioridade
TestCredencialDaFonteFicaCifradaNoBanco / TestEventosGravamEFiltram
```

`TestBootDoBinario` compila o binário, sobe o processo de verdade contra um Postgres
real, espera o `/readyz`, faz login com o administrador criado no boot e confere que nem
a senha nem a chave mestra aparecem no log de partida. É o equivalente automatizado do
`docker compose up` do critério de aceite.

### Bugs encontrados pelos próprios testes durante a fase

1. `redactDSN` devolvia string vazia para DSN sem credencial embutida — o log de partida
   perderia o endereço do banco.
2. `CreateSource` falhava no Postgres por inferência de tipo em `coalesce($n, '{}')`:
   faltavam casts explícitos para `text[]`.
3. `TestBootDoBinario` dependia da ordem de execução — um usuário deixado por outro teste
   fazia o bootstrap (idempotente por desenho) ser pulado.

---

## 3. O que NÃO foi verificado — declarado explicitamente

| Item | Motivo | Como validar |
|---|---|---|
| **Detector de corrida (`-race`)** | exige compilador C; esta máquina não tem gcc/mingw | roda no CI (Linux). Localmente: instalar mingw-w64 e `make test-race` |
| **`docker compose up`** | Docker não está instalado nesta máquina | o `TestBootDoBinario` cobre a mesma fiação sem Docker; o compose em si permanece **não executado** |
| **Pipeline de CI** | não há repositório remoto configurado | `.github/workflows/ci.yml` está escrito mas **nunca executou** |
| **Imagem Docker** | idem | `Dockerfile` escrito, **não construído** |

Nenhum desses itens é reportado como funcionando.

**Decisão do administrador (2026-08-18):** estes quatro itens ficam registrados como
**pendências abertas**, não como bloqueio. Serão executados no CI/Linux ou quando houver
Docker disponível. O desenvolvimento segue para a Fase 2. Esta lista permanece neste
documento e deve ser revisitada — não removida — quando cada item for de fato executado.

---

## 4. Ajustes de stack em relação à proposta aprovada

Dois, ambos já registrados na doc 01 §3:

1. **`sqlc` → pgx/v5 com SQL à mão.** O codegen exigiria uma ferramenta extra no ambiente
   sem ganho no volume de queries desta fase. A camada `store` isola isso: adotar `sqlc`
   depois não afeta os chamadores.
2. **`golang-migrate` → migrador próprio (~140 linhas).** Traz menos dependência e o mesmo
   resultado: cada migração numa transação, sob `pg_advisory_lock` — que é justamente o
   mecanismo que permitirá vários nós subirem juntos sem corrida de schema.

---

## 5. Decisões de implementação que valem registro

- **Nenhuma escrita síncrona no Postgres no caminho de resposta de dados quentes.** Já
  vale como disciplina desde agora; no `edge` (Fase 6) será obrigatório.
- **AAD amarrando o ciphertext à linha.** A credencial da fonte 42 não decifra na linha da
  fonte 99, mesmo com a chave mestra correta. Há teste para isso.
- **Login gasta o mesmo tempo para usuário inexistente**, via hash de comparação, e
  devolve resposta byte a byte idêntica — testado.
- **Campo JSON desconhecido é recusado** (400). Um erro de digitação em `max_connections`
  falharia em silêncio e o limite da fonte simplesmente não valeria.
- **`kind` da fonte é imutável.** Trocar o tipo invalidaria todas as variantes derivadas.
- **Constraint `max_concurrent_downloads <= max_connections`** no schema: baixar consome
  conexão, então o inverso é incoerente por definição.
- **Reordenação exige a lista completa** de fontes, em transação. Lista parcial deixaria
  prioridades duplicadas e a ordem de failover ambígua.
- **Bootstrap do admin é idempotente e nunca reseta senha existente** — caso contrário a
  variável de ambiente viraria uma porta dos fundos permanente.

---

## 6. Preparação Manager/Nodes — o que já está no lugar

Conforme combinado, as quatro costuras da doc 05:

1. **Registro de módulos com gate por papel** — implementado e testado
   (`internal/roles`). Hoje só o módulo `api` (papel `manager`) existe; o log de partida
   registra os módulos habilitados. Um `ROLE` sem módulo derruba o boot com erro claro.
2. **Injeção de dependências, sem estado global** — `api.Deps` recebe tudo; não há
   variável de pacote com estado.
3. **`node_id` em `events` desde a primeira migração** — histórico não precisará migrar.
4. **Só o Manager aplica migrações**; o migrador usa `pg_advisory_lock`, de modo que
   subir vários processos ao mesmo tempo não gera corrida de schema.

Ainda **não** existe (e não deveria): API interna manager↔node, roteamento de cliente,
replicação de cache, service discovery.

---

## 7. Como rodar

```bash
go run ./cmd/vodmanager genkey     # gere a chave e coloque no .env
go test ./...                      # tudo (integração baixa um Postgres embutido)
go test -short ./...               # só unitários, rápido
```

Para usar um Postgres próprio nos testes em vez do embutido:
`VODM_TEST_DATABASE_URL=postgres://...`

---

## 8. Próximo passo

Fase 2 — `M3UProvider`, `XtreamProvider`, staging, diff e scheduler. Aguardando sua
aprovação, e com duas perguntas em aberto para o início dela:

1. Você tem uma fonte M3U e uma Xtream reais para eu usar como referência de formato
   (podem ser amostras anonimizadas do `#EXTINF` e da resposta do `player_api.php`)?
   Sem isso, construo sobre fixtures sintéticas e a compatibilidade com as suas fontes
   só será confirmada quando você testar.
2. Confirma que o parser de séries deve cobrir os padrões em português
   (`T01 EP02`, `Temporada 1`) além de `S01E02` e `1x02`?
