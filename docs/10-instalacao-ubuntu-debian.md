# VOD Manager — 10. Instalação em Ubuntu / Debian

> Guia para colocar o sistema em produção numa máquina Linux. Caminho pensado para
> Ubuntu 22.04/24.04 e Debian 12, usando apenas pacotes oficiais da distribuição.

---

## Recomendação: **sem Docker**

Docker é a resposta certa quando você precisa rodar várias versões conflitantes na mesma
máquina, ou reproduzir um ambiente complicado. Nenhum dos dois é o seu caso:

- O VOD Manager compila para **um binário único**, sem dependência externa em runtime.
  Não há Python, Node, nem bibliotecas do sistema. É um arquivo.
- A única dependência é o **PostgreSQL**, que o Ubuntu/Debian já empacota e mantém
  atualizado com correções de segurança automáticas.
- O caminho crítico aqui é **rede e disco**. Docker põe uma camada de rede virtual entre
  o cliente e o processo. Não é catastrófico, mas é custo sem contrapartida.
- Atualizar sem Docker é copiar um arquivo e reiniciar um serviço. Com Docker você passa
  a manter também um registry, tags de imagem e um `docker-compose.yml`.

**Use Docker se** você já administra outros serviços em Docker nessa máquina e quer tudo
no mesmo padrão operacional — aí a consistência vale mais que a simplicidade. Nesse caso
o `Dockerfile` já está listado como pendência da Fase 1 e eu escrevo quando você pedir.

**Sobre GitHub:** não é necessário para instalar. O código pode ir por `scp`, ou você
compila na sua máquina e envia só o binário. Um repositório privado é útil por histórico
e backup do código, não pela instalação.

---

## Passo a passo

### 1. Preparar a máquina

```bash
sudo apt update && sudo apt upgrade -y && sudo apt install -y postgresql postgresql-contrib golang-go git ufw curl
```

Confira a versão do Go — o projeto exige **1.25 ou superior**:

```bash
go version
```

Se o `apt` trouxe uma versão anterior (comum no Debian 12), instale a oficial:

```bash
sudo rm -rf /usr/local/go && curl -fsSL https://go.dev/dl/go1.25.0.linux-amd64.tar.gz | sudo tar -C /usr/local -xz && export PATH=$PATH:/usr/local/go/bin && go version
```

Para deixar permanente:

```bash
echo 'export PATH=$PATH:/usr/local/go/bin' | sudo tee /etc/profile.d/go.sh
```

### 2. Criar o banco de dados

Gere uma senha forte e **guarde** — você vai usá-la no passo 5:

```bash
openssl rand -base64 24
```

Crie o usuário e o banco (troque `COLE_A_SENHA_AQUI`):

```bash
sudo -u postgres psql -c "CREATE ROLE vodmanager LOGIN PASSWORD 'COLE_A_SENHA_AQUI';" -c "CREATE DATABASE vodmanager OWNER vodmanager;"
```

A extensão `pg_trgm` (usada na busca de títulos parecidos) é criada pelas próprias
migrations no primeiro boot — você não precisa fazer nada.

### 3. Criar o usuário do sistema

O serviço não roda como root. Se um dia houver uma falha de segurança no processo, ela
fica contida num usuário sem shell e sem permissão fora do próprio diretório.

```bash
sudo useradd --system --home /opt/vodmanager --shell /usr/sbin/nologin vodmanager && sudo mkdir -p /opt/vodmanager
```

### 4. Compilar e instalar o binário

Copie o código para a máquina (por `scp`, `git clone`, o que preferir) e, de dentro da
pasta do código:

```bash
go build -o vodmanager ./cmd/vodmanager && sudo install -o vodmanager -g vodmanager -m 0755 vodmanager /opt/vodmanager/vodmanager
```

O binário é autossuficiente: o painel web está **embutido dentro dele**. Não há pasta de
assets para copiar nem servidor de arquivos para configurar.

### 5. Configurar

Gere a chave de criptografia. É ela que protege as credenciais das suas fontes no banco:

```bash
openssl rand -base64 32
```

> **Guarde essa chave num lugar seguro, fora da máquina.** Se você a perder, as
> credenciais das fontes se tornam irrecuperáveis e precisam ser cadastradas de novo. Se
> alguém a obtiver junto com um dump do banco, tem suas credenciais.

Crie `/etc/vodmanager.env` com este conteúdo, substituindo os valores em maiúsculas:

```
VODM_DATABASE_URL=postgres://vodmanager:SENHA_DO_BANCO@localhost:5432/vodmanager?sslmode=disable
VODM_ENCRYPTION_KEY=CHAVE_GERADA_ACIMA
VODM_HTTP_ADDR=:8080
VODM_ROLE=all
VODM_BOOTSTRAP_ADMIN_USERNAME=admin
VODM_BOOTSTRAP_ADMIN_PASSWORD=uma-senha-longa-de-no-minimo-12-caracteres
VODM_LOG_FORMAT=json
```

E proteja o arquivo:

```bash
sudo chown root:vodmanager /etc/vodmanager.env && sudo chmod 640 /etc/vodmanager.env
```

O `chmod 640` importa: sem ele, qualquer usuário da máquina lê a chave de criptografia.

