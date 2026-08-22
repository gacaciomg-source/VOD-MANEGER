#!/bin/bash
#
# Atualiza o VOD Manager instalado nesta máquina — tudo, num comando só.
#
# É o mesmo caminho que o botão "Atualizar" do painel dispara. Faz o que antes exigia
# rodar o instalador de novo:
#
#   1. backup ANTES de tocar em qualquer coisa;
#   2. traz a versão nova do GitHub;
#   3. instala o que a versão nova passou a precisar do sistema;
#   4. compila SEM parar o serviço, para o tempo fora do ar ser só o restart;
#   5. reaplica as unidades do systemd, a pasta de trabalho e o firewall;
#   6. troca o binário e reinicia;
#   7. confere, e volta sozinho para a versão anterior se a nova não subir.
#
# O passo 2 e o passo 5 são o que faltava. Sem o 2, o botão recompilava exatamente o mesmo
# código e anunciava sucesso sem ter mudado nada. Sem o 5, uma versão que precisasse de uma
# unidade nova subia pela metade — e a saída era sempre "rode o instalador de novo".
#
# Uso, de dentro da pasta do código na VPS:
#     sudo ./scripts/atualizar.sh
#
# Se você compila no seu computador e envia o binário pronto:
#     sudo ./scripts/atualizar.sh --binario ~/vodmanager-linux
#
set -euo pipefail

DESTINO=/opt/vodmanager/vodmanager
AMBIENTE=/etc/vodmanager.env
SERVICO=vodmanager
PASTA_BACKUP=/var/backups/vodmanager
FONTE="${VODM_FONTE:-/opt/vodmanager-fonte}"
REPO="${VODM_REPO:-https://github.com/gacaciomg-source/VOD-MANEGER.git}"
BINARIO_PRONTO=""
PULAR_BACKUP=0
PULAR_GIT=0

while [ $# -gt 0 ]; do
    case "$1" in
        --binario)    BINARIO_PRONTO="$2"; PULAR_GIT=1; shift 2 ;;
        --sem-backup) PULAR_BACKUP=1; shift ;;
        --sem-git)    PULAR_GIT=1; shift ;;
        *) echo "opção desconhecida: $1" >&2; exit 2 ;;
    esac
done

if [ "$(id -u)" -ne 0 ]; then
    echo "erro: rode com sudo." >&2
    exit 1
fi
if [ ! -f "$AMBIENTE" ]; then
    echo "erro: $AMBIENTE não existe. Este script é para uma instalação feita pelo instalador." >&2
    exit 1
fi

# O sudo reescreve o PATH por segurança (secure_path) e descarta /usr/local/go/bin — que é
# onde o Go instalado pelo tarball oficial fica. Sem isto, o script anuncia que o Go não
# está instalado numa máquina onde ele está.
export PATH="$PATH:/usr/local/go/bin"

# ---------------------------------------------------------------------------
# As três fases, e por que elas existem
#
# O bash NÃO lê o script inteiro antes de executar: ele lê conforme avança. Puxar a versão
# nova do GitHub reescreve, no disco, o arquivo que está sendo lido neste instante — e o
# bash continua a leitura no byte em que parou, agora dentro de um conteúdo diferente. O
# sintoma é um erro de sintaxe numa linha que ninguém escreveu.
#
#   fase 0  cópia de si mesmo para /tmp e reexecução de lá — o `git reset` da fase 1 já
#           não alcança o arquivo em execução;
#   fase 1  backup e atualização do código; ao terminar, executa a versão RECÉM-BAIXADA
#           deste mesmo script (também a partir de /tmp);
#   fase 2  compila, reaplica o sistema, troca o binário e confere.
#
# A fase 1 passar o bastão para a fase 2 é o que faz o atualizador se atualizar: quem
# instala a versão nova é a lógica da versão nova, e não a antiga.
# ---------------------------------------------------------------------------
FASE="${VODM_FASE:-0}"

if [ "$FASE" = "0" ]; then
    COPIA=$(mktemp /tmp/vodm-atualizar-a.XXXXXX)
    cp "$0" "$COPIA"
    VODM_FASE=1 exec /bin/bash "$COPIA" "$@"
fi

passo() { printf '\n\033[1m==> %s\033[0m\n' "$1"; }

REGISTRO=/opt/vodmanager/runtime/ultima-atualizacao.log

