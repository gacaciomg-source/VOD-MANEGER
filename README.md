# VOD Manager

Gerenciador de acervo de vídeo sob demanda. Ele unifica o catálogo de várias fontes numa
biblioteca só, entrega os vídeos aos seus clientes e guarda uma cópia local do que é assistido
— para que a segunda pessoa a abrir um filme não custe nada à fonte.

**Ele não distribui conteúdo.** As fontes são cadastradas por quem administra a instalação, e
o sistema apenas organiza, intermedia e guarda o que essas fontes já entregam.

## O problema que ele resolve

Quem opera um serviço de VOD a partir de fontes de terceiros esbarra sempre nas mesmas coisas:

- **O mesmo filme aparece seis vezes**, porque cada fonte o declara numa pasta diferente — e
  às vezes a mesma fonte o declara em três.
- **A fonte corta a entrega no meio.** O filme para na metade e o player volta ao começo. É a
  falha mais comum, e a mais invisível: para o sistema, a transmissão "terminou".
- **A fonte vence e não avisa.** Ela continua respondendo `200` e entrega, no lugar de cada
  filme, um aviso de dois quilobytes. Todo conteúdo abre com zero segundos e nada acusa.
- **Milhares de títulos numa pasta só**, porque as fontes entregam o filme mas não o gênero.
- **Cada espectador é uma conexão na fonte.** Dez pessoas no mesmo filme são dez conexões
  para os mesmos bytes.

## O que ele faz

**Catálogo unificado.** Lê fontes M3U e Xtream, normaliza títulos, reconhece o mesmo filme em
fontes diferentes e o apresenta uma vez só, com todas as origens por trás.

**Entrega com failover.** O espectador nunca vê a URL da fonte. Se a origem falha antes do
primeiro byte, o sistema tenta a próxima — e tenta fontes *diferentes*, nunca a mesma três
vezes.

**Acervo em duas camadas.** O que é assistido pode ser guardado no disco da máquina e, quando
esfria, movido para uma conta de nuvem. O disco pequeno vira área de passagem em vez de teto:
o que está quente fica perto, o que ninguém pede há dias fica longe e barato.

**Detecção do que não é filme.** Aviso de manutenção, página de erro e entrega cortada são
reconhecidos e descartados — no caminho da reprodução e no do armazenamento, com o mesmo
critério. Guardar meio filme é pior que não guardar: ele passaria a ser servido no lugar da
fonte, que está inteira.

**Organização por gênero.** Com uma chave gratuita do TMDB, o sistema classifica os títulos
sem pasta e cria as categorias sozinho.

**Painel web.** Fontes, catálogo, categorias, credenciais de clientes com cota e limite de
conexões, reproduções ao vivo, falhas explicadas por causa, e o acervo.

**Exportação.** Lista M3U e API compatível com Xtream, para os clientes usarem o player que
já têm.

## Instalação

Precisa de **Go 1.25+** e **PostgreSQL 14+**.

```bash
git clone https://github.com/gacaciomg-source/VOD-MANEGER.git && cd VOD-MANEGER
```

```bash
sudo ./scripts/instalar.sh
```

O instalador compila, cria as unidades do systemd, aplica as migrações e sobe o serviço. A
senha inicial do administrador aparece no log da primeira execução — anote, ela não é
mostrada de novo.

Para atualizar depois:

```bash
sudo ./scripts/atualizar.sh
```

Ele faz backup, baixa a versão nova, compila **antes** de parar o serviço, troca o binário e
confere se subiu — voltando à versão anterior se não subir.

## Desenvolvimento

Um comando, sem instalar banco nenhum:

```bash
go run ./cmd/vodm-dev
```

Isso baixa e sobe um PostgreSQL de verdade (sem Docker), gera a chave de cifra, aplica as
migrações, cria o administrador e sobe o painel em `http://localhost:8080`. A primeira
execução baixa ~100 MB; as seguintes são rápidas. Os dados ficam em `.vodm-dev/` e sobrevivem
ao restart.

Usuário `admin`, senha `admin-desenvolvimento`.

```bash
go test ./...
```

Os testes de integração sobem um Postgres próprio e executam as consultas de verdade — várias
delas existem porque uma consulta que compilava não executava.

## Configuração

Copie `.env.example` para `.env` e ajuste. Os essenciais:

| Variável | O que é |
|---|---|
| `VODM_DATABASE_URL` | Conexão com o PostgreSQL |
| `VODM_ENCRYPTION_KEY` | Chave mestra das credenciais. Gere com `go run ./cmd/vodmanager genkey` |
| `VODM_PUBLIC_BASE_URL` | Endereço pelo qual o mundo alcança este servidor |
| `VODM_ARMAZENAMENTO_LOCAL` | Pasta do acervo em disco |

O resto — cache, limites, TMDB — se configura pelo painel, sem reiniciar.

## Como ele é feito

Go sem framework web, PostgreSQL sem ORM, painel em JavaScript sem build. A escolha é
deliberada: o sistema roda numa VPS modesta ao lado do banco, e cada dependência a mais é uma
coisa a mais que pode quebrar num servidor que ninguém está olhando.

O código é comentado em português, e os comentários explicam **por quê**, não o quê. Boa parte
deles registra um defeito que aconteceu de verdade — é o formato mais honesto de documentação
que existe, e o que impede o mesmo erro de voltar por outro caminho.

`docs/` tem as decisões de arquitetura e o modelo de dados.

## Licença

[AGPL-3.0](LICENSE).

Em resumo: você pode usar, modificar e rodar isto no seu negócio, inclusive comercialmente.
O que a licença exige é que **quem modificar o sistema e oferecê-lo como serviço publique as
modificações**. Melhorias voltam para quem as usa, em vez de virarem produto fechado de
alguém.

## Aviso

Este software não fornece, hospeda nem distribui conteúdo. Ele se conecta a fontes que o
administrador da instalação configura, e a responsabilidade por ter direito de acessar e
redistribuir esse conteúdo é inteiramente de quem o opera.
