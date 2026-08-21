#!/bin/bash
#
# Atualiza o VOD Manager instalado nesta máquina.
#
# Faz na ordem certa o que dá errado quando feito na ordem errada:
#
#   1. backup ANTES de tocar em qualquer coisa — se a versão nova tiver um problema,
#      você não descobre isso já sem rede de proteção;
#   2. compila a versão nova SEM parar o serviço, para o tempo fora do ar ser só o
#      restart, e não o build inteiro;
#   3. guarda o binário atual, e volta para ele se a versão nova não subir.
#
# Uso, de dentro da pasta do código na VPS:
#     sudo ./scripts/atualizar.sh
#
# Se você compila no seu computador e envia o binário pronto, use:
#     sudo ./scripts/atualizar.sh --binario ~/vodmanager-linux
#
set -euo pipefail

DESTINO=/opt/vodmanager/vodmanager
AMBIENTE=/etc/vodmanager.env
SERVICO=vodmanager
PASTA_BACKUP=/var/backups/vodmanager
BINARIO_PRONTO=""

while [ $# -gt 0 ]; do
    case "$1" in
        --binario) BINARIO_PRONTO="$2"; shift 2 ;;
        --sem-backup) PULAR_BACKUP=1; shift ;;
        *) echo "opção desconhecida: $1" >&2; exit 2 ;;
    esac
done

if [ "$(id -u)" -ne 0 ]; then
    echo "erro: rode com sudo." >&2
    exit 1
fi
if [ ! -f "$AMBIENTE" ]; then
    echo "erro: $AMBIENTE não existe. Este script é para uma instalação feita pelo guia." >&2
    exit 1
fi

# O sudo reescreve o PATH por segurança (secure_path) e descarta /usr/local/go/bin — que
# é onde o Go instalado pelo tarball oficial fica. Sem isto, o script anuncia que o Go não
# está instalado numa máquina onde ele está.
export PATH="$PATH:/usr/local/go/bin"

passo() { printf '\n\033[1m==> %s\033[0m\n' "$1"; }

# Registro em arquivo, além da saída padrão.
#
# Quando a atualização é disparada pelo painel, ninguém está olhando um terminal — e o
# serviço reinicia no meio, derrubando qualquer conexão que estivesse acompanhando. O
# arquivo é o único lugar onde o relato sobrevive ao próprio reinício, e é dele que o
# painel lê para mostrar o que aconteceu.
REGISTRO=/opt/vodmanager/runtime/ultima-atualizacao.log
mkdir -p /opt/vodmanager/runtime
exec > >(tee "$REGISTRO") 2>&1
chown vodmanager:vodmanager "$REGISTRO" 2>/dev/null || true

# ---------------------------------------------------------------------------
passo "1/5  Backup dos dados"

if [ "${PULAR_BACKUP:-0}" = "1" ]; then
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

# ---------------------------------------------------------------------------
passo "2/5  Preparar o binário novo"

NOVO=$(mktemp /tmp/vodmanager-novo.XXXXXX)
trap 'rm -f "$NOVO"' EXIT

if [ -n "$BINARIO_PRONTO" ]; then
    [ -f "$BINARIO_PRONTO" ] || { echo "erro: $BINARIO_PRONTO não existe" >&2; exit 1; }
    cp "$BINARIO_PRONTO" "$NOVO"
    echo "    usando o binário enviado: $BINARIO_PRONTO"
else
    command -v go >/dev/null || {
        echo "erro: o Go não foi encontrado." >&2
        echo "      Procurei no PATH e em /usr/local/go/bin." >&2
        echo "      Instale com:" >&2
        echo "        curl -fsSL https://go.dev/dl/go1.25.0.linux-amd64.tar.gz | sudo tar -C /usr/local -xz" >&2
        echo "      Ou compile no seu computador e use: sudo $0 --binario ARQUIVO" >&2
        exit 1
    }
    [ -f go.mod ] || { echo "erro: rode de dentro da pasta do código." >&2; exit 1; }
    VERSAO=$(git describe --tags --always 2>/dev/null || echo "dev")
    echo "    compilando versão $VERSAO (o serviço continua no ar)"
    go build -trimpath -ldflags "-s -w -X main.version=$VERSAO" -o "$NOVO" ./cmd/vodmanager
fi

chmod 0755 "$NOVO"
# Um binário que não roda nesta máquina precisa ser descoberto AGORA, e não depois de
# já termos substituído o que funcionava.
"$NOVO" version >/dev/null || { echo "erro: o binário novo não executa aqui." >&2; exit 1; }

# ---------------------------------------------------------------------------
passo "3/5  Guardar a versão atual"

ANTERIOR=""
if [ -x "$DESTINO" ]; then
    ANTERIOR="$DESTINO.anterior"
    cp -p "$DESTINO" "$ANTERIOR"
    echo "    $ANTERIOR"
fi

# ---------------------------------------------------------------------------
passo "4/5  Trocar e reiniciar"

systemctl stop "$SERVICO" || true
install -o root -g root -m 0755 "$NOVO" "$DESTINO"
systemctl start "$SERVICO"

# ---------------------------------------------------------------------------
passo "5/5  Conferir se subiu"

PORTA=$(grep -oP 'VODM_HTTP_ADDR=.*:\K[0-9]+' "$AMBIENTE" 2>/dev/null || echo 8080)
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

# A versão nova não subiu. Voltar é melhor que deixar o serviço fora do ar enquanto
# alguém investiga.
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
