#!/bin/bash
#
# Instalador do VOD Manager para Ubuntu 22.04/24.04 e Debian 12.
#
# Faz tudo o que o guia manual faz: pacotes, banco, usuário de sistema, compilação,
# chave de criptografia, serviço systemd, firewall e endereço público.
#
#   sudo bash instalar.sh
#
# É SEGURO RODAR DE NOVO. Reinstalar não apaga dados e, principalmente, não regenera a
# chave de criptografia — regenerá-la tornaria ilegíveis as credenciais das fontes e as
# senhas dos clientes, que é o único estrago sem conserto desta instalação.
#
set -euo pipefail

REPO="${VODM_REPO:-https://github.com/gacaciomg-source/VOD-MANEGER.git}"
FONTE="${VODM_FONTE:-/opt/vodmanager-fonte}"
DESTINO=/opt/vodmanager/vodmanager
AMBIENTE=/etc/vodmanager.env
SERVICO=vodmanager
PORTA="${VODM_PORTA:-8080}"
VERSAO_GO=1.25.0

vermelho() { printf '\033[31m%s\033[0m\n' "$*"; }
verde()    { printf '\033[32m%s\033[0m\n' "$*"; }
passo()    { printf '\n\033[1;36m==> %s\033[0m\n' "$*"; }
aviso()    { printf '\033[33m    %s\033[0m\n' "$*"; }

erro() { vermelho "erro: $*"; exit 1; }

[ "$(id -u)" -eq 0 ] || erro "rode com sudo:  sudo bash instalar.sh"
command -v apt-get >/dev/null || erro "este instalador é para Ubuntu ou Debian."

printf '\n\033[1m  VOD Manager — instalação\033[0m\n'

# ---------------------------------------------------------------------------
passo "1/9  Pacotes do sistema"

export DEBIAN_FRONTEND=noninteractive
apt-get update -qq
apt-get install -y -qq postgresql postgresql-contrib git ufw curl ca-certificates >/dev/null
verde "    postgresql, git, ufw, curl"

# ---------------------------------------------------------------------------
passo "2/9  Go"

instalar_go=1
if command -v go >/dev/null; then
    atual=$(go version | grep -oP 'go\K[0-9]+\.[0-9]+' || echo 0)
    maior=$(printf '%s\n1.25\n' "$atual" | sort -V | tail -1)
    [ "$maior" = "$atual" ] && instalar_go=0
fi

if [ "$instalar_go" -eq 1 ]; then
    echo "    baixando Go $VERSAO_GO..."
    rm -rf /usr/local/go
    curl -fsSL "https://go.dev/dl/go${VERSAO_GO}.linux-amd64.tar.gz" | tar -C /usr/local -xz
    echo 'export PATH=$PATH:/usr/local/go/bin' > /etc/profile.d/go.sh
fi
export PATH=$PATH:/usr/local/go/bin
command -v go >/dev/null || erro "o Go não ficou disponível no PATH."
verde "    $(go version)"

# ---------------------------------------------------------------------------
passo "3/9  Banco de dados"

systemctl enable --now postgresql >/dev/null 2>&1 || true

existe_papel=$(sudo -u postgres psql -tAc \
    "SELECT 1 FROM pg_roles WHERE rolname='vodmanager'" 2>/dev/null || echo "")

# A senha do banco é gerada em hexadecimal por um motivo prático: ela viaja dentro de uma
# URL de conexão, e caracteres como / e @ quebrariam o endereço de um jeito que só
# apareceria depois, como erro incompreensível.
SENHA_BANCO=$(openssl rand -hex 24)

if [ -n "$existe_papel" ]; then
    # Já existe: trocar a senha é seguro e mantém os dados. Recriar apagaria tudo.
    sudo -u postgres psql -qc "ALTER ROLE vodmanager PASSWORD '$SENHA_BANCO';" >/dev/null
    aviso "papel vodmanager já existia; senha renovada, dados preservados"
