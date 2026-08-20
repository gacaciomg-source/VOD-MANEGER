# VOD Manager — 15. Estado atual e o que falta

> Fotografia do projeto em 2026-08-19, para decidir o que vem depois de hospedar.

---

## O que está pronto e testado

| Área | Estado |
|---|---|
| Múltiplas fontes M3U e Xtream | funcionando |
| Sincronização de catálogo | 720 itens/s de média, 2.500/s de pico |
| Separação conteúdo / variantes | funcionando |
| Matching e deduplicação | 66 não resolvidos em 290.610 variantes (0,02%) |
| Failover entre fontes | antes do primeiro byte (decisão D3) |
| Direct Stream com Range/206 | verificado contra fonte real |
| Credenciais de saída revogáveis | com limite de conexões e contabilidade |
| Saída M3U e API Xtream | catálogo completo, 12 testes |
| Painel web em português | 12 telas |
| Endereço público configurável | resolve o problema do XC_VM |
| Monitoramento de recursos | tela Sistema, com veredito |
| Backup e restauração | testado com o acervo real, ida e volta |

Acervo real de referência: **16.391 filmes · 8.883 séries · 254.025 episódios ·
290.610 variantes**.

---

## O que falta, em ordem de valor

### 1. Cache (Fase 5) — o coração da arquitetura

É o que o projeto foi desenhado para chegar, e o que ainda não existe.

**O problema que resolve.** Hoje a entrega é direta da fonte: dez pessoas no mesmo filme
são dez conexões à sua fonte e o dobro de banda. Com cache, o primeiro espectador puxa da
origem e grava; do segundo em diante ninguém mais toca na fonte.

**O princípio, que já está decidido:** *o disco é o buffer, a RAM não é.* Uma conexão de
origem por conteúdo, escrita sequencial em disco, N leitores em ritmos independentes.

**Junto vêm os três modos por fonte** ([docs/09](09-modo-de-cache.md)): sempre direto,
guardar quando alguém assistir, guardar sempre — com override por conteúdo.

**O que muda no dimensionamento:** o disco deixa de ser irrelevante e vira o recurso mais
caro. A tela Sistema já avisa sobre isso.

### 2. Ações nos não resolvidos

66 itens do seu acervo estão parados esperando decisão, e o painel só os lista. Falta:
forçar como filme, trocar de pasta, verificar o link, descartar.

Precisa de uma migração para guardar a URL do item não resolvido — hoje ela não é
persistida, por decisão de não materializar URL fora da camada de transporte.

Trabalho pequeno, valor imediato: são itens do seu catálogo que hoje não rendem nada.

### 3. Limite de conteúdo por credencial

Hoje **toda credencial recebe o catálogo inteiro**. O recorte por categoria existe como
parâmetro na URL (`&conteudo=filmes`), mas não como permissão gravada — um cliente que
descubra a URL sem o parâmetro vê tudo.

Importa quando você começar a vender pacotes diferentes para clientes diferentes.

### 4. Organização de pastas e categorias

Renomear e mesclar categorias já existe. Falta reordenar, agrupar e definir como as pastas
aparecem para o cliente.

### 5. Upload de conteúdo fora das fontes

Você mencionou: subir arquivo pelo painel, ou por link de torrent. É a maior das
pendências em esforço, e a que depende mais de decisões novas (onde guardar, como nomear,
como entrar no catálogo).

### 6. Pendências técnicas da Fase 1

- `go test -race` na integração contínua
- imagem Docker (só se você quiser o caminho Docker)
- pipeline de CI

Nenhuma bloqueia o uso. São higiene de projeto para quando houver mais de uma pessoa
mexendo no código.

---

## Recomendação de ordem

**Agora: hospedar e usar.** Com clientes reais, por alguns dias. É o único jeito de
descobrir o que realmente incomoda — e a tela Sistema vai dizer se a VPS escolhida dá
conta antes de você investir em otimizar o que não é gargalo.

**Depois: cache.** É o maior salto de capacidade e de economia de banda, e o item onde a
arquitetura já está desenhada esperando a implementação.

**Junto ou logo depois: ações nos não resolvidos.** É barato e destrava 66 itens seus.

O resto pode esperar o uso real dizer o que dói.
