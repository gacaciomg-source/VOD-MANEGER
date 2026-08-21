#!/bin/bash
#
# Configura domínio e HTTPS para o VOD Manager.
#
# Disparado pelo painel (que apenas cria um arquivo de pedido) ou à mão:
#     sudo ./scripts/dominio.sh vod.seudominio.com voce@email.com
#
# # A decisão que governa este script
#
# A porta 8080 NUNCA é fechada, e o VODM_HTTP_ADDR nunca é alterado.
#
# O motivo é concreto: os links já entregues aos clientes contêm o IP e a porta. Fechá-la
# ao ligar o domínio derrubaria todo mundo de uma vez, e cada cliente só voltaria depois de
# receber um link novo. Com os dois caminhos vivos, a migração é gradual e ninguém cai.
#
# Fechar a 8080 é uma decisão posterior, tomada quando o administrador vir em Reproduções
# que ninguém mais usa o IP.
#
# # E se der errado
#
# O painel é a ferramenta que o administrador usaria para consertar — então quebrá-lo seria
# o pior resultado possível. Por isso: a configuração anterior é guardada, a nova é validada
# antes de entrar, e se o painel parar de responder o script volta atrás sozinho.
set -euo pipefail

DOMINIO="${1:-}"
EMAIL="${2:-}"
AMBIENTE=/etc/vodmanager.env
SITE=/etc/nginx/sites-available/vodmanager
LINK=/etc/nginx/sites-enabled/vodmanager
REGISTRO=/opt/vodmanager/ultimo-dominio.log

passo()   { printf '\n\033[1;36m==> %s\033[0m\n' "$*"; }
ok()      { printf '\033[32m    %s\033[0m\n' "$*"; }
aviso()   { printf '\033[33m    %s\033[0m\n' "$*"; }
erro()    { printf '\n\033[31merro: %s\033[0m\n' "$*" >&2; }

# A checagem de root vem ANTES do registro em arquivo: sem ela, uma execução sem sudo
# falha no mkdir com "permission denied", que não diz o que fazer.
[ "$(id -u)" -eq 0 ] || { erro "rode com sudo."; exit 1; }

mkdir -p /opt/vodmanager
exec > >(tee "$REGISTRO") 2>&1
chown vodmanager:vodmanager "$REGISTRO" 2>/dev/null || true
[ -n "$DOMINIO" ] || { erro "informe o domínio."; exit 2; }

# Um domínio malformado produziria uma configuração que o nginx recusa, ou pior, uma que
# ele aceita e que não corresponde a nada.
if ! printf '%s' "$DOMINIO" | grep -qE '^[a-zA-Z0-9]([a-zA-Z0-9-]*[a-zA-Z0-9])?(\.[a-zA-Z0-9]([a-zA-Z0-9-]*[a-zA-Z0-9])?)+$'; then
    erro "'$DOMINIO' não parece um domínio válido."
    exit 2
fi

PORTA=$(grep -oP 'VODM_HTTP_ADDR=.*:\K[0-9]+' "$AMBIENTE" 2>/dev/null || echo 8080)

# ---------------------------------------------------------------------------
passo "1/6  Conferindo se o domínio aponta para esta máquina"