else
    sudo -u postgres psql -qc "CREATE ROLE vodmanager LOGIN PASSWORD '$SENHA_BANCO';" >/dev/null
fi

existe_banco=$(sudo -u postgres psql -tAc \
    "SELECT 1 FROM pg_database WHERE datname='vodmanager'" 2>/dev/null || echo "")
if [ -z "$existe_banco" ]; then
    sudo -u postgres psql -qc "CREATE DATABASE vodmanager OWNER vodmanager;" >/dev/null
    verde "    banco vodmanager criado"
else
    verde "    banco vodmanager já existe (dados preservados)"
fi

# ---------------------------------------------------------------------------
passo "4/9  Usuário do sistema"

id -u vodmanager >/dev/null 2>&1 || \
    useradd --system --home /opt/vodmanager --shell /usr/sbin/nologin vodmanager
mkdir -p /opt/vodmanager
verde "    vodmanager (sem shell, sem login)"

# ---------------------------------------------------------------------------
passo "5/9  Código e compilação"

if [ -d "$FONTE/.git" ]; then
    git -C "$FONTE" fetch --quiet --all
    git -C "$FONTE" reset --quiet --hard origin/HEAD 2>/dev/null || git -C "$FONTE" pull --quiet
else
    rm -rf "$FONTE"
    git clone --quiet "$REPO" "$FONTE"
fi

[ -f "$FONTE/go.mod" ] || erro "o repositório não contém o projeto (go.mod ausente em $FONTE).
       Confira se o código foi enviado ao GitHub: $REPO"

echo "    compilando (a primeira vez demora alguns minutos)..."
cd "$FONTE"
VERSAO=$(git describe --tags --always 2>/dev/null || echo "instalado")
go build -trimpath -ldflags "-s -w -X main.version=$VERSAO" -o /tmp/vodmanager-novo ./cmd/vodmanager
install -o vodmanager -g vodmanager -m 0755 /tmp/vodmanager-novo "$DESTINO"
rm -f /tmp/vodmanager-novo
verde "    versão $("$DESTINO" version)"

# ---------------------------------------------------------------------------
passo "6/9  Chave de criptografia e configuração"

CHAVE=""
SENHA_PAINEL=""
REINSTALACAO=0

if [ -f "$AMBIENTE" ]; then
    REINSTALACAO=1
    # A chave existente é preservada SEMPRE. Gerar uma nova aqui tornaria ilegíveis as
    # credenciais das fontes e as senhas dos clientes já cadastrados — dano permanente,
    # e silencioso até alguém tentar assistir.
    CHAVE=$(grep -oP '^VODM_ENCRYPTION_KEY=\K.*' "$AMBIENTE" 2>/dev/null || echo "")
    [ -n "$CHAVE" ] && aviso "chave de criptografia existente preservada"
fi

if [ -z "$CHAVE" ]; then
    CHAVE=$(openssl rand -base64 32)
fi

if [ "$REINSTALACAO" -eq 0 ]; then
    echo
    echo "    Escolha a senha do painel (mínimo 12 caracteres)."
    echo "    Deixe em branco para o sistema gerar uma."
    while :; do
        read -rsp "    Senha: " SENHA_PAINEL; echo
        [ -z "$SENHA_PAINEL" ] && break
        if [ "${#SENHA_PAINEL}" -lt 12 ]; then
            vermelho "    curta demais (${#SENHA_PAINEL} caracteres, mínimo 12)"
            continue
        fi
        read -rsp "    Repita:  " confirma; echo
        [ "$SENHA_PAINEL" = "$confirma" ] && break
        vermelho "    as senhas não conferem"
    done
    [ -z "$SENHA_PAINEL" ] && SENHA_PAINEL=$(openssl rand -base64 18 | tr -d '/+=' | cut -c1-20)
fi

