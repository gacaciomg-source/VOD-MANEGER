# VOD Manager — 05. Plano de Fases e Critérios de Aceite

Mantida a divisão em 11 fases do documento original. Para cada fase existe um
**critério de aceite verificável**. Sem ele atendido, a fase não é considerada concluída
e não avançamos.

Antes de iniciar cada fase, entrego: arquitetura da fase → decisões → arquivos →
schema → APIs → testes previstos. Depois: implementação → execução dos testes →
correção → documentação → só então a próxima.

---

| Fase | Escopo | Critério de aceite (verificável) |
|---|---|---|
| **1** | Esqueleto, config, Postgres + migrations, Docker, auth (usuário admin, sessão, token), API base, health check, logging estruturado, CI com testes | `docker compose up` sobe; login funciona; `/api/v1/auth/me` responde; migrations aplicam e revertem; suite de testes verde no CI |
| **2** | `M3UProvider`, `XtreamProvider`, staging, diff, scheduler, catálogo bruto por fonte | Importar um M3U de fixture e um Xtream simulado; catálogo populado; reimportar não duplica; item removido vira `available=false` e não é apagado; RAM constante com fixture de 300 MB |
| **3** | `Content` × `SourceVariant`, matching, revisão manual, tela "Meus Conteúdos" | Corpus de fixtures de matching com precisão medida e publicada; agrupamento automático ≥95; fila de revisão 80–94; decisão manual sobrevive a novo sync |
| **4** | Prioridade (drag-and-drop), roles primária/secundária/terciária, health, breaker, failover pré-primeiro-byte | Com `fakeorigin`: principal falha → secundária serve; breaker abre e fecha; prioridade manual intacta após recuperação |
| **5** | Cache Engine, `.part`, single-flight, políticas LRU/LFU/TTL/Hybrid, pin/preserve, eviction, admission control de disco | Teste: 50 clientes no mesmo conteúdo → **1** conexão à origem (asserção automatizada); eviction respeita `pinned`/`preserved`; disco cheio não corrompe |
| **6** | Edge: Direct Stream, Range, cache hit via `sendfile`, fast start, tail reader, side-channel de cauda, retomada por Range | TTFB medido em hit e miss; MP4 com `moov` no fim inicia reprodução; seek funciona; corte da origem no meio retoma sem o cliente perceber |
| **7** | M3U de saída, API compatível Xtream, `stream_credentials` revogáveis (D7) + URL assinada, XC_VM em modo proxy (D8) | XC_VM importa o catálogo e reproduz; revogar uma credencial derruba o acesso em ≤5 s e mata as sessões abertas; rotação com duas credenciais ativas funciona; teste automatizado varre **todas** as respostas públicas e falha se encontrar qualquer valor de credencial de fonte ou URL de origem |
| **8** | `StorageManager`, múltiplos storages, `LocalFilesystemProvider`; adaptadores Google Drive e S3 atrás da interface | Local completo e testado. Drive/S3: implementados e testados contra emulador (MinIO) — Google Drive real só é declarado funcional após teste com credencial sua |
| **9** | Lifecycle completo: orphan, quarentena, arquivamento, restauração | Ciclo integral testado: sumir de todas as fontes → orphan → quarentena → arquivo → reaparecer → restore reutilizando o arquivo existente **sem download** |
| **10** | Painel completo: dashboard, séries/temporadas/episódios, órfãos, cache, downloads, storages, streams, monitoramento SSE, logs, configurações | Todas as telas da seção 33 funcionando contra a API real; nenhum dado mockado |
| **11** | Carga, otimização, segurança (rate limit, sessões, permissões, proteção de arquivos), documentação final | Os 6 cenários de carga da doc 04 §4.3 executados com números publicados; revisão de segurança; runbook de operação |

---

## Escopo detalhado da Fase 1 (o que executo assim que você aprovar)

1. Repositório Go + `Makefile` + `Dockerfile` + `docker-compose.yml` (app + Postgres).
2. `internal/config`: carregamento e **validação no boot** (falha rápido, não em runtime).
3. **Registro de módulos com gate por `ROLE`** (`manager` | `node` | `all`) — a costura
   que permite separar Manager e Nodes depois sem reescrever o núcleo. Detalhe em §
   "Preparação para Manager/Nodes" abaixo.