O administrador é criado **apenas no primeiro boot**, e só se ainda não existir nenhum.
Depois de entrar no painel e trocar a senha, remova as duas linhas `BOOTSTRAP_`.

### 6. Criar o serviço

Crie `/etc/systemd/system/vodmanager.service`:

```
[Unit]
Description=VOD Manager
After=network-online.target postgresql.service
Wants=network-online.target

[Service]
Type=simple
User=vodmanager
Group=vodmanager
EnvironmentFile=/etc/vodmanager.env
ExecStart=/opt/vodmanager/vodmanager
Restart=always
RestartSec=5

# Endurecimento: o processo não precisa de nada disso.
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=strict
ProtectHome=true
ReadWritePaths=/opt/vodmanager

# Streaming abre muitos descritores de arquivo ao mesmo tempo.
LimitNOFILE=65535

[Install]
WantedBy=multi-user.target
```

Ative:

```bash
sudo systemctl daemon-reload && sudo systemctl enable --now vodmanager && systemctl status vodmanager --no-pager
```

Verifique que respondeu:

```bash
curl -fsS http://localhost:8080/healthz && echo
```

Logs em tempo real:

```bash
sudo journalctl -u vodmanager -f
```

### 7. Firewall

```bash
sudo ufw allow OpenSSH && sudo ufw allow 8080/tcp && sudo ufw --force enable
```

### 8. Definir o endereço público — **este é o passo que faz o XC_VM funcionar**

Abra o painel em `http://IP_DO_SERVIDOR:8080`, entre e vá em **Configurações**.

Preencha o **endereço público** com o endereço pelo qual as outras máquinas alcançam
este servidor:

| Situação | O que preencher |
|---|---|
| XC_VM na mesma rede local | `http://192.168.1.50:8080` (o IP da máquina) |
| Servidor com IP público | `http://200.1.2.3:8080` |
| Com domínio e HTTPS | `https://vod.seudominio.com` |

**Por que o teste no XC_VM falhou antes:** o link saía com `localhost`. Para o navegador
da sua máquina, `localhost` é a própria máquina — por isso funcionou aí. Para o XC_VM,
`localhost` é *a máquina dele*, onde não existe VOD Manager nenhum. O endereço precisa
ser um que os dois lados enxerguem igual.

O painel avisa, na tela de Configurações e no link de reprodução, quando o endereço em
uso é local.

---

## Opcional: HTTPS com domínio

Só faz sentido se você tem um domínio apontando para o servidor. Sem domínio, pule.

```bash
sudo apt install -y nginx certbot python3-certbot-nginx
```

Crie `/etc/nginx/sites-available/vodmanager`:

```
server {
    listen 80;
    server_name vod.seudominio.com;

    location / {
        proxy_pass http://127.0.0.1:8080;
        proxy_http_version 1.1;
        proxy_set_header Host              $host;
        proxy_set_header X-Real-IP         $remote_addr;
        proxy_set_header X-Forwarded-For   $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;

        # Vídeo é resposta longa: não pode ser bufferizada nem cortada por timeout.
        proxy_buffering off;
        proxy_request_buffering off;
        proxy_read_timeout 1h;
        client_max_body_size 0;
    }
}
```

Ative e emita o certificado:

```bash
sudo ln -s /etc/nginx/sites-available/vodmanager /etc/nginx/sites-enabled/ && sudo nginx -t && sudo systemctl reload nginx && sudo certbot --nginx -d vod.seudominio.com
```

Com nginx na frente, ajuste três linhas do `/etc/vodmanager.env`:

```
VODM_HTTP_ADDR=127.0.0.1:8080
VODM_TRUST_PROXY=true
VODM_COOKIE_SECURE=true
```

Reinicie e feche a porta direta:

```bash
sudo systemctl restart vodmanager && sudo ufw delete allow 8080/tcp
```

O `proxy_buffering off` não é detalhe: com buffering ligado, o nginx tenta acumular a
resposta antes de repassar — e aqui a resposta é um filme inteiro.

---

## Atualizar

De dentro da pasta do código, com a versão nova já baixada:

```bash
go build -o vodmanager ./cmd/vodmanager && sudo systemctl stop vodmanager && sudo install -o vodmanager -g vodmanager -m 0755 vodmanager /opt/vodmanager/vodmanager && sudo systemctl start vodmanager
```

As migrations rodam sozinhas no boot, protegidas por advisory lock — dois processos
subindo ao mesmo tempo não se atropelam.

## Backup

O que precisa de backup é o banco **e a chave de criptografia**. Um sem o outro não
serve para nada.

```bash
sudo -u postgres pg_dump vodmanager | gzip > vodmanager-$(date +%F).sql.gz
```

Diário, por cron, guardado fora da máquina.

---

## Problemas comuns

| Sintoma | Causa provável |
|---|---|
| Serviço não sobe e o log fala de `DATABASE_URL` | Erro de digitação no `/etc/vodmanager.env`, ou senha do banco errada. |
| Painel abre, mas os links de vídeo não funcionam fora da máquina | Endereço público não configurado (passo 8). |
| Vídeo corta depois de alguns minutos, com nginx | Falta `proxy_buffering off` e `proxy_read_timeout`. |
| `Too many open files` no log | `LimitNOFILE` ausente na unit do systemd. |
| Sincronização lenta ou com erro de conexão | Limite de conexões simultâneas da própria fonte. |