umask 077
{
    echo "VODM_DATABASE_URL=postgres://vodmanager:${SENHA_BANCO}@localhost:5432/vodmanager?sslmode=disable"
    echo "VODM_ENCRYPTION_KEY=${CHAVE}"
    echo "VODM_HTTP_ADDR=:${PORTA}"
    echo "VODM_ROLE=all"
    echo "VODM_LOG_FORMAT=json"
    if [ "$REINSTALACAO" -eq 0 ]; then
        echo "VODM_BOOTSTRAP_ADMIN_USERNAME=admin"
        echo "VODM_BOOTSTRAP_ADMIN_PASSWORD=${SENHA_PAINEL}"
    fi
} > "$AMBIENTE"
chown root:vodmanager "$AMBIENTE"
chmod 640 "$AMBIENTE"
verde "    $AMBIENTE"

# ---------------------------------------------------------------------------
passo "7/9  Serviço"

cat > /etc/systemd/system/${SERVICO}.service <<UNIT
[Unit]
Description=VOD Manager
After=network-online.target postgresql.service
Wants=network-online.target

[Service]
Type=simple
User=vodmanager
Group=vodmanager
EnvironmentFile=${AMBIENTE}
ExecStart=${DESTINO}
Restart=always
RestartSec=5

NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=strict
ProtectHome=true
ReadWritePaths=/opt/vodmanager

# Streaming abre muitos descritores ao mesmo tempo; sem isto o serviço trava com
# "Too many open files" justamente quando houver audiência.
LimitNOFILE=65535

[Install]
WantedBy=multi-user.target
UNIT

systemctl daemon-reload
systemctl enable --quiet "$SERVICO"
systemctl restart "$SERVICO"
verde "    habilitado e iniciado"

# Botão "Atualizar" do painel.
#
# O serviço roda como o usuário vodmanager, que não pode reiniciar serviços nem escrever
# fora de /opt/vodmanager — e roda com NoNewPrivileges, que impede o sudo de elevar.
# Ambas as restrições são boas e ficam.
#
# Em vez de abrir uma exceção nelas, invertemos o sentido: o painel apenas CRIA UM ARQUIVO
# dentro da pasta onde já pode escrever, e o systemd — que é root — observa esse arquivo e
# dispara a atualização.
#
# O ganho não é só conveniência. O serviço nunca ganha privilégio nenhum; ele não escolhe o
# que será executado, só pede que a atualização aconteça. Mesmo que o processo fosse
# comprometido, o máximo que conseguiria é disparar o script que o root já definiu.
cat > /etc/systemd/system/vodmanager-update.service <<UNIT
[Unit]
Description=Atualização do VOD Manager (disparada pelo painel)

[Service]
Type=oneshot
# O pedido é consumido ANTES de começar: se o script falhar, o arquivo já não existe e a
# atualização não fica repetindo em laço.
ExecStartPre=/bin/rm -f /opt/vodmanager/solicitar-atualizacao
ExecStart=${FONTE}/scripts/atualizar.sh
UNIT

cat > /etc/systemd/system/vodmanager-update.path <<UNIT
[Unit]
Description=Observa o pedido de atualização vindo do painel

[Path]
PathExists=/opt/vodmanager/solicitar-atualizacao

[Install]
WantedBy=multi-user.target
UNIT

# Configuração de domínio pelo painel, pelo mesmo mecanismo da atualização: o painel só
# escreve um pedido, e o systemd — que é root — executa. O serviço nunca ganha privilégio.
cat > /etc/systemd/system/vodmanager-domain.service <<UNIT
[Unit]
Description=Configuração de domínio do VOD Manager (disparada pelo painel)

[Service]
Type=oneshot
# O pedido carrega o domínio e o e-mail, e é consumido ANTES de começar: se o script
# falhar, o arquivo já não existe e a tarefa não fica repetindo em laço.
ExecStart=/bin/bash -c 'read -r d e < /opt/vodmanager/solicitar-dominio; rm -f /opt/vodmanager/solicitar-dominio; ${FONTE}/scripts/dominio.sh "$d" "$e"'
UNIT

cat > /etc/systemd/system/vodmanager-domain.path <<UNIT
[Unit]
Description=Observa o pedido de domínio vindo do painel