4. Migrations iniciais: `users`, `sessions`, `api_tokens`, `settings`, `events`,
   `sources`, `source_credentials` — só o que a Fase 1 realmente usa.
   O restante do schema entra na fase que o utiliza (nada de tabela morta).
   `stream_credentials` (D7) fica definida na doc 02 e é **criada na Fase 7**.
5. `auth`: hash Argon2id, sessão por cookie httpOnly, token de API, middleware de
   permissão, rate limit no login.
6. `store`: camada de acesso a dados com pgx/v5 e SQL à mão.
7. `api`: `/healthz`, `/readyz`, `/api/v1/auth/*`, CRUD de `sources` + credenciais
   cifradas (AES-GCM, chave mestra por env, `key_version` para rotação futura).
8. Logging estruturado (`slog`) + tabela `events` + `/metrics` do Prometheus.
9. Testes: unitários de config/auth/cifra/roles + integração da API contra Postgres real.
10. `README` com como subir, como rodar testes, como configurar.

**Não** entra na Fase 1: qualquer sync, qualquer cache, qualquer streaming, qualquer
tela do painel além de um login mínimo.

---

## Preparação para separar Manager e Nodes (sem reescrever o núcleo)

Quatro costuras entram já na Fase 1. Nenhuma delas implementa multi-node — todas
eliminam a reescrita depois.

**1. Registro de módulos com gate por papel.**
Cada módulo se registra declarando em quais papéis roda. O `main` só instancia os
módulos do `ROLE` corrente. Um Node no futuro é o mesmo binário com `ROLE=node`.
```
api, panel, xtream, sync, lifecycle  → manager
edge, health                         → manager, node
```

**2. `Coordinator` como interface desde o primeiro dia.**
Single-flight, locks e slots por fonte passam por essa interface. A implementação da v1 é
in-process (`sync.Map` + semáforos). A troca por advisory locks do Postgres ou Redis é
uma implementação nova, sem tocar em quem chama.

**3. Nada de estado global implícito no `edge`.**
O `edge` recebe suas dependências por injeção (`Store`, `Coordinator`, `StorageProvider`,
`VariantSelector`). Nenhum acesso a variável de pacote. Consequência prática: um Node
recebe implementações remotas dessas mesmas interfaces sem alterar uma linha do
caminho crítico.

**4. Identidade de nó e catálogo de nós desde já.**
Todo processo tem `node_id` (estável, de config) e escreve suas métricas e eventos
rotulados com ele. As tabelas `events`, `streams` e `download_jobs` já nascem com a coluna
`node_id`. Assim, quando o segundo nó existir, o painel já sabe separar quem fez o quê —
sem migração de dados históricos.

O que **não** fazemos agora: API interna manager↔node, roteamento de cliente para nó,
replicação de cache, service discovery. São da fase multi-node.

---

## Compromissos de processo

- Nada de código fictício, endpoint falso ou função que retorna dado inventado.
- Nada de erro mascarado — falha aparece no log e na resposta.
- Nenhuma funcionalidade é reportada como concluída sem teste automatizado passando.
- Integração externa que eu não puder testar aqui é declarada explicitamente como
  **não testada**, com o passo exato para você validar.
- Código, schema, interface e textos são autorais. Onde há compatibilidade de protocolo
  (formato Xtream, M3U), é reimplementação a partir do formato observável, sem
  reaproveitar código, banco ou material de terceiros.
- O conteúdo servido é de responsabilidade do administrador, que declara possuir
  autorização sobre as fontes cadastradas.

---

## Parâmetros de referência (aprovados em 2026-08-18)

**100 streams simultâneos / 1 Gbps / 2 TB NVMe.**

Tratados explicitamente como **alvo de teste de carga, não como capacidade garantida**.

- Ninguém promete que o sistema atende 100 streams até que o cenário 3 da doc 04 §4.3
  seja executado e o número **medido** seja publicado.
- O volume real de produção hoje é sabidamente menor. O alvo existe para dar margem e
  para expor gargalos antes que eles apareçam em produção, não para virar número de
  marketing na documentação.
- Sempre que um número de capacidade aparecer em relatório, painel ou README, virá com a
  procedência: *medido em <cenário>, <hardware>, <data>* — ou não aparece.

## Papel do XC_VM (aprovado)

Modo **proxy** na v1, conforme decisão D8. Entrega direta por redirect fica como opção
futura, condicionada a ganho demonstrado em teste de carga. Não será construída agora.