if [ "$FASE" = "1" ]; then
    # Registro em arquivo, além da saída padrão.
    #
    # Quando a atualização vem do painel, ninguém está olhando um terminal — e o serviço
    # reinicia no meio, derrubando qualquer conexão que estivesse acompanhando. O arquivo é
    # o único lugar onde o relato sobrevive ao próprio reinício, e é dele que o painel lê.
    #
    # O redirecionamento é montado UMA vez, aqui. A fase 2 herda estes descritores ao ser
    # executada, então o relato continua no mesmo arquivo sem precisar reabri-lo.
    mkdir -p /opt/vodmanager/runtime
    # O dono é o serviço, não o root: é dele que partem os pedidos gravados aqui.
    chown vodmanager:vodmanager /opt/vodmanager/runtime 2>/dev/null || true
    exec > >(tee "$REGISTRO") 2>&1
    chown vodmanager:vodmanager "$REGISTRO" 2>/dev/null || true

    printf '\033[1mAtualização iniciada em %s\033[0m\n' "$(date '+%F %H:%M:%S')"

    # -----------------------------------------------------------------------
    passo "1/7  Backup dos dados"

    if [ "$PULAR_BACKUP" = "1" ]; then
        echo "    pulado a pedido (--sem-backup)"
    elif [ ! -x "$DESTINO" ]; then
        echo "    não há instalação anterior; nada a salvar"
    else
        mkdir -p "$PASTA_BACKUP"
        ARQUIVO="$PASTA_BACKUP/antes-da-atualizacao-$(date +%F-%H%M).tar.gz"
        # As variáveis ficam num subshell: não vazam para o resto do script nem para os
        # comandos seguintes, que não têm por que enxergar a chave de criptografia.
        (set -a; . "$AMBIENTE"; set +a; "$DESTINO" backup --arquivo "$ARQUIVO" >/dev/null)
        echo "    $ARQUIVO"
    fi

    # -----------------------------------------------------------------------
    passo "2/7  Buscando a versão nova"

    if [ "$PULAR_GIT" = "1" ]; then
        echo "    pulado (--sem-git ou --binario)"
    else
        command -v git >/dev/null || {
            export DEBIAN_FRONTEND=noninteractive
            apt-get update -qq && apt-get install -y -qq git >/dev/null
        }
        # shellcheck source=scripts/lib/servicos.sh
        . "$FONTE/scripts/lib/servicos.sh" 2>/dev/null || . "$(dirname "$0")/lib/servicos.sh"

        ANTES=$(git -C "$FONTE" rev-parse --short HEAD 2>/dev/null || echo "?")
        vodm_atualizar_fonte "$FONTE" "$REPO"
        AGORA=$(git -C "$FONTE" rev-parse --short HEAD 2>/dev/null || echo "?")

        if [ "$ANTES" = "$AGORA" ]; then
            echo "    já estava na versão mais recente ($AGORA)"
        else
            echo "    $ANTES -> $AGORA"
            git -C "$FONTE" --no-pager log --oneline "$ANTES..$AGORA" 2>/dev/null \
                | sed 's/^/      . /' || true
        fi
    fi

    # A partir daqui quem manda é a versão nova deste script.
    SEGUINTE=$(mktemp /tmp/vodm-atualizar-b.XXXXXX)
    cp "$FONTE/scripts/atualizar.sh" "$SEGUINTE" 2>/dev/null || cp "$0" "$SEGUINTE"
    VODM_FASE=2 exec /bin/bash "$SEGUINTE" "$@"
fi

# ---------------------------------------------------------------------------
# Fase 2 — daqui para baixo já é a versão nova do script.
# ---------------------------------------------------------------------------

# shellcheck source=scripts/lib/servicos.sh
. "$FONTE/scripts/lib/servicos.sh"

NOVO=""
limpar() {
    rm -f /tmp/vodm-atualizar-a.?????? /tmp/vodm-atualizar-b.?????? 2>/dev/null || true
    [ -n "$NOVO" ] && rm -f "$NOVO" 2>/dev/null
    return 0
}
trap limpar EXIT

# ---------------------------------------------------------------------------
passo "3/7  Dependências do sistema"

if [ -n "$BINARIO_PRONTO" ]; then
    echo "    não é preciso: o binário veio pronto"
else
    vodm_pacotes postgresql postgresql-contrib git ufw curl ca-certificates
    echo "    em dia"

    # O Go é reinstalado quando falta ou está velho demais para compilar esta versão. Uma
    # versão nova do projeto pode exigir um Go novo, e descobrir isso como um erro de
    # compilação no meio do registro não ajuda ninguém.
    VERSAO_GO=1.25.0
    precisa_go=1
    if command -v go >/dev/null; then
        atual=$(go version | grep -oP 'go\K[0-9]+\.[0-9]+' || echo 0)
        maior=$(printf '%s\n1.25\n' "$atual" | sort -V | tail -1)
        [ "$maior" = "$atual" ] && precisa_go=0
    fi
    if [ "$precisa_go" -eq 1 ]; then
        echo "    instalando Go $VERSAO_GO"
        rm -rf /usr/local/go
        curl -fsSL "https://go.dev/dl/go${VERSAO_GO}.linux-amd64.tar.gz" | tar -C /usr/local -xz
        echo 'export PATH=$PATH:/usr/local/go/bin' > /etc/profile.d/go.sh
        export PATH="$PATH:/usr/local/go/bin"
    fi
    echo "    $(go version)"