[Path]
PathExists=/opt/vodmanager/solicitar-dominio

[Install]
WantedBy=multi-user.target
UNIT

rm -f /etc/sudoers.d/vodmanager   # de versões anteriores deste instalador
systemctl daemon-reload
systemctl enable --now --quiet vodmanager-update.path
systemctl enable --now --quiet vodmanager-domain.path
verde "    botões Atualizar e Domínio liberados no painel"

# ---------------------------------------------------------------------------
passo "8/9  Firewall"

ufw allow OpenSSH >/dev/null 2>&1 || true
ufw allow "${PORTA}/tcp" >/dev/null 2>&1 || true
ufw --force enable >/dev/null 2>&1 || true
verde "    portas ${PORTA} e SSH liberadas"

# ---------------------------------------------------------------------------
passo "9/9  Verificação e endereço público"

OK=0
for _ in $(seq 1 40); do
    if curl -fsS "http://127.0.0.1:${PORTA}/healthz" >/dev/null 2>&1; then OK=1; break; fi
    sleep 1
done

if [ "$OK" -ne 1 ]; then
    echo
    vermelho "O serviço não respondeu em 40 segundos."
    echo "Veja o motivo:  journalctl -u ${SERVICO} -n 40 --no-pager"
    exit 1
fi

# Descobrir o IP público e já gravá-lo como endereço público.
#
# É o erro nº 1 de quem instala: sem isso, os links de reprodução saem com o endereço
# interno e não funcionam no XC_VM nem em nenhuma outra máquina — e o sintoma (o vídeo
# não abre) não sugere a causa.
IP=$(curl -fsS --max-time 5 https://api.ipify.org 2>/dev/null \
     || curl -fsS --max-time 5 https://ifconfig.me 2>/dev/null \
     || hostname -I | awk '{print $1}')
ENDERECO="http://${IP}:${PORTA}"

sudo -u postgres psql -d vodmanager -qc \
    "INSERT INTO settings (key, value) VALUES ('public_base_url', to_jsonb('${ENDERECO}'::text))
     ON CONFLICT (key) DO UPDATE SET value = excluded.value, updated_at = now();" >/dev/null 2>&1 \
    && verde "    endereço público: ${ENDERECO}" \
    || aviso "não foi possível gravar o endereço público; ajuste em Configurações"

# ---------------------------------------------------------------------------
cat <<FIM

$(verde "  Instalação concluída.")

  Painel .......... ${ENDERECO}
  Usuário ......... admin
FIM

if [ "$REINSTALACAO" -eq 0 ]; then
    echo "  Senha ........... ${SENHA_PAINEL}"
fi

cat <<FIM

  ┌────────────────────────────────────────────────────────────────────┐
  │  GUARDE A CHAVE DE CRIPTOGRAFIA FORA DESTA MÁQUINA, AGORA.         │
  │                                                                    │
  │  ${CHAVE}  │
  │                                                                    │
  │  Ela protege as credenciais das suas fontes e as senhas dos seus   │
  │  clientes. Perdê-la torna esses dados irrecuperáveis, mesmo com    │
  │  backup — é a única coisa aqui que não tem conserto.               │
  └────────────────────────────────────────────────────────────────────┘

  Comandos úteis:
    systemctl status ${SERVICO}
    journalctl -u ${SERVICO} -f
    /opt/vodmanager/vodmanager backup     (rode com as variáveis do ambiente)

FIM

if [ "$REINSTALACAO" -eq 0 ]; then
    aviso "Depois de entrar no painel, troque a senha em Configurações e apague as"
    aviso "linhas BOOTSTRAP_ de ${AMBIENTE}."
fi

if ! curl -fsS --max-time 5 "${ENDERECO}/healthz" >/dev/null 2>&1; then
    echo
    aviso "O painel não respondeu pelo endereço público."
    aviso "Se o seu provedor (Hostinger, etc.) tem firewall próprio no painel dele,"
    aviso "libere a porta ${PORTA} lá também — o ufw sozinho não basta."
fi