MEU_IP=$(curl -fsS --max-time 8 https://api.ipify.org 2>/dev/null || hostname -I | awk '{print $1}')
IP_DOMINIO=$(getent ahostsv4 "$DOMINIO" 2>/dev/null | awk '{print $1; exit}' || echo "")

if [ -z "$IP_DOMINIO" ]; then
    erro "o domínio $DOMINIO não resolve para nenhum endereço.
       Crie um registro A apontando para $MEU_IP e aguarde a propagação."
    exit 1
fi
if [ "$IP_DOMINIO" != "$MEU_IP" ]; then
    erro "o domínio $DOMINIO aponta para $IP_DOMINIO, e esta máquina é $MEU_IP.
       O certificado não seria emitido: a autoridade certificadora precisa alcançar
       ESTA máquina pelo domínio. Corrija o registro A e tente de novo."
    exit 1
fi
ok "$DOMINIO → $MEU_IP"

# ---------------------------------------------------------------------------
passo "2/6  Instalando nginx e certbot, se faltarem"

export DEBIAN_FRONTEND=noninteractive
if ! command -v nginx >/dev/null || ! command -v certbot >/dev/null; then
    apt-get update -qq
    apt-get install -y -qq nginx certbot python3-certbot-nginx >/dev/null
fi
ok "$(nginx -v 2>&1) · certbot presente"

# ---------------------------------------------------------------------------
passo "3/6  Guardando a configuração atual"

ANTERIOR=""
if [ -f "$SITE" ]; then
    ANTERIOR="${SITE}.anterior"
    cp -p "$SITE" "$ANTERIOR"
    ok "$ANTERIOR"
else
    ok "não havia configuração anterior"
fi

# ---------------------------------------------------------------------------
passo "4/6  Escrevendo a configuração"

cat > "$SITE" <<NGINX
# Gerado pelo VOD Manager. Alterações à mão são substituídas na próxima execução.
server {
    listen 80;
    server_name ${DOMINIO};

    location / {
        proxy_pass http://127.0.0.1:${PORTA};
        proxy_http_version 1.1;
        proxy_set_header Host              \$host;
        proxy_set_header X-Real-IP         \$remote_addr;
        proxy_set_header X-Forwarded-For   \$proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto \$scheme;

        # Vídeo é resposta longa. Com buffering ligado o nginx tenta acumular o filme
        # inteiro antes de repassar, e sem os tempos abaixo ele corta a transmissão no
        # meio — os dois sintomas mais difíceis de diagnosticar depois.
        proxy_buffering off;
        proxy_request_buffering off;
        proxy_read_timeout 1h;
        proxy_send_timeout 1h;
        client_max_body_size 0;
    }
}
NGINX

ln -sf "$SITE" "$LINK"

if ! nginx -t 2>/dev/null; then
    erro "a configuração gerada foi recusada pelo nginx. Nada foi aplicado."
    nginx -t || true
    if [ -n "$ANTERIOR" ]; then cp -p "$ANTERIOR" "$SITE"; else rm -f "$SITE" "$LINK"; fi
    exit 1
fi
systemctl reload nginx
ok "nginx recarregado"

# ---------------------------------------------------------------------------
passo "5/6  Emitindo o certificado"

if [ -z "$EMAIL" ]; then
    aviso "sem e-mail informado; emitindo sem aviso de expiração"
    REGISTRO_EMAIL="--register-unsafely-without-email"
else
    REGISTRO_EMAIL="--email $EMAIL"
fi

# --nginx ajusta o próprio site para 443 e redireciona o 80.
if certbot --nginx -d "$DOMINIO" --non-interactive --agree-tos $REGISTRO_EMAIL --redirect >/dev/null 2>&1; then
    ok "HTTPS ativo em https://${DOMINIO}"
    ESQUEMA=https
else
    aviso "não foi possível emitir o certificado; o domínio segue em HTTP"
    aviso "veja o motivo em /var/log/letsencrypt/letsencrypt.log"
    ESQUEMA=http
fi

# ---------------------------------------------------------------------------
passo "6/6  Conferindo que o painel responde pelo domínio"

ufw allow 'Nginx Full' >/dev/null 2>&1 || true

OK=0
for _ in $(seq 1 15); do
    if curl -fsS --max-time 5 "${ESQUEMA}://${DOMINIO}/healthz" >/dev/null 2>&1; then OK=1; break; fi
    sleep 2
done

if [ "$OK" -ne 1 ]; then
    erro "o painel não respondeu por ${ESQUEMA}://${DOMINIO}. Voltando atrás."
    if [ -n "$ANTERIOR" ]; then
        cp -p "$ANTERIOR" "$SITE"
    else
        rm -f "$SITE" "$LINK"
    fi
    nginx -t >/dev/null 2>&1 && systemctl reload nginx
    erro "a configuração anterior foi restaurada. O acesso pelo IP continua funcionando."
    exit 1
fi

cat <<FIM

$(ok "Domínio configurado.")

  Painel e conteúdo ..... ${ESQUEMA}://${DOMINIO}
  Acesso pelo IP ........ continua funcionando na porta ${PORTA}

A porta ${PORTA} foi deixada ABERTA de propósito: os links que os seus clientes já têm
apontam para o IP, e fechá-la agora derrubaria todos de uma vez.

Migre os clientes aos poucos — em Credenciais > Lista o endereço já sai com o domínio.
Quando ninguém mais aparecer com o IP em Reproduções, aí sim vale fechar:

    sudo ufw delete allow ${PORTA}/tcp

FIM
