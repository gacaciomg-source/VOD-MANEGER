# VOD Manager

Servidor VOD intermediário: catálogo unificado de múltiplas fontes (M3U / Xtream),
proxy com cache-while-streaming, Direct Stream, failover, deduplicação de download e
preservação de conteúdo.

> **Status: Fase 1 concluída. Fase 2 na parte 1** (normalização, parsers e matching
> inicial — puros e testados). Sincronização real, cache, streaming e painel web ainda
> não existem — ver [docs/05](docs/05-fases.md) para o plano de fases.

## Começando (desenvolvimento — um comando, sem instalar nada)

**Todos os comandos precisam ser executados de dentro da pasta do projeto.**

```bash
cd C:\Users\gustavo\Desktop\vod\gertente
```

```bash
go run ./cmd/vodm-dev
```

Isso baixa e sobe um PostgreSQL de verdade (sem Docker, sem instalação no sistema), gera e
guarda a chave de cifra, aplica as migrações, cria o administrador e sobe a API em `:8080`.
Na primeira execução leva alguns minutos porque baixa o Postgres (~100 MB); depois é rápido.

Os dados ficam em `.vodm-dev/` e sobrevivem ao restart. Usuário `admin`, senha
`admin-desenvolvimento`.

Em outro terminal, faça login e guarde o cookie:

```bash
curl -c cookies.txt -X POST http://localhost:8080/api/v1/auth/login -H "Content-Type: application/json" -d "{\"username\":\"admin\",\"password\":\"admin-desenvolvimento\"}"
```

Cadastre uma fonte:

```bash
curl -b cookies.txt -X POST http://localhost:8080/api/v1/sources -H "Content-Type: application/json" -d "{\"name\":\"Minha Fonte\",\"kind\":\"xtream\",\"base_url\":\"http://seu-servidor.exemplo\"}"
```

Grave a credencial dela (ela é cifrada e nunca mais sai do servidor):

```bash
curl -b cookies.txt -X PUT http://localhost:8080/api/v1/sources/1/credentials -H "Content-Type: application/json" -d "{\"username\":\"seu-usuario\",\"password\":\"sua-senha\"}"
```

Teste e sincronize:

```bash
curl -b cookies.txt -X POST http://localhost:8080/api/v1/sources/1/test
```

```bash
curl -b cookies.txt -X POST http://localhost:8080/api/v1/sources/1/sync
```

Veja o catálogo:

```bash
curl -b cookies.txt "http://localhost:8080/api/v1/contents?limit=10"
```

## Produção

```bash
go run ./cmd/vodmanager genkey
```

Copie `.env.example` para `.env`, cole a chave gerada em `VODM_ENCRYPTION_KEY`, ajuste
`VODM_DATABASE_URL` para o seu PostgreSQL e rode `go run ./cmd/vodmanager` (ou
`docker compose up`). A montagem da aplicação é a mesma do modo de desenvolvimento —
`internal/app` é compartilhado pelos dois binários.

## Testes

```bash
go test ./...
```

Os testes de integração usam um Postgres real: ou o de `VODM_TEST_DATABASE_URL`, ou um
Postgres embutido que o próprio teste baixa e sobe (sem Docker). `go test -short ./...`
roda só os unitários.

## O que existe hoje (Fase 1)

- Autenticação: usuários com papéis (admin/operator/viewer), senha Argon2id, sessão em
  cookie httpOnly, tokens de API, rate limit de login.
- Fontes: CRUD completo, prioridade reordenável, limites de conexão/download/banda.
- Credenciais das fontes cifradas com AES-256-GCM, amarradas à linha por AAD, nunca
  serializadas em nenhuma resposta.
- Log estruturado, tabela de eventos auditável, métricas Prometheus, `/healthz`, `/readyz`.
- Gate de papéis (`manager`/`node`/`all`) pronto para separar Manager e Nodes depois.

## O que existe hoje (Fase 2, parte 1)

- Contrato de ingestão: `RawItem` (fiel à fonte) → `NormalizedItem` (interpretado).
- Parsers puros de M3U e de API compatível com Xtream — **sem HTTP e sem credenciais**.
- Normalização com **procedência**: cada campo diz de onde veio e por qual regra versionada.
- Matching inicial com pesos calibrados e fila de revisão manual.
- `unresolved`: item de série sem temporada/episódio nunca vira filme por descarte.
- Dois testes de guarda: sincronização não abre URL de mídia; fixtures sem credencial.

## Documentação

| Documento | Conteúdo |
|---|---|
| [01 — Visão e Stack](docs/01-visao-e-stack.md) | O que é, princípio central, stack e justificativas, decisões D1–D8 |
| [02 — Modelo de Dados](docs/02-modelo-de-dados.md) | Entidades, tabelas, índices, separação origem × público |
| [03 — Fluxos](docs/03-fluxos.md) | Requisição VOD, cache hit/miss, failover, download compartilhado, sync, matching, órfão, quarentena, arquivamento, restauração, limites |
| [04 — Estrutura, APIs, Riscos e Testes](docs/04-estrutura-apis-riscos-testes.md) | Árvore de arquivos, superfície de API, 14 riscos, estratégia de testes, latência, economia de conexões |
| [05 — Fases](docs/05-fases.md) | 11 fases com critérios de aceite, preparação Manager/Nodes |
| [06 — Relatório da Fase 1](docs/06-fase-1-relatorio.md) | O que foi entregue, o que foi testado e **o que não foi verificado** |
| [07 — Contrato Normalizado](docs/07-contrato-normalizado.md) | Fase 2: contrato de ingestão (RawItem → NormalizedItem → entidades), orçamento de requisições, política de fixtures |
| [08 — Fase 2, parte 1](docs/08-fase-2-parcial.md) | Normalização, parsers, matching inicial: o que foi entregue, o que os testes descobriram e as limitações pendentes das amostras |

## Ideia em uma frase

> **O disco é o buffer, a RAM não é**: uma conexão à origem por conteúdo, escrita
> sequencial em disco, N clientes lendo o mesmo arquivo em ritmos independentes.

## Uso pretendido

O sistema é operado por um administrador que declara possuir autorização sobre as fontes
e conteúdos cadastrados. Implementação autoral; compatibilidade de protocolo é
reimplementação a partir de formatos observáveis, sem reaproveitamento de código,
esquema ou material de terceiros.