fi

# ---------------------------------------------------------------------------
passo "4/7  Compilando a versão nova"

NOVO=$(mktemp /tmp/vodmanager-novo.XXXXXX)

if [ -n "$BINARIO_PRONTO" ]; then
    [ -f "$BINARIO_PRONTO" ] || { echo "erro: $BINARIO_PRONTO não existe" >&2; exit 1; }
    cp "$BINARIO_PRONTO" "$NOVO"
    echo "    usando o binário enviado: $BINARIO_PRONTO"
else
    # A compilação acontece na pasta do código, e não no diretório em que o script foi
    # chamado. Foi assim que o botão do painel falhava: o systemd executa a partir de /,
    # onde não existe go.mod, e o script parava com "rode de dentro da pasta do código".
    cd "$FONTE" || { echo "erro: a pasta do código ($FONTE) não existe." >&2; exit 1; }
    [ -f go.mod ] || { echo "erro: $FONTE não contém o projeto (go.mod ausente)." >&2; exit 1; }

    command -v go >/dev/null || {
        echo "erro: o Go não foi encontrado no PATH nem em /usr/local/go/bin." >&2
        echo "      Ou compile no seu computador e use: sudo $0 --binario ARQUIVO" >&2
        exit 1
    }
    VERSAO=$(git describe --tags --always 2>/dev/null || echo "dev")
    echo "    versão $VERSAO (o serviço continua no ar durante a compilação)"
    go build -trimpath -ldflags "-s -w -X main.version=$VERSAO" -o "$NOVO" ./cmd/vodmanager
fi

chmod 0755 "$NOVO"
# Um binário que não roda nesta máquina precisa ser descoberto AGORA, e não depois de já
# termos substituído o que funcionava.
"$NOVO" version >/dev/null || { echo "erro: o binário novo não executa aqui." >&2; exit 1; }

# ---------------------------------------------------------------------------
passo "5/7  Reaplicando o sistema"

# É o passo que dispensa rodar o instalador de novo. As unidades, a pasta de trabalho e o
# firewall são reescritos a partir da versão NOVA do código — então um recurso que precise
# de uma unidade nova passa a funcionar por este caminho, sem terminal nenhum.
vodm_pasta_runtime
vodm_unidades
PORTA=$(grep -oP 'VODM_HTTP_ADDR=.*:\K[0-9]+' "$AMBIENTE" 2>/dev/null || echo 8080)
vodm_firewall "$PORTA"
echo "    unidades, pasta de trabalho e firewall em dia"

# ---------------------------------------------------------------------------
passo "6/7  Trocando o binário"

ANTERIOR=""
if [ -x "$DESTINO" ]; then
    ANTERIOR="$DESTINO.anterior"
    cp -p "$DESTINO" "$ANTERIOR"
    echo "    versão atual guardada em $ANTERIOR"
fi

systemctl stop "$SERVICO" || true
install -o root -g root -m 0755 "$NOVO" "$DESTINO"
systemctl start "$SERVICO"

# ---------------------------------------------------------------------------
passo "7/7  Conferindo se subiu"

OK=0
for _ in $(seq 1 30); do
    if curl -fsS "http://127.0.0.1:$PORTA/healthz" >/dev/null 2>&1; then OK=1; break; fi
    sleep 1
done

if [ "$OK" = "1" ]; then
    echo "    respondendo em http://127.0.0.1:$PORTA"
    printf '\n\033[32mAtualização concluída.\033[0m  Versão: %s\n\n' "$("$DESTINO" version)"
    exit 0
fi

# A versão nova não subiu. Voltar é melhor que deixar o serviço fora do ar enquanto alguém
# investiga.
printf '\n\033[31mA versão nova não respondeu em 30s.\033[0m\n'
if [ -n "$ANTERIOR" ]; then
    echo "Voltando para a versão anterior..."
    systemctl stop "$SERVICO" || true
    install -o root -g root -m 0755 "$ANTERIOR" "$DESTINO"
    systemctl start "$SERVICO"
    echo "Voltou. O serviço está com a versão de antes."
fi
echo
echo "Veja o motivo em:  sudo journalctl -u $SERVICO -n 50 --no-pager"
exit 1
